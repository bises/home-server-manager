package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MountConfig defines a host filesystem mount to monitor.
type MountConfig struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Path  string `json:"path"`
}

// ServiceConfig defines an allowlisted Compose service managed by the broker.
type ServiceConfig struct {
	ID             string   `json:"id"`
	DisplayName    string   `json:"display_name"`
	Stack          string   `json:"stack"`
	Service        string   `json:"service"`
	Protected      bool     `json:"protected"`
	AllowedActions []string `json:"allowed_actions,omitempty"`
	URL            string   `json:"url,omitempty"`
}

// Config is the host-owned configuration shared by the broker and web service.
type Config struct {
	HostLabel       string          `json:"host_label"`
	ReadOnly        bool            `json:"read_only"`
	CacheTTLSeconds int             `json:"cache_ttl_seconds"`
	Mounts          []MountConfig   `json:"mounts"`
	Services        []ServiceConfig `json:"services"`
}

// Load reads and validates a JSON configuration file from path.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %q: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks required fields and uniqueness constraints.
func (c *Config) Validate() error {
	if c.CacheTTLSeconds <= 0 {
		c.CacheTTLSeconds = 10
	}
	if strings.TrimSpace(c.HostLabel) == "" {
		host, err := os.Hostname()
		if err == nil && host != "" {
			c.HostLabel = host
		} else {
			c.HostLabel = "home-server"
		}
	}

	mountIDs := make(map[string]bool, len(c.Mounts))
	for i := range c.Mounts {
		m := &c.Mounts[i]
		m.ID = strings.TrimSpace(m.ID)
		m.Label = strings.TrimSpace(m.Label)
		m.Path = filepath.Clean(strings.TrimSpace(m.Path))
		if m.ID == "" || m.Path == "" || m.Path == "." {
			return fmt.Errorf("mount[%d]: id and path are required", i)
		}
		if m.Label == "" {
			m.Label = m.ID
		}
		if mountIDs[m.ID] {
			return fmt.Errorf("duplicate mount id %q", m.ID)
		}
		mountIDs[m.ID] = true
	}

	if len(c.Services) == 0 {
		return errors.New("config must define at least one allowlisted service")
	}

	serviceIDs := make(map[string]bool, len(c.Services))
	targets := make(map[string]bool, len(c.Services))
	for i := range c.Services {
		s := &c.Services[i]
		s.ID = strings.TrimSpace(s.ID)
		s.Stack = strings.TrimSpace(s.Stack)
		s.Service = strings.TrimSpace(s.Service)
		s.DisplayName = strings.TrimSpace(s.DisplayName)
		if s.ID == "" || s.Stack == "" || s.Service == "" {
			return fmt.Errorf("service[%d]: id, stack, and service are required", i)
		}
		if s.DisplayName == "" {
			s.DisplayName = s.Service
		}
		if serviceIDs[s.ID] {
			return fmt.Errorf("duplicate service id %q", s.ID)
		}
		serviceIDs[s.ID] = true
		key := s.Stack + "/" + s.Service
		if targets[key] {
			return fmt.Errorf("duplicate stack/service target %q", key)
		}
		targets[key] = true
	}

	return nil
}

// FindService returns the allowlisted service by its stable ID.
func (c Config) FindService(id string) (ServiceConfig, bool) {
	for _, s := range c.Services {
		if s.ID == id {
			return s, true
		}
	}
	return ServiceConfig{}, false
}
