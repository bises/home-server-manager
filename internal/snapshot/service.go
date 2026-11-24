package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bises/home-server-manager/internal/broker"
	"github.com/bises/home-server-manager/internal/config"
	"github.com/bises/home-server-manager/internal/hostmetrics"
)

// BrokerClient talks to the local broker over its Unix domain socket.
type BrokerClient interface {
	FetchSnapshot(ctx context.Context) (broker.SnapshotResponse, error)
	FetchServiceDetail(ctx context.Context, id string) (broker.ServiceDetail, error)
	SubmitAction(ctx context.Context, id, verb string) (int, []byte, error)
}

// UnixBrokerClient implements BrokerClient over a Unix domain socket.
type UnixBrokerClient struct {
	httpClient *http.Client
}

// NewUnixBrokerClient creates a client that dials only the given Unix socket path.
func NewUnixBrokerClient(socketPath string) *UnixBrokerClient {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socketPath)
		},
	}
	return &UnixBrokerClient{
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   15 * time.Second,
		},
	}
}

func (c *UnixBrokerClient) FetchSnapshot(ctx context.Context) (broker.SnapshotResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://broker/v1/snapshot", nil)
	if err != nil {
		return broker.SnapshotResponse{}, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return broker.SnapshotResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return broker.SnapshotResponse{}, readBrokerError(resp)
	}
	var out broker.SnapshotResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return broker.SnapshotResponse{}, fmt.Errorf("decode broker snapshot: %w", err)
	}
	return out, nil
}

func (c *UnixBrokerClient) FetchServiceDetail(ctx context.Context, id string) (broker.ServiceDetail, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://broker/v1/services/"+url.PathEscape(id), nil)
	if err != nil {
		return broker.ServiceDetail{}, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return broker.ServiceDetail{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return broker.ServiceDetail{}, readBrokerError(resp)
	}
	var out broker.ServiceDetail
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return broker.ServiceDetail{}, fmt.Errorf("decode service detail: %w", err)
	}
	return out, nil
}

func (c *UnixBrokerClient) SubmitAction(ctx context.Context, id, verb string) (int, []byte, error) {
	target := fmt.Sprintf("http://broker/v1/services/%s/actions/%s", url.PathEscape(id), url.PathEscape(verb))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return 0, nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, body, err
}

func readBrokerError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	var payload struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &payload) == nil && payload.Message != "" {
		return fmt.Errorf("broker HTTP %d: %s", resp.StatusCode, payload.Message)
	}
	return fmt.Errorf("broker HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

// SummaryCounts summarizes service states for the overview header and Home Assistant.
type SummaryCounts struct {
	Total     int `json:"total"`
	Running   int `json:"running"`
	Stopped   int `json:"stopped"`
	Unhealthy int `json:"unhealthy"`
	Starting  int `json:"starting"`
	Unknown   int `json:"unknown"`
}

// Snapshot is the public GET /api/v1/snapshot payload.
type Snapshot struct {
	Version         string                  `json:"version"`
	ReadOnly        bool                    `json:"read_only"`
	SampledAt       string                  `json:"sampled_at"`
	LastSuccessAt   string                  `json:"last_success_at,omitempty"`
	Freshness       string                  `json:"freshness"`
	StaleAgeSeconds int64                   `json:"stale_age_seconds"`
	DockerAvailable bool                    `json:"docker_available"`
	DockerError     string                  `json:"docker_error,omitempty"`
	Host            hostmetrics.HostMetrics `json:"host"`
	Summary         SummaryCounts           `json:"summary"`
	Services        []broker.ServiceStatus  `json:"services"`
}

// Service caches the latest broker snapshot and combines it with host metrics.
type Service struct {
	cfg     config.Config
	broker  BrokerClient
	metrics *hostmetrics.Reader
	now     func() time.Time

	mu            sync.Mutex
	hasSuccess    bool
	lastSuccessAt time.Time
	lastAttemptAt time.Time
	lastServices  []broker.ServiceStatus
}

// NewService creates a snapshot Service.
func NewService(cfg config.Config, brokerClient BrokerClient, metricsReader *hostmetrics.Reader) *Service {
	return &Service{
		cfg:     cfg,
		broker:  brokerClient,
		metrics: metricsReader,
		now:     func() time.Time { return time.Now().UTC() },
	}
}

// Snapshot returns the current host metrics and service states (fresh, stale, or unavailable).
func (s *Service) Snapshot(ctx context.Context, forceRefresh bool) Snapshot {
	now := s.now()
	ttl := time.Duration(s.cfg.CacheTTLSeconds) * time.Second

	s.mu.Lock()
	canUseCache := !forceRefresh && s.hasSuccess && now.Sub(s.lastAttemptAt) < ttl && now.Sub(s.lastSuccessAt) < ttl
	cachedServices := cloneServices(s.lastServices)
	cachedSuccessAt := s.lastSuccessAt
	s.mu.Unlock()

	host := s.metrics.Collect(s.cfg.HostLabel, s.cfg.Mounts)

	if canUseCache {
		return Snapshot{
			Version:         "v1",
			ReadOnly:        s.cfg.ReadOnly,
			SampledAt:       now.Format(time.RFC3339),
			LastSuccessAt:   cachedSuccessAt.Format(time.RFC3339),
			Freshness:       "fresh",
			StaleAgeSeconds: 0,
			DockerAvailable: true,
			Host:            host,
			Summary:         summarize(cachedServices),
			Services:        cachedServices,
		}
	}

	bSnap, err := s.broker.FetchSnapshot(ctx)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastAttemptAt = now

	if err == nil {
		s.hasSuccess = true
		s.lastSuccessAt = now
		s.lastServices = cloneServices(bSnap.Services)
		return Snapshot{
			Version:         "v1",
			ReadOnly:        s.cfg.ReadOnly,
			SampledAt:       now.Format(time.RFC3339),
			LastSuccessAt:   now.Format(time.RFC3339),
			Freshness:       "fresh",
			StaleAgeSeconds: 0,
			DockerAvailable: true,
			Host:            host,
			Summary:         summarize(bSnap.Services),
			Services:        bSnap.Services,
		}
	}

	if s.hasSuccess {
		age := max(int64(now.Sub(s.lastSuccessAt).Seconds()), 0)
		staleServices := cloneServices(s.lastServices)
		return Snapshot{
			Version:         "v1",
			ReadOnly:        s.cfg.ReadOnly,
			SampledAt:       now.Format(time.RFC3339),
			LastSuccessAt:   s.lastSuccessAt.Format(time.RFC3339),
			Freshness:       "stale",
			StaleAgeSeconds: age,
			DockerAvailable: false,
			DockerError:     err.Error(),
			Host:            host,
			Summary:         summarize(staleServices),
			Services:        staleServices,
		}
	}

	unknown := make([]broker.ServiceStatus, 0, len(s.cfg.Services))
	for _, sc := range s.cfg.Services {
		unknown = append(unknown, broker.ServiceStatus{
			ID:             sc.ID,
			DisplayName:    sc.DisplayName,
			Stack:          sc.Stack,
			Service:        sc.Service,
			Protected:      sc.Protected,
			URL:            sc.URL,
			Present:        false,
			State:          "unknown",
			Health:         "none",
			StatusText:     "Docker unavailable",
			Ports:          []broker.PublishedPort{},
			AllowedActions: []string{},
		})
	}
	return Snapshot{
		Version:         "v1",
		ReadOnly:        s.cfg.ReadOnly,
		SampledAt:       now.Format(time.RFC3339),
		Freshness:       "unavailable",
		StaleAgeSeconds: 0,
		DockerAvailable: false,
		DockerError:     err.Error(),
		Host:            host,
		Summary:         summarize(unknown),
		Services:        unknown,
	}
}

// ServiceDetail returns detailed state for a single allowlisted service.
func (s *Service) ServiceDetail(ctx context.Context, id string) (broker.ServiceDetail, error) {
	return s.broker.FetchServiceDetail(ctx, id)
}

// SubmitAction forwards an action request to the broker.
func (s *Service) SubmitAction(ctx context.Context, id, verb string) (int, []byte, error) {
	return s.broker.SubmitAction(ctx, id, verb)
}

func summarize(services []broker.ServiceStatus) SummaryCounts {
	var c SummaryCounts
	c.Total = len(services)
	for _, svc := range services {
		switch svc.State {
		case "running":
			c.Running++
		case "stopped":
			c.Stopped++
		case "unhealthy":
			c.Unhealthy++
		case "starting":
			c.Starting++
		default:
			c.Unknown++
		}
	}
	return c
}

func cloneServices(in []broker.ServiceStatus) []broker.ServiceStatus {
	if len(in) == 0 {
		return nil
	}
	out := make([]broker.ServiceStatus, len(in))
	copy(out, in)
	return out
}
