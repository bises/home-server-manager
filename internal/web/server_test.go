package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/bises/home-server-manager/internal/auth"
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

func (f *fakeBroker) FetchOperation(_ context.Context, id string) (broker.Operation, error) {
	return broker.Operation{ID: id, ServiceID: "fixture-web", Verb: "restart", State: "succeeded"}, nil
}

func (f *fakeBroker) FetchActivity(context.Context) ([]broker.AuditRecord, error) {
	return []broker.AuditRecord{}, nil
}

func (f *fakeBroker) SubmitAction(_ context.Context, id, verb, actor string) (int, []byte, error) {
	if f.snap.ReadOnly {
		return http.StatusForbidden, []byte(`{"error":"read_only_mode","message":"Changes are disabled during the read-only pilot"}`), nil
	}
	b, _ := json.Marshal(broker.Operation{ID: "op-123", ServiceID: id, Verb: verb, Actor: actor, State: "accepted"})
	return http.StatusAccepted, b, nil
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

	// Action endpoint must return 401 when auth is not configured.
	recAct := httptest.NewRecorder()
	h.ServeHTTP(recAct, httptest.NewRequest(http.MethodPost, "/api/v1/services/fixture-web/actions/restart", nil))
	if recAct.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for action without auth configured, got %d", recAct.Code)
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

func TestPhase2AuthRoleScopeAndCSRF(t *testing.T) {
	cfg := config.Config{
		HostLabel: "hsm-fixture-host",
		ReadOnly:  false,
		Services: []config.ServiceConfig{
			{ID: "fixture-web", DisplayName: "Fixture Web", Stack: "hsm-fixture", Service: "fixture-web", AllowedActions: []string{"restart"}},
		},
	}
	fb := &fakeBroker{
		snap: broker.SnapshotResponse{
			SampledAt: "2026-11-24T20:00:00Z",
			ReadOnly:  false,
			Services: []broker.ServiceStatus{
				{ID: "fixture-web", DisplayName: "Fixture Web", Stack: "hsm-fixture", Service: "fixture-web", Present: true, State: "running", AllowedActions: []string{"restart"}},
			},
		},
	}
	mgr := auth.NewManager(auth.Config{
		Users: []auth.UserConfig{
			{Username: "viewer1", Role: "viewer", PasswordHash: auth.HashPassword("vpass")},
			{Username: "op1", Role: "operator", PasswordHash: auth.HashPassword("opass")},
		},
		Tokens: []auth.TokenConfig{
			{ID: "ha-read", Actor: "ha-sensors", TokenSHA256: auth.HashSecret("rtok"), Read: true},
			{ID: "ha-act", Actor: "ha-actions", TokenSHA256: auth.HashSecret("atok"), Actions: map[string][]string{"fixture-web": {"restart"}}},
		},
	})
	svc := snapshot.NewService(cfg, fb, hostmetrics.NewReader())
	srv, err := NewServer(svc, Options{Auth: mgr})
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()

	// Unauthenticated API -> 401; UI -> 303 redirect to /login.
	recUnauth := httptest.NewRecorder()
	h.ServeHTTP(recUnauth, httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil))
	if recUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", recUnauth.Code)
	}
	recUI := httptest.NewRecorder()
	h.ServeHTTP(recUI, httptest.NewRequest(http.MethodGet, "/", nil))
	if recUI.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect to /login, got %d", recUI.Code)
	}

	// Read-only token: reads snapshot (200), denied action (403).
	reqR := httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil)
	reqR.Header.Set("Authorization", "Bearer rtok")
	recR := httptest.NewRecorder()
	h.ServeHTTP(recR, reqR)
	if recR.Code != http.StatusOK {
		t.Fatalf("expected 200 for read token, got %d", recR.Code)
	}
	reqRAct := httptest.NewRequest(http.MethodPost, "/api/v1/services/fixture-web/actions/restart", nil)
	reqRAct.Header.Set("Authorization", "Bearer rtok")
	recRAct := httptest.NewRecorder()
	h.ServeHTTP(recRAct, reqRAct)
	if recRAct.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for read token acting, got %d", recRAct.Code)
	}

	// Action token: allowed restart on fixture-web (202), denied stop (403).
	reqA := httptest.NewRequest(http.MethodPost, "/api/v1/services/fixture-web/actions/restart", nil)
	reqA.Header.Set("Authorization", "Bearer atok")
	recA := httptest.NewRecorder()
	h.ServeHTTP(recA, reqA)
	if recA.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for scoped action token, got %d: %s", recA.Code, recA.Body.String())
	}
	reqAStop := httptest.NewRequest(http.MethodPost, "/api/v1/services/fixture-web/actions/stop", nil)
	reqAStop.Header.Set("Authorization", "Bearer atok")
	recAStop := httptest.NewRecorder()
	h.ServeHTTP(recAStop, reqAStop)
	if recAStop.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for unapproved verb, got %d", recAStop.Code)
	}

	// Browser operator login + CSRF enforcement.
	form := url.Values{"username": {"op1"}, "password": {"opass"}}
	reqLogin := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	reqLogin.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recLogin := httptest.NewRecorder()
	h.ServeHTTP(recLogin, reqLogin)
	if recLogin.Code != http.StatusSeeOther || len(recLogin.Result().Cookies()) == 0 {
		t.Fatalf("expected login cookie, got %d", recLogin.Code)
	}
	cookie := recLogin.Result().Cookies()[0]

	// Browser action without CSRF -> 403.
	badForm := url.Values{"verb": {"restart"}, "csrf_token": {"wrong"}}
	reqBadCSRF := httptest.NewRequest(http.MethodPost, "/services/fixture-web/action", strings.NewReader(badForm.Encode()))
	reqBadCSRF.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqBadCSRF.AddCookie(cookie)
	recBadCSRF := httptest.NewRecorder()
	h.ServeHTTP(recBadCSRF, reqBadCSRF)
	if recBadCSRF.Code != http.StatusForbidden {
		t.Fatalf("expected 403 on bad CSRF token, got %d", recBadCSRF.Code)
	}
}
