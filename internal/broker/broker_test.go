package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/bises/home-server-manager/internal/config"
	"github.com/bises/home-server-manager/internal/dockerapi"
)

type stubDocker struct {
	mu         sync.Mutex
	containers []dockerapi.ContainerSummary
	detail     dockerapi.ContainerDetail
	err        error
	actionErr  error
	delay      time.Duration
}

func (s *stubDocker) ListContainers(context.Context) ([]dockerapi.ContainerSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	out := make([]dockerapi.ContainerSummary, len(s.containers))
	copy(out, s.containers)
	return out, nil
}

func (s *stubDocker) InspectContainer(context.Context, string) (dockerapi.ContainerDetail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return dockerapi.ContainerDetail{}, s.err
	}
	return s.detail, nil
}

func (s *stubDocker) StartContainer(ctx context.Context, _ string) error {
	return s.doAction(ctx, "running", "Up 1 second")
}

func (s *stubDocker) StopContainer(ctx context.Context, _ string, _ int) error {
	return s.doAction(ctx, "exited", "Exited (0) 1 second ago")
}

func (s *stubDocker) RestartContainer(ctx context.Context, _ string, _ int) error {
	return s.doAction(ctx, "running", "Up 1 second (healthy)")
}

func (s *stubDocker) doAction(ctx context.Context, newState, newStatus string) error {
	s.mu.Lock()
	delay := s.delay
	aErr := s.actionErr
	s.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if aErr != nil {
		return aErr
	}
	s.mu.Lock()
	if len(s.containers) > 0 {
		s.containers[0].State = newState
		s.containers[0].Status = newStatus
	}
	s.mu.Unlock()
	return nil
}

func fixtureConfig(readOnly bool) config.Config {
	return config.Config{
		HostLabel: "test-host",
		ReadOnly:  readOnly,
		Services: []config.ServiceConfig{
			{ID: "fixture-web", DisplayName: "Fixture Web", Stack: "hsm-fixture", Service: "fixture-web", AllowedActions: []string{"start", "stop", "restart"}, URL: "http://127.0.0.1:18080"},
			{ID: "fixture-unhealthy", DisplayName: "Fixture Unhealthy", Stack: "hsm-fixture", Service: "fixture-unhealthy", AllowedActions: []string{"restart"}},
			{ID: "fixture-stopped", DisplayName: "Fixture Stopped", Stack: "hsm-fixture", Service: "fixture-stopped", AllowedActions: []string{"start"}},
			{ID: "fixture-db", DisplayName: "Fixture Postgres", Stack: "hsm-fixture", Service: "fixture-db", Protected: true, AllowedActions: []string{"restart"}},
		},
	}
}

func TestBrokerSnapshotAndReadOnlyDenial(t *testing.T) {
	b := New(fixtureConfig(true), &stubDocker{
		containers: []dockerapi.ContainerSummary{
			{
				ID:     "c1",
				Names:  []string{"/hsm-fixture-web"},
				Image:  "nginx:alpine",
				State:  "running",
				Status: "Up 1m (healthy)",
				Labels: map[string]string{"com.docker.compose.project": "hsm-fixture", "com.docker.compose.service": "fixture-web"},
				Ports:  []dockerapi.PortBinding{{IP: "127.0.0.1", PublicPort: 18080, PrivatePort: 80, Type: "tcp"}},
			},
			{
				ID:     "c2",
				Names:  []string{"/hsm-fixture-unhealthy"},
				Image:  "alpine:3.20",
				State:  "running",
				Status: "Up 1m (unhealthy)",
				Labels: map[string]string{"com.docker.compose.project": "hsm-fixture", "com.docker.compose.service": "fixture-unhealthy"},
			},
			{
				ID:     "c3",
				Names:  []string{"/hsm-fixture-stopped"},
				Image:  "alpine:3.20",
				State:  "exited",
				Status: "Exited (0) 10s ago",
				Labels: map[string]string{"com.docker.compose.project": "hsm-fixture", "com.docker.compose.service": "fixture-stopped"},
			},
		},
	})

	h := b.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/snapshot", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var snap SnapshotResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Services) != 4 {
		t.Fatalf("expected 4 allowlisted services, got %d", len(snap.Services))
	}
	if snap.Services[0].State != "running" || snap.Services[1].State != "unhealthy" || snap.Services[2].State != "stopped" {
		t.Fatalf("unexpected states: %+v", snap.Services)
	}

	recAct := httptest.NewRecorder()
	h.ServeHTTP(recAct, httptest.NewRequest(http.MethodPost, "/v1/services/fixture-web/actions/restart", nil))
	if recAct.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden in read-only mode, got %d", recAct.Code)
	}
}

func TestBrokerActionLifecycleConcurrencyTimeoutAndProtected(t *testing.T) {
	sd := &stubDocker{
		delay: 40 * time.Millisecond,
		containers: []dockerapi.ContainerSummary{
			{
				ID: "c1", Names: []string{"/hsm-fixture-web"}, Image: "nginx:alpine",
				State: "running", Status: "Up 1m (healthy)",
				Labels: map[string]string{"com.docker.compose.project": "hsm-fixture", "com.docker.compose.service": "fixture-web"},
			},
			{
				ID: "c4", Names: []string{"/hsm-fixture-db"}, Image: "postgres:15-alpine",
				State: "running", Status: "Up 1m (healthy)",
				Labels: map[string]string{"com.docker.compose.project": "hsm-fixture", "com.docker.compose.service": "fixture-db"},
			},
		},
	}
	b := New(fixtureConfig(false), sd)
	h := b.Handler()

	// Protected service must be denied even when ReadOnly=false.
	recProt := httptest.NewRecorder()
	h.ServeHTTP(recProt, httptest.NewRequest(http.MethodPost, "/v1/services/fixture-db/actions/restart", bytes.NewBufferString(`{"actor":"admin"}`)))
	if recProt.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for protected service, got %d", recProt.Code)
	}

	// Disallowed verb for service.
	recVerb := httptest.NewRecorder()
	h.ServeHTTP(recVerb, httptest.NewRequest(http.MethodPost, "/v1/services/fixture-unhealthy/actions/stop", bytes.NewBufferString(`{"actor":"admin"}`)))
	if recVerb.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-allowlisted verb, got %d", recVerb.Code)
	}

	// Start restart on fixture-web -> 202 Accepted.
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, httptest.NewRequest(http.MethodPost, "/v1/services/fixture-web/actions/restart", bytes.NewBufferString(`{"actor":"home-assistant"}`)))
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec1.Code, rec1.Body.String())
	}
	var op Operation
	if err := json.Unmarshal(rec1.Body.Bytes(), &op); err != nil {
		t.Fatal(err)
	}

	// Concurrent second request on same service -> 409 Conflict.
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodPost, "/v1/services/fixture-web/actions/stop", bytes.NewBufferString(`{"actor":"admin"}`)))
	if rec2.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict for concurrent action, got %d", rec2.Code)
	}

	// Poll operation until succeeded.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		recOp := httptest.NewRecorder()
		h.ServeHTTP(recOp, httptest.NewRequest(http.MethodGet, "/v1/operations/"+op.ID, nil))
		_ = json.Unmarshal(recOp.Body.Bytes(), &op)
		if op.State == "succeeded" {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if op.State != "succeeded" || op.VerifiedState != "running" {
		t.Fatalf("expected succeeded operation, got %+v", op)
	}

	// Activity log must contain the audit record.
	recAct := httptest.NewRecorder()
	h.ServeHTTP(recAct, httptest.NewRequest(http.MethodGet, "/v1/activity", nil))
	if recAct.Code != http.StatusOK || !bytes.Contains(recAct.Body.Bytes(), []byte("home-assistant")) {
		t.Fatalf("expected audit record in activity: %s", recAct.Body.String())
	}

	// Timeout test -> state becomes unknown.
	b.actionTimeout = 10 * time.Millisecond
	sd.mu.Lock()
	sd.delay = 80 * time.Millisecond
	sd.mu.Unlock()
	recTimeout := httptest.NewRecorder()
	h.ServeHTTP(recTimeout, httptest.NewRequest(http.MethodPost, "/v1/services/fixture-web/actions/restart", bytes.NewBufferString(`{"actor":"admin"}`)))
	if recTimeout.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", recTimeout.Code)
	}
	var opT Operation
	_ = json.Unmarshal(recTimeout.Body.Bytes(), &opT)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		recOp := httptest.NewRecorder()
		h.ServeHTTP(recOp, httptest.NewRequest(http.MethodGet, "/v1/operations/"+opT.ID, nil))
		_ = json.Unmarshal(recOp.Body.Bytes(), &opT)
		if opT.State == "unknown" || opT.State == "failed" {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if opT.State != "unknown" {
		t.Fatalf("expected unknown state on timeout, got %+v", opT)
	}
}

func TestBrokerDockerUnavailable(t *testing.T) {
	b := New(fixtureConfig(true), &stubDocker{err: errors.New("dial unix: no such file")})
	rec := httptest.NewRecorder()
	b.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/snapshot", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when Docker is unavailable, got %d", rec.Code)
	}
}
