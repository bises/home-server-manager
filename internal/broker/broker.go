package broker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bises/home-server-manager/internal/config"
	"github.com/bises/home-server-manager/internal/dockerapi"
)

const (
	defaultActionTimeout = 25 * time.Second
	maxAuditRecords      = 200
)

// DockerEngine is the narrow Docker Engine API interface used by the broker.
type DockerEngine interface {
	ListContainers(ctx context.Context) ([]dockerapi.ContainerSummary, error)
	InspectContainer(ctx context.Context, id string) (dockerapi.ContainerDetail, error)
	StartContainer(ctx context.Context, id string) error
	StopContainer(ctx context.Context, id string, timeoutSec int) error
	RestartContainer(ctx context.Context, id string, timeoutSec int) error
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

// Operation tracks one asynchronous container action and its verified outcome.
type Operation struct {
	ID            string `json:"id"`
	ServiceID     string `json:"service_id"`
	Verb          string `json:"verb"`
	Actor         string `json:"actor"`
	State         string `json:"state"` // accepted, running, succeeded, failed, unknown
	Message       string `json:"message"`
	VerifiedState string `json:"verified_state,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

// AuditRecord captures an action lifecycle event for the Activity view and host journal.
type AuditRecord struct {
	ID          string `json:"id"`
	OperationID string `json:"operation_id"`
	Timestamp   string `json:"timestamp"`
	Actor       string `json:"actor"`
	ServiceID   string `json:"service_id"`
	Verb        string `json:"verb"`
	Outcome     string `json:"outcome"`
	Message     string `json:"message"`
}

// ActionRequest carries caller identity from the web/API layer to the broker.
type ActionRequest struct {
	Actor string `json:"actor"`
}

// Broker owns the Docker Engine connection and enforces the host allowlist.
type Broker struct {
	cfg           config.Config
	docker        DockerEngine
	actionTimeout time.Duration

	mu         sync.Mutex
	inFlight   map[string]string
	operations map[string]*Operation
	audit      []AuditRecord
}

// New creates a Broker for the given allowlist config and Docker engine client.
func New(cfg config.Config, docker DockerEngine) *Broker {
	return &Broker{
		cfg:           cfg,
		docker:        docker,
		actionTimeout: defaultActionTimeout,
		inFlight:      make(map[string]string),
		operations:    make(map[string]*Operation),
		audit:         make([]AuditRecord, 0, 32),
	}
}

// Handler returns the HTTP handler exposed over the local Unix socket.
func (b *Broker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/snapshot", b.handleSnapshot)
	mux.HandleFunc("GET /v1/services/{id}", b.handleServiceDetail)
	mux.HandleFunc("POST /v1/services/{id}/actions/{verb}", b.handleAction)
	mux.HandleFunc("GET /v1/operations/{id}", b.handleOperation)
	mux.HandleFunc("GET /v1/activity", b.handleActivity)
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
	verb := strings.ToLower(strings.TrimSpace(r.PathValue("verb")))

	svcCfg, ok := b.cfg.FindService(id)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "unknown_service", "Service is not in the host allowlist")
		return
	}
	if b.cfg.ReadOnly {
		writeJSONError(w, http.StatusForbidden, "read_only_mode", "Changes are disabled during the read-only pilot")
		return
	}
	if svcCfg.Protected {
		writeJSONError(w, http.StatusForbidden, "protected_service", "Protected services are view-only")
		return
	}
	if !isSupportedVerb(verb) || !slices.Contains(svcCfg.AllowedActions, verb) {
		writeJSONError(w, http.StatusForbidden, "action_not_allowed", fmt.Sprintf("Action %q is not allowlisted for %s", verb, id))
		return
	}

	actor := "unknown"
	var req ActionRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err == nil && strings.TrimSpace(req.Actor) != "" {
		actor = strings.TrimSpace(req.Actor)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	opID := newID("op")

	b.mu.Lock()
	if existingOp, busy := b.inFlight[id]; busy {
		b.mu.Unlock()
		writeJSONError(w, http.StatusConflict, "action_in_progress", fmt.Sprintf("Action %s is already running for %s", existingOp, id))
		return
	}
	op := &Operation{
		ID:        opID,
		ServiceID: id,
		Verb:      verb,
		Actor:     actor,
		State:     "accepted",
		Message:   fmt.Sprintf("Accepted %s for %s", verb, svcCfg.DisplayName),
		CreatedAt: now,
		UpdatedAt: now,
	}
	b.inFlight[id] = opID
	b.operations[opID] = op
	b.appendAuditLocked(opID, actor, id, verb, "running", op.Message)
	snapOp := *op
	b.mu.Unlock()

	go b.executeOperation(opID, svcCfg, verb, actor)

	writeJSON(w, http.StatusAccepted, snapOp)
}

func (b *Broker) executeOperation(opID string, svcCfg config.ServiceConfig, verb, actor string) {
	defer func() {
		b.mu.Lock()
		delete(b.inFlight, svcCfg.ID)
		b.mu.Unlock()
	}()

	b.updateOperation(opID, "running", "Executing "+verb+" against Docker", "")

	ctx, cancel := context.WithTimeout(context.Background(), b.actionTimeout)
	defer cancel()

	containers, err := b.docker.ListContainers(ctx)
	if err != nil {
		outcome := "failed"
		if errors.Is(err, context.DeadlineExceeded) {
			outcome = "unknown"
		}
		b.finishOperation(opID, svcCfg.ID, verb, actor, outcome, "Could not list containers: "+err.Error(), "unknown")
		return
	}

	byTarget := indexByComposeTarget(containers)
	c, found := byTarget[svcCfg.Stack+"/"+svcCfg.Service]
	if !found {
		b.finishOperation(opID, svcCfg.ID, verb, actor, "failed", "Container does not exist; create/up is not enabled", "stopped")
		return
	}

	switch verb {
	case "start":
		err = b.docker.StartContainer(ctx, c.ID)
	case "stop":
		err = b.docker.StopContainer(ctx, c.ID, 10)
	case "restart":
		err = b.docker.RestartContainer(ctx, c.ID, 10)
	}

	// Always re-read Docker after the command completes or times out to verify state.
	verifyCtx, verifyCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer verifyCancel()
	verifiedState := "unknown"
	if after, vErr := b.docker.ListContainers(verifyCtx); vErr == nil {
		if c2, ok := indexByComposeTarget(after)[svcCfg.Stack+"/"+svcCfg.Service]; ok {
			verifiedState = normalizeService(svcCfg, c2, true, b.cfg.ReadOnly).State
		} else {
			verifiedState = "stopped"
		}
	}

	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			b.finishOperation(opID, svcCfg.ID, verb, actor, "unknown",
				fmt.Sprintf("%s timed out; current observed state is %s. Verify before retrying.", verb, verifiedState),
				verifiedState)
			return
		}
		b.finishOperation(opID, svcCfg.ID, verb, actor, "failed",
			fmt.Sprintf("%s failed: %v (observed state: %s)", verb, err, verifiedState),
			verifiedState)
		return
	}

	if matchesExpectedState(verb, verifiedState) {
		b.finishOperation(opID, svcCfg.ID, verb, actor, "succeeded",
			fmt.Sprintf("%s completed; %s is %s", verb, svcCfg.DisplayName, verifiedState),
			verifiedState)
		return
	}

	b.finishOperation(opID, svcCfg.ID, verb, actor, "unknown",
		fmt.Sprintf("%s finished, but observed state is %s", verb, verifiedState),
		verifiedState)
}

func matchesExpectedState(verb, state string) bool {
	switch verb {
	case "start", "restart":
		return state == "running" || state == "starting"
	case "stop":
		return state == "stopped"
	default:
		return false
	}
}

func (b *Broker) updateOperation(opID, state, message, verifiedState string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if op, ok := b.operations[opID]; ok {
		op.State = state
		op.Message = message
		if verifiedState != "" {
			op.VerifiedState = verifiedState
		}
		op.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
}

func (b *Broker) finishOperation(opID, serviceID, verb, actor, outcome, message, verifiedState string) {
	b.mu.Lock()
	if op, ok := b.operations[opID]; ok {
		op.State = outcome
		op.Message = message
		op.VerifiedState = verifiedState
		op.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	b.appendAuditLocked(opID, actor, serviceID, verb, outcome, message)
	b.mu.Unlock()

	log.Printf("[AUDIT] op=%s actor=%q service=%q verb=%q outcome=%q verified=%q msg=%q",
		opID, actor, serviceID, verb, outcome, verifiedState, message)
}

func (b *Broker) appendAuditLocked(opID, actor, serviceID, verb, outcome, message string) {
	rec := AuditRecord{
		ID:          newID("aud"),
		OperationID: opID,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		Actor:       actor,
		ServiceID:   serviceID,
		Verb:        verb,
		Outcome:     outcome,
		Message:     message,
	}
	// Prepend newest-first, bounded to maxAuditRecords.
	b.audit = append([]AuditRecord{rec}, b.audit...)
	if len(b.audit) > maxAuditRecords {
		b.audit = b.audit[:maxAuditRecords]
	}
}

func (b *Broker) handleOperation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	b.mu.Lock()
	op, ok := b.operations[id]
	var copyOp Operation
	if ok {
		copyOp = *op
	}
	b.mu.Unlock()
	if !ok {
		writeJSONError(w, http.StatusNotFound, "unknown_operation", "Operation not found")
		return
	}
	writeJSON(w, http.StatusOK, copyOp)
}

func (b *Broker) handleActivity(w http.ResponseWriter, _ *http.Request) {
	b.mu.Lock()
	events := make([]AuditRecord, len(b.audit))
	copy(events, b.audit)
	b.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"read_only": b.cfg.ReadOnly,
		"events":    events,
	})
}

func isSupportedVerb(verb string) bool {
	return verb == "start" || verb == "stop" || verb == "restart"
}

func newID(prefix string) string {
	var buf [6]byte
	_, _ = rand.Read(buf[:])
	return prefix + "-" + hex.EncodeToString(buf[:])
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
