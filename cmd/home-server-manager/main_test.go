package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bises/home-server-manager/internal/auth"
	"github.com/bises/home-server-manager/internal/broker"
	"github.com/bises/home-server-manager/internal/config"
	"github.com/bises/home-server-manager/internal/dockerapi"
	"github.com/bises/home-server-manager/internal/hostmetrics"
	"github.com/bises/home-server-manager/internal/snapshot"
	"github.com/bises/home-server-manager/internal/web"
)

func TestEndToEndUnixSocketBrokerAndWeb(t *testing.T) {
	var dockerDown atomic.Bool

	dockerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dockerDown.Load() {
			http.Error(w, "daemon stopped", http.StatusServiceUnavailable)
			return
		}
		switch r.URL.Path {
		case "/version":
			_ = json.NewEncoder(w).Encode(dockerapi.VersionInfo{Version: "29.8.0", APIVersion: "1.47"})
		case "/v1.47/containers/json":
			_ = json.NewEncoder(w).Encode([]dockerapi.ContainerSummary{
				{
					ID: "id-web", Names: []string{"/hsm-fixture-web"}, Image: "nginx:alpine",
					State: "running", Status: "Up 2m (healthy)",
					Labels: map[string]string{"com.docker.compose.project": "hsm-fixture", "com.docker.compose.service": "fixture-web"},
					Ports:  []dockerapi.PortBinding{{IP: "127.0.0.1", PublicPort: 18080, PrivatePort: 80, Type: "tcp"}},
				},
				{
					ID: "id-unhealthy", Names: []string{"/hsm-fixture-unhealthy"}, Image: "alpine:3.20",
					State: "running", Status: "Up 1m (unhealthy)",
					Labels: map[string]string{"com.docker.compose.project": "hsm-fixture", "com.docker.compose.service": "fixture-unhealthy"},
				},
				{
					ID: "id-stopped", Names: []string{"/hsm-fixture-stopped"}, Image: "alpine:3.20",
					State: "exited", Status: "Exited (0) 10s ago",
					Labels: map[string]string{"com.docker.compose.project": "hsm-fixture", "com.docker.compose.service": "fixture-stopped"},
				},
				{
					ID: "id-db", Names: []string{"/hsm-fixture-db"}, Image: "postgres:15-alpine",
					State: "running", Status: "Up 2m (healthy)",
					Labels: map[string]string{"com.docker.compose.project": "hsm-fixture", "com.docker.compose.service": "fixture-db"},
				},
			})
		case "/v1.47/containers/id-unhealthy/json":
			_ = json.NewEncoder(w).Encode(dockerapi.ContainerDetail{
				ID: "id-unhealthy", Name: "/hsm-fixture-unhealthy", RestartCount: 0,
				State: dockerapi.ContainerState{
					Status: "running", Running: true,
					Health: &dockerapi.HealthState{
						Status: "unhealthy", FailingStreak: 3,
						Log: []dockerapi.HealthLogEntry{{ExitCode: 1, Output: "false", End: "2026-11-24T20:00:00Z"}},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer dockerSrv.Close()

	cfg, err := config.Load(filepath.Join("..", "..", "fixtures", "manager.json"))
	if err != nil {
		t.Fatalf("load fixture config: %v", err)
	}
	dc, err := dockerapi.NewClient(dockerSrv.URL)
	if err != nil {
		t.Fatalf("new docker client: %v", err)
	}

	sockPath := filepath.Join(t.TempDir(), "broker.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := broker.New(cfg, dc)
	go func() {
		_ = broker.ListenAndServeUnix(ctx, sockPath, b.Handler())
	}()

	// Wait briefly for socket listener to bind.
	bc := snapshot.NewUnixBrokerClient(sockPath)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := bc.FetchSnapshot(ctx); err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	snapSvc := snapshot.NewService(cfg, bc, hostmetrics.NewReader())

	// API-only mode (Home Assistant)
	apiOnlySrv, err := web.NewServer(snapSvc, web.Options{DisableUI: true})
	if err != nil {
		t.Fatal(err)
	}
	apiHandler := apiOnlySrv.Handler()

	recRoot := httptest.NewRecorder()
	apiHandler.ServeHTTP(recRoot, httptest.NewRequest(http.MethodGet, "/", nil))
	if recRoot.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for / when UI is disabled, got %d", recRoot.Code)
	}

	recSnap := httptest.NewRecorder()
	apiHandler.ServeHTTP(recSnap, httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil))
	if recSnap.Code != http.StatusOK {
		t.Fatalf("expected 200 for /api/v1/snapshot, got %d", recSnap.Code)
	}
	var snap snapshot.Snapshot
	if err := json.Unmarshal(recSnap.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Freshness != "fresh" || snap.Summary.Running != 2 || snap.Summary.Unhealthy != 1 || snap.Summary.Stopped != 1 {
		t.Fatalf("unexpected snapshot summary: %+v", snap.Summary)
	}

	// Detail endpoint over Unix socket
	recDet := httptest.NewRecorder()
	apiHandler.ServeHTTP(recDet, httptest.NewRequest(http.MethodGet, "/api/v1/services/fixture-unhealthy", nil))
	if recDet.Code != http.StatusOK {
		t.Fatalf("expected 200 for detail, got %d", recDet.Code)
	}

	// Web denies unauthenticated mutations before reaching the broker.
	recAct := httptest.NewRecorder()
	apiHandler.ServeHTTP(recAct, httptest.NewRequest(http.MethodPost, "/api/v1/services/fixture-web/actions/restart", nil))
	if recAct.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated action, got %d", recAct.Code)
	}

	// Docker goes down -> stale state preserved
	dockerDown.Store(true)
	recStale := httptest.NewRecorder()
	apiHandler.ServeHTTP(recStale, httptest.NewRequest(http.MethodGet, "/api/v1/snapshot?refresh=1", nil))
	if recStale.Code != http.StatusOK {
		t.Fatalf("expected 200 on stale snapshot, got %d", recStale.Code)
	}
	var stale snapshot.Snapshot
	if err := json.Unmarshal(recStale.Body.Bytes(), &stale); err != nil {
		t.Fatal(err)
	}
	if stale.Freshness != "stale" || stale.DockerAvailable || stale.Summary.Running != 2 {
		t.Fatalf("expected stale snapshot with preserved counts, got %+v", stale)
	}
}

func TestPhase2HomeAssistantRestartOverUnixSocket(t *testing.T) {
	dockerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			_ = json.NewEncoder(w).Encode(dockerapi.VersionInfo{Version: "29.8.0", APIVersion: "1.47"})
		case "/v1.47/containers/json":
			_ = json.NewEncoder(w).Encode([]dockerapi.ContainerSummary{
				{
					ID: "id-web", Names: []string{"/hsm-fixture-web"}, Image: "nginx:alpine",
					State: "running", Status: "Up 2m (healthy)",
					Labels: map[string]string{"com.docker.compose.project": "hsm-fixture", "com.docker.compose.service": "fixture-web"},
				},
			})
		case "/v1.47/containers/id-web/restart":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer dockerSrv.Close()

	cfg, err := config.Load(filepath.Join("..", "..", "fixtures", "manager-phase2.json"))
	if err != nil {
		t.Fatal(err)
	}
	dc, err := dockerapi.NewClient(dockerSrv.URL)
	if err != nil {
		t.Fatal(err)
	}

	sockPath := filepath.Join(t.TempDir(), "broker.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := broker.New(cfg, dc)
	go func() {
		_ = broker.ListenAndServeUnix(ctx, sockPath, b.Handler())
	}()

	bc := snapshot.NewUnixBrokerClient(sockPath)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := bc.FetchSnapshot(ctx); err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	authMgr := auth.NewManager(auth.Config{
		Tokens: []auth.TokenConfig{
			{ID: "ha-act", Actor: "home-assistant", TokenSHA256: auth.HashSecret("ha-secret"), Read: true, Actions: map[string][]string{"fixture-web": {"restart"}}},
		},
	})
	snapSvc := snapshot.NewService(cfg, bc, hostmetrics.NewReader())
	srv, err := web.NewServer(snapSvc, web.Options{DisableUI: true, Auth: authMgr})
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()

	// Home Assistant triggers restart with UI disabled.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/services/fixture-web/actions/restart", nil)
	req.Header.Set("Authorization", "Bearer ha-secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d: %s", rec.Code, rec.Body.String())
	}
	var op broker.Operation
	if err := json.Unmarshal(rec.Body.Bytes(), &op); err != nil {
		t.Fatal(err)
	}

	// Poll /api/v1/operations/{id} until succeeded.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		reqOp := httptest.NewRequest(http.MethodGet, "/api/v1/operations/"+op.ID, nil)
		reqOp.Header.Set("Authorization", "Bearer ha-secret")
		recOp := httptest.NewRecorder()
		h.ServeHTTP(recOp, reqOp)
		_ = json.Unmarshal(recOp.Body.Bytes(), &op)
		if op.State == "succeeded" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if op.State != "succeeded" || op.Actor != "home-assistant" {
		t.Fatalf("expected succeeded operation for home-assistant, got %+v", op)
	}
}
