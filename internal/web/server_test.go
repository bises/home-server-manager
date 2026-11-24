package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bises/home-server-manager/internal/broker"
	"github.com/bises/home-server-manager/internal/config"
	"github.com/bises/home-server-manager/internal/hostmetrics"
	"github.com/bises/home-server-manager/internal/snapshot"
)

type fakeBroker struct {
	snap broker.SnapshotResponse
	err  error
}

func (f *fakeBroker) FetchSnapshot(context.Context) (broker.SnapshotResponse, error) {
	if f.err != nil {
		return broker.SnapshotResponse{}, f.err
	}
	return f.snap, nil
}

func (f *fakeBroker) FetchServiceDetail(_ context.Context, id string) (broker.ServiceDetail, error) {
	for _, s := range f.snap.Services {
		if s.ID == id {
			return broker.ServiceDetail{ServiceStatus: s, RestartCount: 1}, nil
		}
	}
	return broker.ServiceDetail{}, errors.New("broker HTTP 404: unknown_service")
}

func (f *fakeBroker) SubmitAction(context.Context, string, string) (int, []byte, error) {
	return http.StatusForbidden, []byte(`{"error":"read_only_mode","message":"Changes are disabled during the read-only pilot"}`), nil
}

func newTestServer(t *testing.T, disableUI bool) (*Server, *fakeBroker) {
	t.Helper()
	cfg := config.Config{
		HostLabel:       "hsm-fixture-host",
		ReadOnly:        true,
		CacheTTLSeconds: 10,
		Mounts: []config.MountConfig{
			{ID: "root", Label: "Root storage", Path: "/"},
			{ID: "backup", Label: "Backup mount", Path: "/mnt/immich-backup"},
		},
		Services: []config.ServiceConfig{
			{ID: "fixture-web", DisplayName: "Fixture Web", Stack: "hsm-fixture", Service: "fixture-web"},
			{ID: "fixture-unhealthy", DisplayName: "Fixture Unhealthy", Stack: "hsm-fixture", Service: "fixture-unhealthy"},
			{ID: "fixture-stopped", DisplayName: "Fixture Stopped", Stack: "hsm-fixture", Service: "fixture-stopped"},
		},
	}
	fb := &fakeBroker{
		snap: broker.SnapshotResponse{
			SampledAt: "2026-11-24T20:00:00Z",
			ReadOnly:  true,
			Services: []broker.ServiceStatus{
				{ID: "fixture-web", DisplayName: "Fixture Web", Stack: "hsm-fixture", Service: "fixture-web", Present: true, State: "running", Health: "healthy", Ports: []broker.PublishedPort{{HostPort: 18080, ContainerPort: 80, Protocol: "tcp"}}},
				{ID: "fixture-unhealthy", DisplayName: "Fixture Unhealthy", Stack: "hsm-fixture", Service: "fixture-unhealthy", Present: true, State: "unhealthy", Health: "unhealthy", StatusText: "Up 1m (unhealthy)"},
				{ID: "fixture-stopped", DisplayName: "Fixture Stopped", Stack: "hsm-fixture", Service: "fixture-stopped", Present: true, State: "stopped", Health: "none", StatusText: "Exited (0) 5s ago"},
			},
		},
	}
	svc := snapshot.NewService(cfg, fb, hostmetrics.NewReader())
	srv, err := NewServer(svc, Options{DisableUI: disableUI})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv, fb
}

func TestDisableUIServesAPIOnly(t *testing.T) {
	srv, _ := newTestServer(t, true)
	h := srv.Handler()

	// UI routes must 404 when UI is disabled.
	for _, path := range []string{"/", "/services", "/activity", "/static/app.css"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404 for %s with DisableUI=true, got %d", path, rec.Code)
		}
	}

	// Snapshot API must work identically for Home Assistant.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for /api/v1/snapshot, got %d", rec.Code)
	}
	var snap snapshot.Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Freshness != "fresh" || snap.Summary.Running != 1 || snap.Summary.Unhealthy != 1 || snap.Summary.Stopped != 1 {
		t.Fatalf("unexpected snapshot: %+v", snap.Summary)
	}

	// Action endpoint must return 403 in Phase 1.
	recAct := httptest.NewRecorder()
	h.ServeHTTP(recAct, httptest.NewRequest(http.MethodPost, "/api/v1/services/fixture-web/actions/restart", nil))
	if recAct.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for action in Phase 1, got %d", recAct.Code)
	}
}

func TestUIRendersOverviewServicesAndStaleBanner(t *testing.T) {
	srv, fb := newTestServer(t, false)
	h := srv.Handler()

	// Overview renders attention items and disk labels.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /: %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"hsm-fixture-host", "Fixture Unhealthy", "Fixture Stopped", "Root storage", "Backup mount"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected %q in overview HTML", want)
		}
	}

	// Services filter works without JS.
	recSvc := httptest.NewRecorder()
	h.ServeHTTP(recSvc, httptest.NewRequest(http.MethodGet, "/services?status=unhealthy", nil))
	if recSvc.Code != http.StatusOK || !strings.Contains(recSvc.Body.String(), "Fixture Unhealthy") {
		t.Fatalf("expected filtered unhealthy service")
	}

	// Simulate broker/Docker failure -> stale banner appears on refresh=1.
	fb.err = errors.New("docker daemon stopped")
	recStale := httptest.NewRecorder()
	h.ServeHTTP(recStale, httptest.NewRequest(http.MethodGet, "/?refresh=1", nil))
	if recStale.Code != http.StatusOK || !strings.Contains(recStale.Body.String(), "Docker status stale") {
		t.Fatalf("expected stale status banner in overview")
	}
}
