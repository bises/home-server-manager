package dockerapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientNegotiateAndListContainers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			_ = json.NewEncoder(w).Encode(VersionInfo{Version: "29.8.0", APIVersion: "1.47"})
		case "/v1.47/containers/json":
			if r.URL.Query().Get("all") != "1" {
				t.Fatalf("expected all=1 query param")
			}
			_ = json.NewEncoder(w).Encode([]ContainerSummary{
				{
					ID:     "abc123",
					Names:  []string{"/hsm-fixture-web"},
					Image:  "nginx:alpine",
					State:  "running",
					Status: "Up 2 minutes (healthy)",
					Labels: map[string]string{
						"com.docker.compose.project": "hsm-fixture",
						"com.docker.compose.service": "fixture-web",
					},
					Ports: []PortBinding{{IP: "127.0.0.1", PublicPort: 18080, PrivatePort: 80, Type: "tcp"}},
				},
			})
		case "/v1.47/containers/abc123/json":
			_ = json.NewEncoder(w).Encode(ContainerDetail{
				ID:   "abc123",
				Name: "/hsm-fixture-web",
				State: ContainerState{
					Status:  "running",
					Running: true,
					Health:  &HealthState{Status: "healthy"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	containers, err := client.ListContainers(context.Background())
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	if len(containers) != 1 || containers[0].ID != "abc123" {
		t.Fatalf("unexpected containers: %+v", containers)
	}

	detail, err := client.InspectContainer(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("InspectContainer: %v", err)
	}
	if detail.State.Health == nil || detail.State.Health.Status != "healthy" {
		t.Fatalf("unexpected detail health: %+v", detail.State.Health)
	}
}
