package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bises/home-server-manager/internal/auth"
	"github.com/bises/home-server-manager/internal/broker"
	"github.com/bises/home-server-manager/internal/hostmetrics"
	"github.com/bises/home-server-manager/internal/snapshot"
)

//go:embed templates/*.html static/*
var assetsFS embed.FS

// Options configures the web/API HTTP handler.
type Options struct {
	DisableUI     bool
	Auth          *auth.Manager
	SecureCookies bool
	Discovery     DiscoveryClient
	LocalSetup    bool
}

// Server serves the versioned API and optional built-in operations dashboard.
type Server struct {
	snap          *snapshot.Service
	disableUI     bool
	auth          *auth.Manager
	secureCookies bool
	discovery     DiscoveryClient
	localSetup    bool
	pages         map[string]*template.Template
}

// NewServer constructs a Server.
func NewServer(snap *snapshot.Service, opts Options) (*Server, error) {
	s := &Server{
		snap:          snap,
		disableUI:     opts.DisableUI,
		auth:          opts.Auth,
		secureCookies: opts.SecureCookies,
		discovery:     opts.Discovery,
		localSetup:    opts.LocalSetup && !opts.Auth.Enabled(),
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
	mux.HandleFunc("GET /api/v1/operations/{id}", s.handleAPIOperation)
	mux.HandleFunc("POST /api/v1/services/{id}/actions/{verb}", s.handleAPIAction)

	if !s.disableUI {
		staticSub, _ := fs.Sub(assetsFS, "static")
		mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))
		mux.HandleFunc("GET /login", s.handleLoginGet)
		mux.HandleFunc("POST /login", s.handleLoginPost)
		mux.HandleFunc("POST /logout", s.handleLogout)
		mux.HandleFunc("GET /{$}", s.handleOverview)
		mux.HandleFunc("GET /services", s.handleServices)
		mux.HandleFunc("GET /services/{id}", s.handleServiceDetail)
		mux.HandleFunc("POST /services/{id}/action", s.handleBrowserAction)
		mux.HandleFunc("GET /activity", s.handleActivity)
		if s.discovery != nil && (s.localSetup || s.authEnabled()) {
			mux.HandleFunc("GET /setup", s.handleSetup)
			mux.HandleFunc("POST /setup", s.handleSetupPost)
		}
	}

	handler := http.NewCrossOriginProtection().Handler(mux)
	if !s.localSetup {
		return handler
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackHost(r.Host) {
			http.Error(w, "Open this read-only dashboard on localhost or through an SSH tunnel", http.StatusForbidden)
			return
		}
		handler.ServeHTTP(w, r)
	})
}

func (s *Server) authEnabled() bool {
	return s.auth != nil && s.auth.Enabled()
}

func (s *Server) authenticateAPI(r *http.Request) (auth.Principal, bool) {
	if !s.authEnabled() {
		return auth.Principal{Kind: "browser", Actor: "local-viewer", Role: "viewer", Read: true}, true
	}
	if authHdr := r.Header.Get("Authorization"); authHdr != "" {
		return s.auth.VerifyBearer(authHdr)
	}
	if c, err := r.Cookie(auth.SessionCookieName); err == nil {
		if sess, ok := s.auth.VerifySessionCookie(c.Value); ok {
			return auth.Principal{
				Kind:      "browser",
				Actor:     sess.Username,
				Role:      sess.Role,
				CSRFToken: sess.CSRFToken,
				Read:      true,
			}, true
		}
	}
	return auth.Principal{}, false
}

func (s *Server) requireBrowserSession(w http.ResponseWriter, r *http.Request) (auth.Session, bool) {
	if !s.authEnabled() {
		return auth.Session{Username: "local", Role: "viewer"}, true
	}
	c, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return auth.Session{}, false
	}
	sess, ok := s.auth.VerifySessionCookie(c.Value)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return auth.Session{}, false
	}
	return sess, true
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":       "ok",
		"ui_enabled":   !s.disableUI,
		"auth_enabled": s.authEnabled(),
	})
}

func (s *Server) handleAPISnapshot(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticateAPI(r)
	if !ok {
		writeErrorCode(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if p.Kind == "token" && !p.Read {
		writeErrorCode(w, http.StatusForbidden, "forbidden", "Token lacks read scope")
		return
	}
	force := r.URL.Query().Get("refresh") == "1"
	snap := s.snap.Snapshot(r.Context(), force)
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) handleAPIServiceDetail(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticateAPI(r)
	if !ok {
		writeErrorCode(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if p.Kind == "token" && !p.Read {
		writeErrorCode(w, http.StatusForbidden, "forbidden", "Token lacks read scope")
		return
	}
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

func (s *Server) handleAPIActivity(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticateAPI(r)
	if !ok {
		writeErrorCode(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if p.Kind == "token" && !p.Read {
		writeErrorCode(w, http.StatusForbidden, "forbidden", "Token lacks read scope")
		return
	}
	events, err := s.snap.Activity(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": "v1",
		"events":  events,
	})
}

func (s *Server) handleAPIOperation(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticateAPI(r)
	if !ok {
		writeErrorCode(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if p.Kind == "token" && !p.Read && len(p.Actions) == 0 {
		writeErrorCode(w, http.StatusForbidden, "forbidden", "Token lacks read or action scope")
		return
	}
	opID := r.PathValue("id")
	op, err := s.snap.Operation(r.Context(), opID)
	if err != nil {
		code := http.StatusServiceUnavailable
		if strings.Contains(err.Error(), "HTTP 404") {
			code = http.StatusNotFound
		}
		writeError(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, op)
}

func (s *Server) handleAPIAction(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled() {
		writeErrorCode(w, http.StatusUnauthorized, "auth_required", "Authentication must be configured to run actions")
		return
	}
	p, ok := s.authenticateAPI(r)
	if !ok {
		writeErrorCode(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	id := r.PathValue("id")
	verb := strings.ToLower(strings.TrimSpace(r.PathValue("verb")))

	if s.authEnabled() && p.Kind == "browser" {
		csrf := r.Header.Get("X-CSRF-Token")
		if !auth.VerifyCSRF(auth.Session{CSRFToken: p.CSRFToken}, csrf) {
			writeErrorCode(w, http.StatusForbidden, "invalid_csrf", "Missing or invalid CSRF token")
			return
		}
	}
	if !p.CanAct(id, verb) {
		writeErrorCode(w, http.StatusForbidden, "forbidden", "Caller is not authorized for this service action")
		return
	}

	status, body, err := s.snap.SubmitAction(r.Context(), id, verb, p.Actor)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Broker unavailable: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

type navUser struct {
	Enabled   bool
	Username  string
	Role      string
	CSRFToken string
	CanSetup  bool
}

type stackGroup struct {
	Name     string
	Services []broker.ServiceStatus
}

type loginData struct {
	Title     string
	ActiveNav string
	User      navUser
	Error     string
}

type overviewData struct {
	Title     string
	ActiveNav string
	User      navUser
	Snap      snapshot.Snapshot
	Attention []broker.ServiceStatus
}

type servicesData struct {
	Title       string
	ActiveNav   string
	User        navUser
	Snap        snapshot.Snapshot
	Query       string
	Status      string
	StackGroups []stackGroup
	MatchCount  int
}

type detailData struct {
	Title       string
	ActiveNav   string
	User        navUser
	Detail      broker.ServiceDetail
	Operation   *broker.Operation
	ActionError string
	LoadError   string
}

type activityData struct {
	Title     string
	ActiveNav string
	User      navUser
	Snap      snapshot.Snapshot
	Events    []broker.AuditRecord
	LoadError string
}

func (s *Server) toNavUser(sess auth.Session) navUser {
	return navUser{
		Enabled:   s.authEnabled(),
		Username:  sess.Username,
		Role:      sess.Role,
		CSRFToken: sess.CSRFToken,
		CanSetup:  s.discovery != nil && (s.localSetup || (s.authEnabled() && sess.Role == "operator")),
	}
}

func (s *Server) handleLoginGet(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, "login.html", loginData{Title: "Sign in"})
}

func (s *Server) handleLoginPost(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderStatus(w, http.StatusBadRequest, "login.html", loginData{Title: "Sign in", Error: "Invalid form submission"})
		return
	}
	// RemoteAddr is the immediate peer. Forwarded headers are not trusted here.
	rateKey, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		rateKey = r.RemoteAddr
	}
	if !s.auth.BeginLogin(rateKey) {
		w.Header().Set("Retry-After", "60")
		s.renderStatus(w, http.StatusTooManyRequests, "login.html", loginData{Title: "Sign in", Error: "Too many failed login attempts. Try again in a minute."})
		return
	}
	_, cookieVal, ok := s.auth.AuthenticateUser(r.FormValue("username"), r.FormValue("password"))
	if !ok {
		s.renderStatus(w, http.StatusUnauthorized, "login.html", loginData{Title: "Sign in", Error: "Invalid username or password"})
		return
	}
	s.auth.ResetLoginAttempts(rateKey)
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    cookieVal,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secureCookies,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if s.authEnabled() {
		sess, ok := s.requireBrowserSession(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil || !auth.VerifyCSRF(sess, r.FormValue("csrf_token")) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		if c, err := r.Cookie(auth.SessionCookieName); err == nil {
			s.auth.DeleteSession(c.Value)
		}
		http.SetCookie(w, &http.Cookie{
			Name:     auth.SessionCookieName,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   s.secureCookies,
			SameSite: http.SameSiteLaxMode,
		})
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.requireBrowserSession(w, r)
	if !ok {
		return
	}
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
		User:      s.toNavUser(sess),
		Snap:      snap,
		Attention: attention,
	})
}

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.requireBrowserSession(w, r)
	if !ok {
		return
	}
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
		User:        s.toNavUser(sess),
		Snap:        snap,
		Query:       query,
		Status:      status,
		StackGroups: groups,
		MatchCount:  matches,
	})
}

func (s *Server) handleServiceDetail(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.requireBrowserSession(w, r)
	if !ok {
		return
	}
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
			User:      s.toNavUser(sess),
			LoadError: err.Error(),
		})
		return
	}

	var opPtr *broker.Operation
	if opID := strings.TrimSpace(r.URL.Query().Get("op")); opID != "" {
		if op, oErr := s.snap.Operation(r.Context(), opID); oErr == nil {
			opPtr = &op
		}
	}

	s.render(w, "service_detail.html", detailData{
		Title:       detail.DisplayName,
		ActiveNav:   "services",
		User:        s.toNavUser(sess),
		Detail:      detail,
		Operation:   opPtr,
		ActionError: r.URL.Query().Get("err"),
	})
}

func (s *Server) handleBrowserAction(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled() {
		http.Error(w, "authentication must be configured to run actions", http.StatusForbidden)
		return
	}
	sess, ok := s.requireBrowserSession(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if s.authEnabled() && !auth.VerifyCSRF(sess, r.FormValue("csrf_token")) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	verb := strings.ToLower(strings.TrimSpace(r.FormValue("verb")))
	if sess.Role != "operator" {
		http.Redirect(w, r, "/services/"+url.PathEscape(id)+"?err="+url.QueryEscape("Viewer role cannot run service actions"), http.StatusSeeOther)
		return
	}

	status, body, err := s.snap.SubmitAction(r.Context(), id, verb, sess.Username)
	if err != nil {
		http.Redirect(w, r, "/services/"+url.PathEscape(id)+"?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	if status == http.StatusAccepted || status == http.StatusOK {
		var op broker.Operation
		if json.Unmarshal(body, &op) == nil && op.ID != "" {
			http.Redirect(w, r, "/services/"+url.PathEscape(id)+"?op="+url.QueryEscape(op.ID), http.StatusSeeOther)
			return
		}
	}
	var errPayload struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &errPayload)
	msg := errPayload.Message
	if msg == "" {
		msg = fmt.Sprintf("Action rejected (HTTP %d)", status)
	}
	http.Redirect(w, r, "/services/"+url.PathEscape(id)+"?err="+url.QueryEscape(msg), http.StatusSeeOther)
}

func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.requireBrowserSession(w, r)
	if !ok {
		return
	}
	snap := s.snap.Snapshot(r.Context(), false)
	events, err := s.snap.Activity(r.Context())
	loadErr := ""
	if err != nil {
		loadErr = err.Error()
	}
	s.render(w, "activity.html", activityData{
		Title:     "Activity",
		ActiveNav: "activity",
		User:      s.toNavUser(sess),
		Snap:      snap,
		Events:    events,
		LoadError: loadErr,
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
	s.renderStatus(w, http.StatusOK, page, data)
}

func (s *Server) renderStatus(w http.ResponseWriter, code int, page string, data any) {
	tmpl, ok := s.pages[page]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
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
			case "running", "succeeded":
				return "●"
			case "unhealthy", "failed":
				return "▲"
			case "stopped":
				return "■"
			case "starting", "accepted":
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
			case "succeeded":
				return "Succeeded"
			case "failed":
				return "Failed"
			case "accepted":
				return "Accepted"
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

	pageNames := []string{"login.html", "overview.html", "services.html", "service_detail.html", "activity.html", "setup.html"}
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

func writeErrorCode(w http.ResponseWriter, code int, errCode, message string) {
	writeJSON(w, code, map[string]any{"error": errCode, "message": message})
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("Write JSON response: %v", err)
	}
}
