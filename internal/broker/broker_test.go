package broker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bises/home-server-manager/internal/config"
	"github.com/bises/home-server-manager/internal/dockerapi"
)

type stubDocker struct {
	containers []dockerapi.ContainerSummary
	detail     dockerapi.ContainerDetail
	err        error
}

func (s stubDocker) ListContainers(context.Context) ([]dockerapi.ContainerSummary, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.containers, nil
}

func (s stubDocker) InspectContainer(context.Context, string) (dockerapi.ContainerDetail, error) {
	if s.err != nil {
		return dockerapi.ContainerDetail{}, s.err
	}
	return s.detail, nil
}

func fixtureConfig() config.Config {
	return config.Config{
		HostLabel: "test-host",
		ReadOnly:  true,
		Services: []config.ServiceConfig{
			{ID: "fixture-web", DisplayName: "Fixture Web", Stack: "hsm-fixture", Service: "fixture-web", URL: "http://127.0.0.1:18080"},
			{ID: "fixture-unhealthy", DisplayName: "Fixture Unhealthy", Stack: "hsm-fixture", Service: "fixture-unhealthy"},
			{ID: "fixture-stopped", DisplayName: "Fixture Stopped", Stack: "hsm-fixture", Service: "fixture-stopped"},
		},
	}
}

func TestBrokerSnapshotAndReadOnlyDenial(t *testing.T) {
	b := New(fixtureConfig(), stubDocker{
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
			{
				ID:     "c-unlisted",
				Names:  []string{"/unrelated-service"},
				Image:  "busybox",
				State:  "running",
				Status: "Up 1h",
				Labels: map[string]string{"com.docker.compose.project": "other", "com.docker.compose.service": "other"},
			},
		},
	})

	h := b.Handler()

	// 1. Snapshot returns only allowlisted services with normalized states.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/snapshot", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var snap SnapshotResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Services) != 3 {
		t.Fatalf("expected 3 allowlisted services, got %d", len(snap.Services))
	}
	if snap.Services[0].State != "running" || snap.Services[0].Health != "healthy" || len(snap.Services[0].Ports) != 1 {
		t.Fatalf("unexpected web state: %+v", snap.Services[0])
	}
	if snap.Services[1].State != "unhealthy" {
		t.Fatalf("expected unhealthy state, got %+v", snap.Services[1])
	}
	if snap.Services[2].State != "stopped" {
		t.Fatalf("expected stopped state, got %+v", snap.Services[2])
	}

	// 2. Action endpoint must deny mutations during Phase 1 read-only pilot.
	recAct := httptest.NewRecorder()
	h.ServeHTTP(recAct, httptest.NewRequest(http.MethodPost, "/v1/services/fixture-web/actions/restart", nil))
	if recAct.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden in read-only mode, got %d", recAct.Code)
	}

	// 3. Unknown service must return 404.
	recUnknown := httptest.NewRecorder()
	h.ServeHTTP(recUnknown, httptest.NewRequest(http.MethodGet, "/v1/services/not-allowlisted", nil))
	if recUnknown.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown service, got %d", recUnknown.Code)
	}
}

func TestBrokerDockerUnavailable(t *testing.T) {
	b := New(fixtureConfig(), stubDocker{err: errors.New("dial unix: no such file")})
	rec := httptest.NewRecorder()
	b.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/snapshot", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when Docker is unavailable, got %d", rec.Code)
	}
}
