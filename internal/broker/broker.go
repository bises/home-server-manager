package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bises/home-server-manager/internal/config"
	"github.com/bises/home-server-manager/internal/dockerapi"
)

// DockerReader is the narrow read-only Docker Engine interface used by the broker.
type DockerReader interface {
	ListContainers(ctx context.Context) ([]dockerapi.ContainerSummary, error)
	InspectContainer(ctx context.Context, id string) (dockerapi.ContainerDetail, error)
}

// PublishedPort describes a published host-to-container port mapping.
type PublishedPort struct {
	IP            string `json:"ip,omitempty"`
	HostPort      int    `json:"host_port"`
	ContainerPort int    `json:"container_port"`
	Protocol      string `json:"protocol"`
}

// ServiceStatus is the broker's normalized view of one allowlisted Compose service.
type ServiceStatus struct {
	ID             string          `json:"id"`
	DisplayName    string          `json:"display_name"`
	Stack          string          `json:"stack"`
	Service        string          `json:"service"`
	Protected      bool            `json:"protected"`
	URL            string          `json:"url,omitempty"`
	Present        bool            `json:"present"`
	ContainerID    string          `json:"container_id,omitempty"`
	ContainerName  string          `json:"container_name,omitempty"`
	Image          string          `json:"image,omitempty"`
	State          string          `json:"state"`
	RawState       string          `json:"raw_state,omitempty"`
	Health         string          `json:"health"`
	StatusText     string          `json:"status_text,omitempty"`
	Ports          []PublishedPort `json:"ports"`
	AllowedActions []string        `json:"allowed_actions"`
}

// ServiceDetail extends ServiceStatus with container inspect metadata.
type ServiceDetail struct {
	ServiceStatus
	RestartCount     int    `json:"restart_count"`
	StartedAt        string `json:"started_at,omitempty"`
	FinishedAt       string `json:"finished_at,omitempty"`
	FailingStreak    int    `json:"failing_streak,omitempty"`
	LastHealthOutput string `json:"last_health_output,omitempty"`
	LastHealthEnd    string `json:"last_health_end,omitempty"`
}

// SnapshotResponse is returned by GET /v1/snapshot on the broker Unix socket.
type SnapshotResponse struct {
	SampledAt string          `json:"sampled_at"`
	ReadOnly  bool            `json:"read_only"`
	Services  []ServiceStatus `json:"services"`
}

// Broker owns the Docker Engine connection and enforces the host allowlist.
type Broker struct {
	cfg    config.Config
	docker DockerReader
}

// New creates a Broker for the given allowlist config and Docker reader.
func New(cfg config.Config, docker DockerReader) *Broker {
	return &Broker{cfg: cfg, docker: docker}
}

// Handler returns the HTTP handler exposed over the local Unix socket.
func (b *Broker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/snapshot", b.handleSnapshot)
	mux.HandleFunc("GET /v1/services/{id}", b.handleServiceDetail)
	mux.HandleFunc("POST /v1/services/{id}/actions/{verb}", b.handleAction)
	return mux
}

// ListenAndServeUnix binds a Unix domain socket with 0660 permissions and serves until ctx is cancelled.
func ListenAndServeUnix(ctx context.Context, socketPath string, handler http.Handler) error {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o750); err != nil {
		return fmt.Errorf("create socket dir: %w", err)
	}
	_ = os.Remove(socketPath)

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen unix %q: %w", socketPath, err)
	}
	if err := os.Chmod(socketPath, 0o660); err != nil {
		_ = ln.Close()
		return fmt.Errorf("chmod socket %q: %w", socketPath, err)
	}

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      20 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		_ = os.Remove(socketPath)
		return <-errCh
	case err := <-errCh:
		_ = os.Remove(socketPath)
		return err
	}
}

func (b *Broker) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	containers, err := b.docker.ListContainers(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "docker_unavailable", err.Error())
		return
	}

	byTarget := indexByComposeTarget(containers)
	services := make([]ServiceStatus, 0, len(b.cfg.Services))
	for _, svcCfg := range b.cfg.Services {
		key := svcCfg.Stack + "/" + svcCfg.Service
		c, found := byTarget[key]
		services = append(services, normalizeService(svcCfg, c, found, b.cfg.ReadOnly))
	}

	writeJSON(w, http.StatusOK, SnapshotResponse{
		SampledAt: time.Now().UTC().Format(time.RFC3339),
		ReadOnly:  b.cfg.ReadOnly,
		Services:  services,
	})
}

func (b *Broker) handleServiceDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	svcCfg, ok := b.cfg.FindService(id)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "unknown_service", "Service is not in the host allowlist")
		return
	}

	containers, err := b.docker.ListContainers(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "docker_unavailable", err.Error())
		return
	}

	byTarget := indexByComposeTarget(containers)
	c, found := byTarget[svcCfg.Stack+"/"+svcCfg.Service]
	base := normalizeService(svcCfg, c, found, b.cfg.ReadOnly)
	detail := ServiceDetail{ServiceStatus: base}

	if found {
		inspected, err := b.docker.InspectContainer(r.Context(), c.ID)
		if err != nil {
			writeJSONError(w, http.StatusServiceUnavailable, "docker_inspect_failed", err.Error())
			return
		}
		detail.RestartCount = inspected.RestartCount
		detail.StartedAt = inspected.State.StartedAt
		detail.FinishedAt = inspected.State.FinishedAt
		if inspected.State.Health != nil {
			detail.Health = normalizeHealthWord(inspected.State.Health.Status)
			detail.State = effectiveState(base.RawState, detail.Health)
			detail.FailingStreak = inspected.State.Health.FailingStreak
			if n := len(inspected.State.Health.Log); n > 0 {
				last := inspected.State.Health.Log[n-1]
				detail.LastHealthOutput = strings.TrimSpace(last.Output)
				detail.LastHealthEnd = last.End
			}
		}
	}

	writeJSON(w, http.StatusOK, detail)
}

func (b *Broker) handleAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := b.cfg.FindService(id); !ok {
		writeJSONError(w, http.StatusNotFound, "unknown_service", "Service is not in the host allowlist")
		return
	}
	if b.cfg.ReadOnly {
		writeJSONError(w, http.StatusForbidden, "read_only_mode", "Changes are disabled during the read-only pilot")
		return
	}
	writeJSONError(w, http.StatusForbidden, "action_not_enabled", "Service actions are not enabled in this release")
}

func indexByComposeTarget(containers []dockerapi.ContainerSummary) map[string]dockerapi.ContainerSummary {
	out := make(map[string]dockerapi.ContainerSummary, len(containers))
	for _, c := range containers {
		proj := c.Labels["com.docker.compose.project"]
		svc := c.Labels["com.docker.compose.service"]
		if proj == "" || svc == "" {
			continue
		}
		key := proj + "/" + svc
		// Prefer running container if multiple exist for the same service.
		if existing, exists := out[key]; !exists || (existing.State != "running" && c.State == "running") {
			out[key] = c
		}
	}
	return out
}

func normalizeService(cfg config.ServiceConfig, c dockerapi.ContainerSummary, found, readOnly bool) ServiceStatus {
	st := ServiceStatus{
		ID:             cfg.ID,
		DisplayName:    cfg.DisplayName,
		Stack:          cfg.Stack,
		Service:        cfg.Service,
		Protected:      cfg.Protected,
		URL:            cfg.URL,
		Present:        found,
		State:          "stopped",
		Health:         "none",
		Ports:          []PublishedPort{},
		AllowedActions: []string{},
	}
	if !readOnly && !cfg.Protected && len(cfg.AllowedActions) > 0 {
		st.AllowedActions = append(st.AllowedActions, cfg.AllowedActions...)
	}
	if !found {
		st.StatusText = "Not created"
		return st
	}

	st.ContainerID = c.ID
	if len(c.Names) > 0 {
		st.ContainerName = strings.TrimPrefix(c.Names[0], "/")
	}
	st.Image = c.Image
	st.RawState = strings.ToLower(c.State)
	st.StatusText = c.Status
	st.Health = healthFromStatus(c.Status)
	st.State = effectiveState(st.RawState, st.Health)
	st.Ports = extractPorts(c.Ports)
	return st
}

func healthFromStatus(status string) string {
	lower := strings.ToLower(status)
	switch {
	case strings.Contains(lower, "(unhealthy)"):
		return "unhealthy"
	case strings.Contains(lower, "(health: starting)"):
		return "starting"
	case strings.Contains(lower, "(healthy)"):
		return "healthy"
	default:
		return "none"
	}
}

func normalizeHealthWord(h string) string {
	switch strings.ToLower(strings.TrimSpace(h)) {
	case "healthy":
		return "healthy"
	case "unhealthy":
		return "unhealthy"
	case "starting":
		return "starting"
	default:
		return "none"
	}
}

func effectiveState(rawState, health string) string {
	if rawState == "running" {
		switch health {
		case "unhealthy":
			return "unhealthy"
		case "starting":
			return "starting"
		default:
			return "running"
		}
	}
	if rawState == "" {
		return "unknown"
	}
	return "stopped"
}

func extractPorts(bindings []dockerapi.PortBinding) []PublishedPort {
	seen := make(map[string]bool)
	ports := make([]PublishedPort, 0, len(bindings))
	for _, p := range bindings {
		if p.PublicPort <= 0 {
			continue
		}
		key := fmt.Sprintf("%s:%d->%d/%s", p.IP, p.PublicPort, p.PrivatePort, p.Type)
		if seen[key] {
			continue
		}
		seen[key] = true
		ports = append(ports, PublishedPort{
			IP:            p.IP,
			HostPort:      p.PublicPort,
			ContainerPort: p.PrivatePort,
			Protocol:      p.Type,
		})
	}
	sort.Slice(ports, func(i, j int) bool {
		if ports[i].HostPort != ports[j].HostPort {
			return ports[i].HostPort < ports[j].HostPort
		}
		return ports[i].ContainerPort < ports[j].ContainerPort
	})
	return ports
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func writeJSONError(w http.ResponseWriter, code int, errCode, message string) {
	writeJSON(w, code, map[string]any{
		"error":   errCode,
		"message": message,
	})
}
