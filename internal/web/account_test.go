package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bises/home-server-manager/internal/auth"
)

func TestBrowserAccountSetupAndPasswordChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	code, _, err := auth.InitManagedConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := auth.NewManagedManager(path)
	if err != nil {
		t.Fatal(err)
	}
	fixture, _ := newTestServer(t, false)
	server, err := NewServer(fixture.snap, Options{Auth: manager})
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	cleartext := httptest.NewRecorder()
	handler.ServeHTTP(cleartext, httptest.NewRequest(http.MethodGet, "http://local.test/account/setup", nil))
	if cleartext.Code != http.StatusForbidden {
		t.Fatalf("setup over HTTP returned %d", cleartext.Code)
	}

	tlsServer := httptest.NewTLSServer(handler)
	defer tlsServer.Close()
	client := tlsServer.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	request := func(path string, origin string, form url.Values, cookie *http.Cookie, want int) (*http.Response, string) {
		t.Helper()
		method := http.MethodGet
		if form != nil {
			method = http.MethodPost
		}
		req, err := http.NewRequest(method, tlsServer.URL+path, strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Origin", origin)
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != want {
			t.Fatalf("%s %s returned %d, want %d", method, path, res.StatusCode, want)
		}
		return res, string(body)
	}
	login, _ := request("/login", "", nil, nil, http.StatusSeeOther)
	if login.Header.Get("Location") != "/account/setup" {
		t.Fatal("initial login should lead to account setup")
	}
	request("/api/v1/snapshot", "", nil, nil, http.StatusUnauthorized)
	_, page := request("/account/setup", "", nil, nil, http.StatusOK)
	if !strings.Contains(page, "One-time setup code") || strings.Contains(page, code) {
		t.Fatal("setup page should prompt for the code without showing it")
	}
	current, replacement := "old fixture passphrase", "new fixture passphrase"
	setup := url.Values{
		"setup_code": {code}, "username": {"owner"},
		"password": {current}, "confirm_password": {current},
	}
	request("/account/setup", "https://other.test", setup, nil, http.StatusForbidden)
	setup.Set("setup_code", "invalid")
	request("/account/setup", tlsServer.URL, setup, nil, http.StatusForbidden)
	setup.Set("setup_code", code)
	request("/account/setup", tlsServer.URL, setup, nil, http.StatusSeeOther)
	request("/account/setup", tlsServer.URL, setup, nil, http.StatusSeeOther)
	signIn := url.Values{"username": {"owner"}, "password": {current}}
	signInResponse, _ := request("/login", tlsServer.URL, signIn, nil, http.StatusSeeOther)
	cookies := signInResponse.Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly {
		t.Fatal("managed login must issue a secure, HTTP-only session cookie")
	}
	cookie := cookies[0]
	session, ok := manager.VerifySessionCookie(cookie.Value)
	if !ok {
		t.Fatal("expected an active session")
	}
	secondLogin, _ := request("/login", tlsServer.URL, signIn, nil, http.StatusSeeOther)
	secondCookie := secondLogin.Cookies()[0]
	request("/account/password", "", nil, nil, http.StatusSeeOther)
	_, page = request("/account/password", "", nil, cookie, http.StatusOK)
	if !strings.Contains(page, "Current password") || !strings.Contains(page, session.CSRFToken) {
		t.Fatal("password page should prompt for the current password and include CSRF protection")
	}
	change := url.Values{
		"current_password": {"wrong"}, "password": {replacement}, "confirm_password": {replacement},
	}
	request("/account/password", tlsServer.URL, change, cookie, http.StatusForbidden)
	change.Set("csrf_token", session.CSRFToken)
	request("/account/password", tlsServer.URL, change, cookie, http.StatusForbidden)
	change.Set("current_password", current)
	changeResponse, _ := request("/account/password", tlsServer.URL, change, cookie, http.StatusSeeOther)
	if cookies := changeResponse.Cookies(); len(cookies) != 1 || cookies[0].MaxAge != -1 {
		t.Fatal("password change should clear the browser session cookie")
	}
	request("/account/password", "", nil, cookie, http.StatusSeeOther)
	request("/account/password", "", nil, secondCookie, http.StatusSeeOther)
	request("/login", tlsServer.URL, signIn, nil, http.StatusUnauthorized)
	reloaded, err := auth.NewManagedManager(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := reloaded.AuthenticateUser("owner", replacement); !ok {
		t.Fatal("the new password should work after restart")
	}
}
