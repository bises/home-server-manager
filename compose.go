package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	readTimeout   = 20 * time.Second
	actionTimeout = time.Minute
	pullTimeout   = 5 * time.Minute
)

var errComposeTimeout = errors.New("Docker Compose timed out")

type composeError struct {
	operation string
	cause     error
}

func (e *composeError) Error() string { return fmt.Sprintf("%s: %v", e.operation, e.cause) }
func (e *composeError) Unwrap() error { return e.cause }

var actionArgs = map[string][]string{
	"start":   {"start"},
	"stop":    {"stop"},
	"restart": {"restart"},
	"up":      {"up", "-d"},
	"down":    {"rm", "-s", "-f"},
}

type composeRunner func(ctx context.Context, file string, args ...string) ([]byte, error)

type project struct {
	path    string
	file    string
	display string
	actions sync.Mutex
}

type projectSet struct {
	files       []*project
	byPath      map[string]*project
	defaultFile *project
}

func loadProjects(paths, defaultPath string) (projectSet, error) {
	paths = strings.TrimSpace(paths)
	defaultPath = strings.TrimSpace(defaultPath)
	if paths == "" {
		paths = defaultPath
	}
	if paths == "" {
		paths = "./test-docker-compose.yml"
	}

	projects := projectSet{byPath: make(map[string]*project)}
	for _, path := range strings.Split(paths, ",") {
		path = strings.TrimSpace(path)
		if path == "" || projects.byPath[path] != nil {
			continue
		}
		file, err := filepath.Abs(path)
		if err != nil {
			return projectSet{}, fmt.Errorf("resolve Compose path %q: %w", path, err)
		}
		base := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
		display := strings.ReplaceAll(strings.TrimSuffix(base, "-compose"), "-", " ")
		p := &project{path: path, file: file, display: display}
		projects.files = append(projects.files, p)
		projects.byPath[path] = p
	}
	if len(projects.files) == 0 {
		return projectSet{}, errors.New("configure at least one Compose file")
	}
	projects.defaultFile = projects.byPath[defaultPath]
	if len(projects.files) == 1 {
		projects.defaultFile = projects.files[0]
	}
	return projects, nil
}

func (p projectSet) get(path string) *project {
	if path == "" {
		return p.defaultFile
	}
	return p.byPath[path]
}

func runCompose(ctx context.Context, file string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", append([]string{
		"compose", "--file", file, "--profile", "*",
	}, args...)...)
	cmd.Dir = filepath.Dir(file)
	cmd.WaitDelay = 5 * time.Second
	return cmd.Output()
}

func (p *project) command(parent context.Context, run composeRunner, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	out, err := run(ctx, p.file, args...)
	if ctx.Err() == context.DeadlineExceeded {
		return nil, errComposeTimeout
	}
	if err != nil {
		return nil, &composeError{operation: "Docker Compose " + args[0], cause: err}
	}
	return out, nil
}

type composeConfig struct {
	Name     string              `json:"name"`
	Services map[string]struct{} `json:"services"`
}

func (p *project) config(ctx context.Context, run composeRunner) (composeConfig, error) {
	out, err := p.command(ctx, run, readTimeout, "config", "--format", "json", "--no-env-resolution")
	if err != nil {
		return composeConfig{}, err
	}
	var cfg composeConfig
	if err := json.Unmarshal(out, &cfg); err != nil {
		return composeConfig{}, fmt.Errorf("decode Compose config: %w", err)
	}
	return cfg, nil
}

type publishedPort struct {
	HostPort      int
	ContainerPort int
	Protocol      string
}

type containerStatus struct {
	Name        string
	Service     string
	State       string
	Status      string
	Health      string
	Ports       []publishedPort
	ComposeFile string `json:"compose_file"`
	ProjectName string `json:"project_name"`
	DisplayName string `json:"display_name"`
}

type composeContainer struct {
	Name       string
	Service    string
	State      string
	Status     string
	Health     string
	Project    string
	Publishers []struct {
		PublishedPort int
		TargetPort    int
		Protocol      string
	}
}

func (p *project) status(ctx context.Context, run composeRunner, services ...string) ([]containerStatus, error) {
	args := []string{"ps", "--all", "--format", "json", "--orphans=false"}
	if len(services) > 0 {
		args = append(args, "--")
		args = append(args, services...)
	}
	out, err := p.command(ctx, run, readTimeout, args...)
	if err != nil {
		return nil, err
	}
	rows, err := decodeContainers(out)
	if err != nil {
		return nil, fmt.Errorf("decode Compose status: %w", err)
	}

	status := make([]containerStatus, 0, len(rows))
	for _, row := range rows {
		item := containerStatus{
			Name: row.Name, Service: row.Service, State: row.State, Status: row.Status,
			Health: row.Health, Ports: make([]publishedPort, 0, len(row.Publishers)),
			ComposeFile: p.path, ProjectName: row.Project, DisplayName: p.display,
		}
		for _, port := range row.Publishers {
			if port.PublishedPort > 0 {
				item.Ports = append(item.Ports, publishedPort{port.PublishedPort, port.TargetPort, port.Protocol})
			}
		}
		status = append(status, item)
	}
	return status, nil
}

func decodeContainers(out []byte) ([]composeContainer, error) {
	out = bytes.TrimSpace(out)
	var rows []composeContainer
	if len(out) > 0 && out[0] == '[' {
		err := json.Unmarshal(out, &rows)
		return rows, err
	}

	decoder := json.NewDecoder(bytes.NewReader(out))
	for {
		var row composeContainer
		err := decoder.Decode(&row)
		if err == io.EOF {
			return rows, nil
		}
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
}

func serviceArgs(args []string, name string) []string {
	return append(slices.Clone(args), "--", name)
}

func (p *project) pull(ctx context.Context, run composeRunner, service string) (string, error) {
	status, err := p.status(ctx, run, service)
	if err != nil {
		return "", err
	}
	if _, err := p.command(ctx, run, pullTimeout, "pull", "--policy", "always", "--", service); err != nil {
		return "", err
	}
	if len(status) == 0 {
		return "Image pulled for " + service + ". Use Bring Up to start it.", nil
	}

	args := []string{"up", "--no-deps", "--no-start"}
	if slices.ContainsFunc(status, func(c containerStatus) bool { return c.State == "running" }) {
		args = []string{"up", "--no-deps", "-d"}
	}
	if _, err := p.command(ctx, run, actionTimeout, serviceArgs(args, service)...); err != nil {
		return "", &composeError{operation: "Image pulled, but container update", cause: err}
	}
	return "Image pulled and container updated for " + service, nil
}
