package dockerapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const defaultAPIVersion = "1.45"

// PortBinding represents a container port mapping from the Docker Engine API.
type PortBinding struct {
	IP          string `json:"IP,omitempty"`
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort,omitempty"`
	Type        string `json:"Type"`
}

// ContainerSummary represents a container entry returned by GET /containers/json.
type ContainerSummary struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Created int64             `json:"Created"`
	Labels  map[string]string `json:"Labels"`
	Ports   []PortBinding     `json:"Ports"`
}

// HealthLogEntry is a single healthcheck execution record from container inspect.
type HealthLogEntry struct {
	Start    string `json:"Start"`
	End      string `json:"End"`
	ExitCode int    `json:"ExitCode"`
	Output   string `json:"Output"`
}

// HealthState describes Docker's container healthcheck state.
type HealthState struct {
	Status        string           `json:"Status"`
	FailingStreak int              `json:"FailingStreak"`
	Log           []HealthLogEntry `json:"Log"`
}

// ContainerState describes the detailed runtime state from GET /containers/{id}/json.
type ContainerState struct {
	Status     string       `json:"Status"`
	Running    bool         `json:"Running"`
	Paused     bool         `json:"Paused"`
	Restarting bool         `json:"Restarting"`
	OOMKilled  bool         `json:"OOMKilled"`
	Dead       bool         `json:"Dead"`
	ExitCode   int          `json:"ExitCode"`
	Error      string       `json:"Error"`
	StartedAt  string       `json:"StartedAt"`
	FinishedAt string       `json:"FinishedAt"`
	Health     *HealthState `json:"Health,omitempty"`
}

// ContainerDetail represents the fields we read from GET /containers/{id}/json.
type ContainerDetail struct {
	ID           string         `json:"Id"`
	Name         string         `json:"Name"`
	RestartCount int            `json:"RestartCount"`
	State        ContainerState `json:"State"`
	Config       struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

// VersionInfo represents the subset of GET /version needed for API negotiation.
type VersionInfo struct {
	Version       string `json:"Version"`
	APIVersion    string `json:"ApiVersion"`
	MinAPIVersion string `json:"MinAPIVersion"`
}

// Client is a minimal, version-negotiating Docker Engine API client.
type Client struct {
	httpClient *http.Client
	baseURL    string

	mu         sync.Mutex
	apiVersion string
}

// NewClient creates a Docker Engine API client from a Docker endpoint URI
// (e.g. "unix:///var/run/docker.sock", "/var/run/docker.sock", or "http://127.0.0.1:2375").
func NewClient(endpoint string) (*Client, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		endpoint = "unix:///var/run/docker.sock"
	}

	transport := &http.Transport{
		DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
	}
	baseURL := "http://docker"

	switch {
	case strings.HasPrefix(endpoint, "unix://"):
		sockPath := strings.TrimPrefix(endpoint, "unix://")
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", sockPath)
		}
	case strings.HasPrefix(endpoint, "/"):
		sockPath := endpoint
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", sockPath)
		}
	case strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://"):
		u, err := url.Parse(endpoint)
		if err != nil {
			return nil, fmt.Errorf("parse docker endpoint %q: %w", endpoint, err)
		}
		baseURL = strings.TrimRight(u.String(), "/")
	case strings.HasPrefix(endpoint, "tcp://"):
		baseURL = "http://" + strings.TrimRight(strings.TrimPrefix(endpoint, "tcp://"), "/")
	default:
		return nil, fmt.Errorf("unsupported docker endpoint %q (expected unix://, http://, or tcp://)", endpoint)
	}

	return &Client{
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   15 * time.Second,
		},
		baseURL: baseURL,
	}, nil
}

// NegotiateVersion queries /version to pick a supported Engine API version.
func (c *Client) NegotiateVersion(ctx context.Context) (string, error) {
	c.mu.Lock()
	cached := c.apiVersion
	c.mu.Unlock()
	if cached != "" {
		return cached, nil
	}

	var ver VersionInfo
	if err := c.getJSON(ctx, "/version", &ver); err != nil {
		return "", err
	}
	picked := strings.TrimPrefix(strings.TrimSpace(ver.APIVersion), "v")
	if picked == "" {
		picked = defaultAPIVersion
	}

	c.mu.Lock()
	c.apiVersion = picked
	c.mu.Unlock()
	return picked, nil
}

// ListContainers returns all containers (running and stopped) from the Engine API.
func (c *Client) ListContainers(ctx context.Context) ([]ContainerSummary, error) {
	ver, err := c.NegotiateVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("negotiate docker api version: %w", err)
	}
	var out []ContainerSummary
	path := fmt.Sprintf("/v%s/containers/json?all=1", ver)
	if err := c.getJSON(ctx, path, &out); err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	return out, nil
}

// InspectContainer returns detailed container state and healthcheck log for one ID.
func (c *Client) InspectContainer(ctx context.Context, id string) (ContainerDetail, error) {
	if strings.TrimSpace(id) == "" {
		return ContainerDetail{}, errors.New("container id is required")
	}
	ver, err := c.NegotiateVersion(ctx)
	if err != nil {
		return ContainerDetail{}, fmt.Errorf("negotiate docker api version: %w", err)
	}
	var out ContainerDetail
	path := fmt.Sprintf("/v%s/containers/%s/json", ver, url.PathEscape(id))
	if err := c.getJSON(ctx, path, &out); err != nil {
		return ContainerDetail{}, fmt.Errorf("inspect container %s: %w", id, err)
	}
	return out, nil
}

func (c *Client) getJSON(ctx context.Context, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("docker engine HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}
