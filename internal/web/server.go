package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/bises/home-server-manager/internal/broker"
	"github.com/bises/home-server-manager/internal/hostmetrics"
	"github.com/bises/home-server-manager/internal/snapshot"
)

//go:embed templates/*.html static/*
var assetsFS embed.FS

// Options configures the web/API HTTP handler.
type Options struct {
	DisableUI bool
}

// Server serves the versioned API and optional built-in operations dashboard.
type Server struct {
	snap      *snapshot.Service
	disableUI bool
	pages     map[string]*template.Template
}

// NewServer constructs a Server.
func NewServer(snap *snapshot.Service, opts Options) (*Server, error) {
	s := &Server{
		snap:      snap,
		disableUI: opts.DisableUI,
	}
	if !opts.DisableUI {
		pages, err := parseTemplates()
		if err != nil {
			return nil, err
		}
		s.pages = pages
	}
	return s, nil
}

// Handler returns the HTTP handler with CSRF/cross-origin protection.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /api/v1/snapshot", s.handleAPISnapshot)
	mux.HandleFunc("GET /api/v1/services/{id}", s.handleAPIServiceDetail)
	mux.HandleFunc("GET /api/v1/activity", s.handleAPIActivity)
	mux.HandleFunc("POST /api/v1/services/{id}/actions/{verb}", s.handleAPIAction)

	if !s.disableUI {
		staticSub, _ := fs.Sub(assetsFS, "static")
		mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))
		mux.HandleFunc("GET /{$}", s.handleOverview)
		mux.HandleFunc("GET /services", s.handleServices)
		mux.HandleFunc("GET /services/{id}", s.handleServiceDetail)
		mux.HandleFunc("GET /activity", s.handleActivity)
	}

	return http.NewCrossOriginProtection().Handler(mux)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"ui_enabled": !s.disableUI,
	})
}

func (s *Server) handleAPISnapshot(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("refresh") == "1"
	snap := s.snap.Snapshot(r.Context(), force)
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) handleAPIServiceDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	detail, err := s.snap.ServiceDetail(r.Context(), id)
	if err != nil {
		code := http.StatusServiceUnavailable
		if strings.Contains(err.Error(), "HTTP 404") {
			code = http.StatusNotFound
		}
		writeError(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleAPIActivity(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":   "v1",
		"read_only": true,
		"events":    []any{},
		"message":   "Action audit records begin in Phase 2 when authenticated controls are enabled.",
	})
}

func (s *Server) handleAPIAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	verb := r.PathValue("verb")
	status, body, err := s.snap.SubmitAction(r.Context(), id, verb)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Broker unavailable: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

type stackGroup struct {
	Name     string
	Services []broker.ServiceStatus
}

type overviewData struct {
	Title     string
	ActiveNav string
	Snap      snapshot.Snapshot
	Attention []broker.ServiceStatus
}

type servicesData struct {
	Title       string
	ActiveNav   string
	Snap        snapshot.Snapshot
	Query       string
	Status      string
	StackGroups []stackGroup
	MatchCount  int
}

type detailData struct {
	Title     string
	ActiveNav string
	Detail    broker.ServiceDetail
	LoadError string
}

type activityData struct {
	Title     string
	ActiveNav string
	Snap      snapshot.Snapshot
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("refresh") == "1"
	snap := s.snap.Snapshot(r.Context(), force)

	attention := make([]broker.ServiceStatus, 0)
	for _, svc := range snap.Services {
		if svc.State == "unhealthy" || svc.State == "stopped" || svc.State == "unknown" {
			attention = append(attention, svc)
		}
	}

	s.render(w, "overview.html", overviewData{
		Title:     "Overview",
		ActiveNav: "overview",
		Snap:      snap,
		Attention: attention,
	})
}

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("refresh") == "1"
	snap := s.snap.Snapshot(r.Context(), force)
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	status := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("status")))
	if status == "" {
		status = "all"
	}

	qLower := strings.ToLower(query)
	groupOrder := make([]string, 0)
	groupMap := make(map[string][]broker.ServiceStatus)
	matches := 0

	for _, svc := range snap.Services {
		if !matchesStatus(svc.State, status) {
			continue
		}
		if qLower != "" &&
			!strings.Contains(strings.ToLower(svc.DisplayName), qLower) &&
			!strings.Contains(strings.ToLower(svc.Service), qLower) &&
			!strings.Contains(strings.ToLower(svc.Stack), qLower) &&
			!strings.Contains(strings.ToLower(svc.Image), qLower) {
			continue
		}
		matches++
		if _, exists := groupMap[svc.Stack]; !exists {
			groupOrder = append(groupOrder, svc.Stack)
		}
		groupMap[svc.Stack] = append(groupMap[svc.Stack], svc)
	}

	groups := make([]stackGroup, 0, len(groupOrder))
	for _, name := range groupOrder {
		groups = append(groups, stackGroup{Name: name, Services: groupMap[name]})
	}

	s.render(w, "services.html", servicesData{
		Title:       "Services",
		ActiveNav:   "services",
		Snap:        snap,
		Query:       query,
		Status:      status,
		StackGroups: groups,
		MatchCount:  matches,
	})
}

func (s *Server) handleServiceDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	detail, err := s.snap.ServiceDetail(r.Context(), id)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			http.NotFound(w, r)
			return
		}
		s.render(w, "service_detail.html", detailData{
			Title:     id,
			ActiveNav: "services",
			LoadError: err.Error(),
		})
		return
	}

	s.render(w, "service_detail.html", detailData{
		Title:     detail.DisplayName,
		ActiveNav: "services",
		Detail:    detail,
	})
}

func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	snap := s.snap.Snapshot(r.Context(), false)
	s.render(w, "activity.html", activityData{
		Title:     "Activity",
		ActiveNav: "activity",
		Snap:      snap,
	})
}

func matchesStatus(state, filter string) bool {
	switch filter {
	case "", "all":
		return true
	case "attention":
		return state == "unhealthy" || state == "stopped" || state == "unknown"
	default:
		return state == filter
	}
}

func (s *Server) render(w http.ResponseWriter, page string, data any) {
	tmpl, ok := s.pages[page]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		log.Printf("Render %s: %v", page, err)
	}
}

func parseTemplates() (map[string]*template.Template, error) {
	funcs := template.FuncMap{
		"formatBytes": hostmetrics.FormatBytes,
		"formatPct": func(v float64) string {
			return fmt.Sprintf("%.0f%%", v)
		},
		"formatLoad": func(v float64) string {
			return fmt.Sprintf("%.2f", v)
		},
		"formatUptime": func(sec uint64) string {
			if sec == 0 {
				return "n/a"
			}
			d := time.Duration(sec) * time.Second
			days := d / (24 * time.Hour)
			hours := (d % (24 * time.Hour)) / time.Hour
			mins := (d % time.Hour) / time.Minute
			if days > 0 {
				return fmt.Sprintf("%dd %dh", days, hours)
			}
			if hours > 0 {
				return fmt.Sprintf("%dh %dm", hours, mins)
			}
			return fmt.Sprintf("%dm", mins)
		},
		"stateIcon": func(state string) string {
			switch state {
			case "running":
				return "●"
			case "unhealthy":
				return "▲"
			case "stopped":
				return "■"
			case "starting":
				return "◐"
			default:
				return "?"
			}
		},
		"stateLabel": func(state string) string {
			switch state {
			case "running":
				return "Running"
			case "unhealthy":
				return "Unhealthy"
			case "stopped":
				return "Stopped"
			case "starting":
				return "Starting"
			default:
				return "Unknown"
			}
		},
		"formatPorts": func(ports []broker.PublishedPort) string {
			if len(ports) == 0 {
				return "—"
			}
			parts := make([]string, 0, len(ports))
			for _, p := range ports {
				parts = append(parts, fmt.Sprintf("%d→%d/%s", p.HostPort, p.ContainerPort, p.Protocol))
			}
			return strings.Join(parts, ", ")
		},
	}

	pageNames := []string{"overview.html", "services.html", "service_detail.html", "activity.html"}
	out := make(map[string]*template.Template, len(pageNames))
	for _, p := range pageNames {
		t, err := template.New("layout").Funcs(funcs).ParseFS(assetsFS, "templates/layout.html", "templates/"+p)
		if err != nil {
			return nil, fmt.Errorf("parse template %s: %w", p, err)
		}
		out[p] = t
	}
	return out, nil
}

func writeError(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, map[string]any{"status": "error", "message": message})
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("Write JSON response: %v", err)
	}
}
