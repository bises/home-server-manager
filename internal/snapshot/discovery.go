package snapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/bises/home-server-manager/internal/broker"
)

func (c *UnixBrokerClient) DiscoverContainers(ctx context.Context) ([]broker.DiscoveryEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://broker/v1/discovery", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readBrokerError(resp)
	}
	var entries []broker.DiscoveryEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func (c *UnixBrokerClient) ChangeWatchlist(ctx context.Context, change broker.WatchChange) error {
	payload, err := json.Marshal(change)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://broker/v1/discovery", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return readBrokerError(resp)
	}
	return nil
}
