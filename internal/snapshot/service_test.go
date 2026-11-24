package snapshot

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/bises/home-server-manager/internal/broker"
	"github.com/bises/home-server-manager/internal/config"
	"github.com/bises/home-server-manager/internal/hostmetrics"
)

type stubBroker struct {
	snap broker.SnapshotResponse
	err  error
}

func (s *stubBroker) FetchSnapshot(context.Context) (broker.SnapshotResponse, error) {
	if s.err != nil {
		return broker.SnapshotResponse{}, s.err
	}
	return s.snap, nil
}

func (s *stubBroker) FetchServiceDetail(context.Context, string) (broker.ServiceDetail, error) {
	return broker.ServiceDetail{}, s.err
}

func (s *stubBroker) FetchOperation(context.Context, string) (broker.Operation, error) {
	return broker.Operation{}, s.err
}

func (s *stubBroker) FetchActivity(context.Context) ([]broker.AuditRecord, error) {
	return nil, s.err
}

func (s *stubBroker) SubmitAction(context.Context, string, string, string) (int, []byte, error) {
	return http.StatusForbidden, []byte(`{"error":"read_only_mode"}`), nil
}

func TestSnapshotFreshStaleAndUnavailable(t *testing.T) {
	cfg := config.Config{
		HostLabel:       "jbhouse-test",
		ReadOnly:        true,
		CacheTTLSeconds: 10,
		Services: []config.ServiceConfig{
			{ID: "web", DisplayName: "Web", Stack: "s", Service: "web"},
		},
	}

	// 1. Unavailable before any successful read.
	b := &stubBroker{err: errors.New("socket refused")}
	svc := NewService(cfg, b, hostmetrics.NewReader())
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	s1 := svc.Snapshot(context.Background(), false)
	if s1.Freshness != "unavailable" || s1.DockerAvailable || s1.Summary.Unknown != 1 {
		t.Fatalf("expected unavailable snapshot, got %+v", s1)
	}

	// 2. Fresh when broker succeeds.
	b.err = nil
	b.snap = broker.SnapshotResponse{
		SampledAt: now.Format(time.RFC3339),
		ReadOnly:  true,
		Services: []broker.ServiceStatus{
			{ID: "web", DisplayName: "Web", Stack: "s", Service: "web", Present: true, State: "running", Health: "healthy"},
		},
	}
	s2 := svc.Snapshot(context.Background(), true)
	if s2.Freshness != "fresh" || !s2.DockerAvailable || s2.Summary.Running != 1 {
		t.Fatalf("expected fresh snapshot, got %+v", s2)
	}

	// 3. Stale with age when broker later fails after TTL.
	now = now.Add(25 * time.Second)
	b.err = errors.New("docker daemon stopped")
	s3 := svc.Snapshot(context.Background(), false)
	if s3.Freshness != "stale" || s3.DockerAvailable || s3.StaleAgeSeconds != 25 || s3.Summary.Running != 1 {
		t.Fatalf("expected stale snapshot preserving last known running state, got %+v", s3)
	}
}
