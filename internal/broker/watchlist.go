package broker

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/bises/home-server-manager/internal/config"
	"github.com/bises/home-server-manager/internal/dockerapi"
)

const maxWatchChanges = 256 << 10

var containerNamePattern = regexp.MustCompile("^[a-zA-Z0-9][a-zA-Z0-9_.-]*$")

type DiscoveryEntry struct {
	Name     string
	Stack    string
	Image    string
	State    string
	Present  bool
	Managed  bool
	Watching bool
}

type WatchChange struct {
	Action string   `json:"action"`
	Names  []string `json:"names"`
}

type watchlist struct {
	mu      sync.Mutex
	path    string
	entries map[string]string
}

// LoadWatchlist opens the optional monitor-only catalog before the broker starts serving.
func (b *Broker) LoadWatchlist(path string) error {
	list := &watchlist{path: path, entries: make(map[string]string)}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &list.entries); err != nil || list.entries == nil {
			return fmt.Errorf("invalid watchlist at %q", path)
		}
		for name := range list.entries {
			if !containerNamePattern.MatchString(name) {
				return fmt.Errorf("invalid container name in watchlist at %q", path)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read watchlist: %w", err)
	}
	b.watch = list
	return nil
}

func (w *watchlist) saveLocked(entries map[string]string) error {
	file, err := os.CreateTemp(filepath.Dir(w.path), ".watchlist-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := json.NewEncoder(file).Encode(entries); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), w.path); err != nil {
		return err
	}
	w.entries = entries
	return nil
}

func containerByName(containers []dockerapi.ContainerSummary) map[string]dockerapi.ContainerSummary {
	out := make(map[string]dockerapi.ContainerSummary, len(containers))
	for _, c := range containers {
		for _, alias := range c.Names {
			name := strings.TrimPrefix(alias, "/")
			if containerNamePattern.MatchString(name) {
				out[name] = c
				break
			}
		}
	}
	return out
}

func (b *Broker) managedIDs(containers []dockerapi.ContainerSummary) map[string]bool {
	targets := indexByComposeTarget(containers)
	ids := make(map[string]bool)
	for _, service := range b.cfg.Services {
		for _, c := range targets[service.Stack+"/"+service.Service] {
			ids[c.ID] = true
		}
	}
	return ids
}

func containerStack(c dockerapi.ContainerSummary) string {
	if stack := c.Labels["com.docker.compose.project"]; stack != "" {
		return stack
	}
	return "Standalone containers"
}

func watchedService(name, stack string) config.ServiceConfig {
	return config.ServiceConfig{
		ID: "container-" + name, DisplayName: name, Service: name, Stack: stack, Protected: true,
	}
}

func (b *Broker) watchedStatuses(containers []dockerapi.ContainerSummary) []ServiceStatus {
	if b.watch == nil {
		return nil
	}
	byName := containerByName(containers)
	managed := b.managedIDs(containers)
	b.watch.mu.Lock()
	defer b.watch.mu.Unlock()
	results := make([]ServiceStatus, 0, len(b.watch.entries))
	for name, stack := range b.watch.entries {
		c, found := byName[name]
		service := watchedService(name, stack)
		if _, exists := b.cfg.FindService(service.ID); exists || managed[c.ID] {
			continue
		}
		results = append(results, normalizeService(service, c, found, true))
	}
	sort.Slice(results, func(i, j int) bool { return results[i].DisplayName < results[j].DisplayName })
	return results
}

func (b *Broker) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	containers, err := b.docker.ListContainers(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "docker_unavailable", "Docker inventory is unavailable")
		return
	}
	byName := containerByName(containers)
	managed := b.managedIDs(containers)
	b.watch.mu.Lock()
	defer b.watch.mu.Unlock()
	out := make([]DiscoveryEntry, 0, len(byName))
	for name, c := range byName {
		stack, watching := b.watch.entries[name]
		if !watching {
			stack = containerStack(c)
		}
		_, duplicateID := b.cfg.FindService(watchedService(name, stack).ID)
		out = append(out, DiscoveryEntry{
			Name: name, Stack: stack, Image: c.Image,
			State:   effectiveState(strings.ToLower(c.State), healthFromStatus(c.Status)),
			Present: true, Managed: managed[c.ID] || duplicateID, Watching: watching,
		})
	}
	for name, stack := range b.watch.entries {
		if _, found := byName[name]; !found {
			out = append(out, DiscoveryEntry{Name: name, Stack: stack, State: "unknown", Watching: true})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}

func (b *Broker) handleWatchChange(w http.ResponseWriter, r *http.Request) {
	var change WatchChange
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWatchChanges)).Decode(&change); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_selection", "Invalid container selection")
		return
	}
	if change.Action != "add" && change.Action != "add-all" && change.Action != "remove" {
		writeJSONError(w, http.StatusBadRequest, "invalid_action", "Choose which containers to add or remove")
		return
	}

	var available map[string]string
	if change.Action != "remove" {
		containers, err := b.docker.ListContainers(r.Context())
		if err != nil {
			writeJSONError(w, http.StatusServiceUnavailable, "docker_unavailable", "Docker inventory is unavailable")
			return
		}
		managed := b.managedIDs(containers)
		available = make(map[string]string)
		for name, c := range containerByName(containers) {
			stack := containerStack(c)
			if _, duplicateID := b.cfg.FindService(watchedService(name, stack).ID); !duplicateID && !managed[c.ID] {
				available[name] = stack
			}
		}
		if change.Action == "add" && len(change.Names) == 0 {
			writeJSONError(w, http.StatusBadRequest, "empty_selection", "Select at least one container")
			return
		}
	}

	b.watch.mu.Lock()
	defer b.watch.mu.Unlock()
	next := make(map[string]string, len(b.watch.entries))
	for name, stack := range b.watch.entries {
		next[name] = stack
	}
	switch change.Action {
	case "add":
		for _, name := range change.Names {
			stack, exists := available[name]
			if !exists {
				if _, watching := next[name]; watching {
					continue
				}
				writeJSONError(w, http.StatusConflict, "container_unavailable", "A selected container has changed. Refresh the list and try again.")
				return
			}
			next[name] = stack
		}
	case "add-all":
		for name, stack := range available {
			next[name] = stack
		}
	case "remove":
		for _, name := range change.Names {
			delete(next, name)
		}
	}
	if err := b.watch.saveLocked(next); err != nil {
		log.Printf("Save watchlist: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "watchlist_save_failed", "Could not save the watchlist")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
