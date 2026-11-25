package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/bises/home-server-manager/internal/auth"
	"github.com/bises/home-server-manager/internal/broker"
)

type testDiscovery struct {
	lastChange broker.WatchChange
}

func (d *testDiscovery) DiscoverContainers(context.Context) ([]broker.DiscoveryEntry, error) {
	return []broker.DiscoveryEntry{{Name: "example-app", Present: true, State: "running"}}, nil
}

func (d *testDiscovery) ChangeWatchlist(_ context.Context, change broker.WatchChange) error {
	d.lastChange = change
	return nil
}

func TestSetupRequiresOperatorAndCSRF(t *testing.T) {
	srv, _ := newTestServer(t, false)
	srv.auth = auth.NewManager(auth.Config{Users: []auth.UserConfig{
		{Username: "operator", Role: "operator", PasswordHash: auth.HashPassword("test-operator-password")},
		{Username: "viewer", Role: "viewer", PasswordHash: auth.HashPassword("test-viewer-password")},
	}})
	discovery := &testDiscovery{}
	srv.discovery = discovery
	h := srv.Handler()
	serve := func(method string, token string, values url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/setup", strings.NewReader(values.Encode()))
		if token != "" {
			req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := serve(http.MethodGet, "", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("unauthenticated setup returned %d", rec.Code)
	}
	_, viewerCookie, _ := srv.auth.AuthenticateUser("viewer", "test-viewer-password")
	if rec := serve(http.MethodGet, viewerCookie, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer setup returned %d", rec.Code)
	}
	operator, operatorCookie, _ := srv.auth.AuthenticateUser("operator", "test-operator-password")
	if rec := serve(http.MethodGet, operatorCookie, nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "example-app") {
		t.Fatalf("operator setup returned %d or omitted the discovered app", rec.Code)
	}

	values := url.Values{"action": {"add"}, "container": {"example-app"}}
	if rec := serve(http.MethodPost, operatorCookie, values); rec.Code != http.StatusForbidden || discovery.lastChange.Action != "" {
		t.Fatalf("request without CSRF changed the watchlist or returned %d", rec.Code)
	}
	values.Set("csrf_token", operator.CSRFToken)
	if rec := serve(http.MethodPost, operatorCookie, values); rec.Code != http.StatusSeeOther ||
		len(discovery.lastChange.Names) != 1 || discovery.lastChange.Names[0] != "example-app" {
		t.Fatalf("validated setup update returned %d or did not reach the broker", rec.Code)
	}
}

func TestLocalSetupAllowsSameOriginMonitoringOnly(t *testing.T) {
	srv, _ := newTestServer(t, false)
	discovery := &testDiscovery{}
	srv.localSetup = true
	srv.discovery = discovery
	h := srv.Handler()
	for _, tc := range []struct {
		method string
		target string
		origin string
		want   int
	}{
		{http.MethodGet, "http://127.0.0.1:8091/setup", "", http.StatusOK},
		{http.MethodGet, "http://example.test/setup", "", http.StatusForbidden},
		{http.MethodPost, "http://127.0.0.1:8091/setup", "", http.StatusForbidden},
		{http.MethodPost, "http://127.0.0.1:8091/setup", "http://127.0.0.1:9000", http.StatusForbidden},
		{http.MethodPost, "http://127.0.0.1:8091/setup", "http://127.0.0.1:8091", http.StatusSeeOther},
		{http.MethodPost, "http://127.0.0.1:8091/api/v1/services/fixture-web/actions/restart", "http://127.0.0.1:8091", http.StatusUnauthorized},
	} {
		discovery.lastChange = broker.WatchChange{}
		req := httptest.NewRequest(tc.method, tc.target, strings.NewReader("action=add&container=example-app"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s %s (Origin %q) returned %d, want %d", tc.method, tc.target, tc.origin, rec.Code, tc.want)
		}
		if (discovery.lastChange.Action == "add") != (tc.want == http.StatusSeeOther) {
			t.Errorf("%s %s unexpectedly changed or failed to change the watch list", tc.method, tc.target)
		}
	}
}
