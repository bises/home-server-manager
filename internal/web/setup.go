package web

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/bises/home-server-manager/internal/auth"
	"github.com/bises/home-server-manager/internal/broker"
)

const maxSetupFormBytes = 256 << 10

type DiscoveryClient interface {
	DiscoverContainers(context.Context) ([]broker.DiscoveryEntry, error)
	ChangeWatchlist(context.Context, broker.WatchChange) error
}

type setupData struct {
	Title     string
	ActiveNav string
	User      navUser
	Entries   []broker.DiscoveryEntry
	NewCount  int
	Notice    string
	Error     string
}

func isLoopbackHost(host string) bool {
	if hostname, _, err := net.SplitHostPort(host); err == nil {
		host = hostname
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	return net.ParseIP(strings.Trim(host, "[]")).IsLoopback()
}

func sameOriginSetupPost(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		return origin == "http://"+r.Host || origin == "https://"+r.Host
	}
	return r.Header.Get("Sec-Fetch-Site") == "same-origin"
}

func (s *Server) requireSetupAccess(w http.ResponseWriter, r *http.Request) (auth.Session, bool) {
	sess, ok := s.requireBrowserSession(w, r)
	if !ok {
		return auth.Session{}, false
	}
	if !s.localSetup && (!s.authEnabled() || sess.Role != "operator") {
		http.Error(w, "Operator access required", http.StatusForbidden)
		return auth.Session{}, false
	}
	return sess, true
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.requireSetupAccess(w, r)
	if !ok {
		return
	}
	s.renderSetup(w, r, sess, "")
}

func (s *Server) handleSetupPost(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.requireSetupAccess(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSetupFormBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid container selection", http.StatusBadRequest)
		return
	}
	validRequest := auth.VerifyCSRF(sess, r.PostForm.Get("csrf_token"))
	if s.localSetup {
		validRequest = sameOriginSetupPost(r)
	}
	if !validRequest {
		http.Error(w, "Invalid CSRF token", http.StatusForbidden)
		return
	}
	change := broker.WatchChange{Action: r.PostForm.Get("action"), Names: r.PostForm["container"]}
	if name := r.PostForm.Get("remove"); name != "" {
		change = broker.WatchChange{Action: "remove", Names: []string{name}}
	}
	if err := s.discovery.ChangeWatchlist(r.Context(), change); err != nil {
		s.renderSetup(w, r, sess, err.Error())
		return
	}
	http.Redirect(w, r, "/setup?saved=1", http.StatusSeeOther)
}

func (s *Server) renderSetup(w http.ResponseWriter, r *http.Request, sess auth.Session, message string) {
	data := setupData{Title: "Setup", ActiveNav: "setup", User: s.toNavUser(sess), Error: message}
	entries, err := s.discovery.DiscoverContainers(r.Context())
	if err != nil && data.Error == "" {
		data.Error = err.Error()
	}
	data.Entries = entries
	for _, entry := range entries {
		if entry.Present && !entry.Managed && !entry.Watching {
			data.NewCount++
		}
	}
	if data.Error == "" && r.URL.Query().Get("saved") == "1" {
		data.Notice = "Dashboard updated."
	}
	s.render(w, "setup.html", data)
}
