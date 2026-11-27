package web

import (
	"errors"
	"log"
	"net/http"

	"github.com/bises/home-server-manager/internal/auth"
)

const maxAccountFormBytes = 8 << 10

type accountData struct {
	Title       string
	ActiveNav   string
	User        navUser
	Setup       bool
	Error       string
	MinPassword int
	MaxPassword int
}

func (s *Server) handleAccountSetup(w http.ResponseWriter, r *http.Request) {
	if !s.auth.NeedsAccountSetup() {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	data := accountData{Title: "Create your operator account", Setup: true}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		s.renderAccount(w, http.StatusOK, data, "")
		return
	}
	if !s.parseAccountForm(w, r, data, auth.Session{}) {
		return
	}
	key := "account-setup:" + immediatePeer(r)
	if !s.allowAccountAttempt(w, key, data) {
		return
	}
	err := s.auth.CreateFirstAccount(r.PostForm.Get("setup_code"), r.PostForm.Get("username"), r.PostForm.Get("password"))
	if err != nil {
		s.renderAccountError(w, data, err)
		return
	}
	s.auth.ResetLoginAttempts(key)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.requireBrowserSession(w, r)
	if !ok {
		return
	}
	data := accountData{Title: "Change password", ActiveNav: "password", User: s.toNavUser(sess)}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		s.renderAccount(w, http.StatusOK, data, "")
		return
	}
	if !s.parseAccountForm(w, r, data, sess) {
		return
	}
	key := "account-password:" + sess.Username
	if !s.allowAccountAttempt(w, key, data) {
		return
	}
	err := s.auth.ChangePassword(sess.ID, r.PostForm.Get("current_password"), r.PostForm.Get("password"))
	if err != nil {
		s.renderAccountError(w, data, err)
		return
	}
	s.auth.ResetLoginAttempts(key)
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func sameOriginAccountPost(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		return origin == "https://"+r.Host
	}
	return r.Header.Get("Sec-Fetch-Site") == "same-origin"
}

func (s *Server) parseAccountForm(w http.ResponseWriter, r *http.Request, data accountData, sess auth.Session) bool {
	if !sameOriginAccountPost(r) {
		http.Error(w, "Same-origin form submission required", http.StatusForbidden)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAccountFormBytes)
	if err := r.ParseForm(); err != nil {
		s.renderAccount(w, http.StatusBadRequest, data, "Invalid form submission")
		return false
	}
	if !data.Setup && !auth.VerifyCSRF(sess, r.PostForm.Get("csrf_token")) {
		http.Error(w, "Invalid CSRF token", http.StatusForbidden)
		return false
	}
	if r.PostForm.Get("password") != r.PostForm.Get("confirm_password") {
		s.renderAccount(w, http.StatusBadRequest, data, "The passwords do not match")
		return false
	}
	return true
}

func (s *Server) allowAccountAttempt(w http.ResponseWriter, key string, data accountData) bool {
	if s.auth.BeginLogin(key) {
		return true
	}
	w.Header().Set("Retry-After", "60")
	s.renderAccount(w, http.StatusTooManyRequests, data, "Too many attempts. Try again in a minute.")
	return false
}

func (s *Server) renderAccountError(w http.ResponseWriter, data accountData, err error) {
	var storage *auth.AccountStoreError
	switch {
	case errors.As(err, &storage):
		log.Print(storage)
		s.renderAccount(w, http.StatusInternalServerError, data, "Could not save the account. Check the server logs.")
	case errors.Is(err, auth.ErrSetupCode), errors.Is(err, auth.ErrCurrentPassword), errors.Is(err, auth.ErrInactiveSession):
		s.renderAccount(w, http.StatusForbidden, data, err.Error())
	default:
		s.renderAccount(w, http.StatusBadRequest, data, err.Error())
	}
}

func (s *Server) renderAccount(w http.ResponseWriter, status int, data accountData, message string) {
	data.Error = message
	data.MinPassword = auth.MinPasswordLength
	data.MaxPassword = auth.MaxPasswordLength
	s.renderStatus(w, status, "account.html", data)
}
