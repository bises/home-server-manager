package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"sort"
	"time"
)

func main() {
	projects, err := loadProjects(os.Getenv("DOCKER_COMPOSE_PATHS"), os.Getenv("DOCKER_COMPOSE_PATH"))
	if err != nil {
		log.Fatal(err)
	}

	manager := &manager{projects: projects, run: runCompose}
	addr := envOr("LISTEN_ADDR", "127.0.0.1:5000")
	server := http.Server{
		Addr:              addr,
		Handler:           manager.handler(envOr("WEB_ROOT", "frontend/build")),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      8 * time.Minute,
		IdleTimeout:       time.Minute,
	}
	log.Printf("Home Server Manager listening on %s", addr)
	log.Fatal(server.ListenAndServe())
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

type manager struct {
	projects projectSet
	run      composeRunner
}

func (m *manager) handler(webRoot string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/hello", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "success", "message": "Hello, World!"})
	})
	mux.HandleFunc("GET /api/compose-files", m.listServices)
	mux.HandleFunc("GET /api/services", m.listServices)
	mux.HandleFunc("GET /api/containers/status", m.listContainers)
	mux.HandleFunc("POST /api/containers/{action}/{service}", m.containerAction)
	mux.Handle("GET /", http.FileServer(http.Dir(webRoot)))
	return http.NewCrossOriginProtection().Handler(mux)
}

func (m *manager) listServices(w http.ResponseWriter, r *http.Request) {
	type composeFile struct {
		Path        string `json:"path"`
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		LegacyName  string `json:"displayName"`
	}
	type serviceInfo struct {
		Service     string `json:"service"`
		ComposeFile string `json:"compose_file"`
		ProjectName string `json:"project_name"`
		DisplayName string `json:"display_name"`
	}

	files := make([]composeFile, 0, len(m.projects.files))
	services := make([]serviceInfo, 0)
	for _, p := range m.projects.files {
		cfg, err := p.config(r.Context(), m.run)
		if err != nil {
			writeComposeError(w, p, err)
			return
		}
		files = append(files, composeFile{p.path, cfg.Name, p.display, p.display})
		names := make([]string, 0, len(cfg.Services))
		for name := range cfg.Services {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			services = append(services, serviceInfo{name, p.path, cfg.Name, p.display})
		}
	}

	total := len(services)
	if r.URL.Path == "/api/compose-files" {
		total = len(files)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "success", "compose_files": files, "services": services, "total": total,
	})
}

func (m *manager) listContainers(w http.ResponseWriter, r *http.Request) {
	containers := make([]containerStatus, 0)
	for _, p := range m.projects.files {
		status, err := p.status(r.Context(), m.run)
		if err != nil {
			writeComposeError(w, p, err)
			return
		}
		containers = append(containers, status...)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "success", "containers": containers, "total": len(containers),
	})
}

func (m *manager) containerAction(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	args, known := actionArgs[action]
	if !known && action != "pull" {
		writeError(w, http.StatusNotFound, "Unknown container action")
		return
	}

	p := m.projects.get(r.URL.Query().Get("compose_file"))
	if p == nil {
		writeError(w, http.StatusBadRequest, "Select a configured Compose file")
		return
	}
	if !p.actions.TryLock() {
		writeError(w, http.StatusConflict, "Another action is already running for this Compose file")
		return
	}
	defer p.actions.Unlock()

	cfg, err := p.config(r.Context(), m.run)
	if err != nil {
		writeComposeError(w, p, err)
		return
	}
	service := r.PathValue("service")
	if _, exists := cfg.Services[service]; !exists {
		writeError(w, http.StatusNotFound, "Service is not defined in this Compose file")
		return
	}

	message := "Action completed for " + service
	if action == "pull" {
		message, err = p.pull(r.Context(), m.run, service)
	} else {
		_, err = p.command(r.Context(), m.run, actionTimeout, serviceArgs(args, service)...)
	}
	if err != nil {
		writeComposeError(w, p, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "success", "service": service, "message": message,
	})
}

func writeComposeError(w http.ResponseWriter, p *project, err error) {
	log.Printf("Compose operation for %s: %v", p.path, err)
	if errors.Is(err, errComposeTimeout) {
		writeError(w, http.StatusGatewayTimeout, "Docker Compose timed out for "+p.display)
		return
	}
	var step *composeError
	if errors.As(err, &step) {
		writeError(w, http.StatusBadGateway, step.operation+" failed for "+p.display)
		return
	}
	writeError(w, http.StatusBadGateway, "Docker Compose returned unexpected data for "+p.display)
}

func writeError(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, map[string]any{"status": "error", "message": message})
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("Write API response: %v", err)
	}
}
