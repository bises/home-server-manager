package broker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bises/home-server-manager/internal/config"
	"github.com/bises/home-server-manager/internal/dockerapi"
)

func TestDiscoveredWatchesPersistAndCannotControlDocker(t *testing.T) {
	cfg := config.Config{Services: []config.ServiceConfig{
		{ID: "configured", Stack: "example", Service: "configured", DisplayName: "Configured"},
	}}
	engine := &stubDocker{containers: []dockerapi.ContainerSummary{
		{ID: "configured-id", Names: []string{"/configured"}, State: "running", Labels: map[string]string{
			"com.docker.compose.project": "example", "com.docker.compose.service": "configured",
		}},
		{ID: "watch-running", Names: []string{"/selected"}, State: "running"},
		{ID: "watch-stopped", Names: []string{"/stopped"}, State: "exited"},
	}}
	path := filepath.Join(t.TempDir(), "watchlist.json")
	newHandler := func() http.Handler {
		t.Helper()
		b := New(cfg, engine)
		if err := b.LoadWatchlist(path); err != nil {
			t.Fatal(err)
		}
		return b.Handler()
	}
	request := func(h http.Handler, method, path, body string, code int) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		if rec.Code != code {
			t.Fatalf("%s %s returned %d, want %d. %s", method, path, rec.Code, code, rec.Body)
		}
		return rec
	}

	h := newHandler()
	rec := request(h, http.MethodGet, "/v1/discovery", "", http.StatusOK)
	var entries []DiscoveryEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || !entries[0].Managed || entries[2].State != "stopped" {
		t.Fatalf("unexpected discovery entries: %+v", entries)
	}
	request(h, http.MethodPost, "/v1/discovery", "{\"action\":\"add\",\"names\":[\"selected\", \"missing\"]}", http.StatusConflict)
	rec = request(h, http.MethodGet, "/v1/snapshot", "", http.StatusOK)
	var status SnapshotResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil || len(status.Services) != 1 {
		t.Fatalf("failed selection changed the watchlist: %+v (%v)", status.Services, err)
	}
	request(h, http.MethodPost, "/v1/discovery", "{\"action\":\"add-all\"}", http.StatusNoContent)

	h = newHandler()
	rec = request(h, http.MethodGet, "/v1/snapshot", "", http.StatusOK)
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Services) != 3 || status.Services[1].ID != "container-selected" ||
		!status.Services[1].Protected || len(status.Services[1].AllowedActions) != 0 {
		t.Fatalf("unexpected persisted watchlist: %+v", status.Services)
	}
	request(h, http.MethodGet, "/v1/services/container-selected", "", http.StatusOK)
	request(h, http.MethodPost, "/v1/services/container-selected/actions/restart", "", http.StatusNotFound)
	request(h, http.MethodPost, "/v1/discovery", "{\"action\":\"remove\",\"names\":[\"selected\"]}", http.StatusNoContent)
	request(newHandler(), http.MethodGet, "/v1/services/container-selected", "", http.StatusNotFound)
}
