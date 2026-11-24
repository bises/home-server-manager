package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

const SessionCookieName = "hsm_session"

// UserConfig defines a browser user account.
type UserConfig struct {
	Username       string `json:"username"`
	Role           string `json:"role"` // viewer | operator
	PasswordSHA256 string `json:"password_sha256"`
}

// TokenConfig defines a scoped machine credential (e.g. Home Assistant).
type TokenConfig struct {
	ID          string              `json:"id"`
	Actor       string              `json:"actor"`
	TokenSHA256 string              `json:"token_sha256"`
	Read        bool                `json:"read"`
	Actions     map[string][]string `json:"actions,omitempty"`
}

// Config holds browser users and machine tokens.
type Config struct {
	SessionTTLHours int           `json:"session_ttl_hours,omitempty"`
	Users           []UserConfig  `json:"users"`
	Tokens          []TokenConfig `json:"tokens"`
}

// Session represents an authenticated browser session.
type Session struct {
	ID        string
	Username  string
	Role      string
	CSRFToken string
	ExpiresAt time.Time
}

// Principal represents the authenticated caller (browser user or machine token).
type Principal struct {
	Kind      string // "browser" | "token"
	Actor     string
	Role      string // "viewer" | "operator" | "machine"
	CSRFToken string
	Read      bool
	Actions   map[string][]string
}

// CanAct returns true if the principal is authorized to run verb on serviceID.
func (p Principal) CanAct(serviceID, verb string) bool {
	if p.Kind == "browser" {
		return p.Role == "operator"
	}
	if p.Kind == "token" {
		verbs := p.Actions[serviceID]
		return slices.Contains(verbs, verb)
	}
	return false
}

// Manager manages browser sessions and machine token verification.
type Manager struct {
	enabled    bool
	sessionTTL time.Duration
	hmacKey    []byte
	users      []UserConfig
	tokens     []TokenConfig

	mu       sync.Mutex
	sessions map[string]Session
}

// LoadConfig reads an auth JSON file.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read auth config %q: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse auth config %q: %w", path, err)
	}
	return cfg, nil
}

// NewManager constructs an auth Manager. If cfg has no users or tokens, Enabled() is false.
func NewManager(cfg Config) *Manager {
	ttl := time.Duration(cfg.SessionTTLHours) * time.Hour
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)

	return &Manager{
		enabled:    len(cfg.Users) > 0 || len(cfg.Tokens) > 0,
		sessionTTL: ttl,
		hmacKey:    key,
		users:      cfg.Users,
		tokens:     cfg.Tokens,
		sessions:   make(map[string]Session),
	}
}

// Enabled reports whether authentication is enforced.
func (m *Manager) Enabled() bool {
	return m != nil && m.enabled
}

// HashSecret returns the lowercase hex SHA-256 digest of a plaintext secret.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// AuthenticateUser verifies username and password and issues a new browser session.
func (m *Manager) AuthenticateUser(username, password string) (Session, string, bool) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return Session{}, "", false
	}
	passHash := HashSecret(password)
	for _, u := range m.users {
		if subtle.ConstantTimeCompare([]byte(u.Username), []byte(username)) == 1 &&
			subtle.ConstantTimeCompare([]byte(strings.ToLower(u.PasswordSHA256)), []byte(passHash)) == 1 {
			sess := Session{
				ID:        randomHex(16),
				Username:  u.Username,
				Role:      u.Role,
				CSRFToken: randomHex(16),
				ExpiresAt: time.Now().UTC().Add(m.sessionTTL),
			}
			m.mu.Lock()
			m.sessions[sess.ID] = sess
			m.mu.Unlock()
			return sess, m.signSessionID(sess.ID), true
		}
	}
	return Session{}, "", false
}

// VerifySessionCookie validates the signed cookie and returns the active session.
func (m *Manager) VerifySessionCookie(val string) (Session, bool) {
	parts := strings.SplitN(val, ".", 2)
	if len(parts) != 2 {
		return Session{}, false
	}
	id, sigHex := parts[0], parts[1]
	expected := m.signHMAC(id)
	if subtle.ConstantTimeCompare([]byte(sigHex), []byte(expected)) != 1 {
		return Session{}, false
	}

	m.mu.Lock()
	sess, ok := m.sessions[id]
	if ok && time.Now().UTC().After(sess.ExpiresAt) {
		delete(m.sessions, id)
		ok = false
	}
	m.mu.Unlock()
	if !ok {
		return Session{}, false
	}
	return sess, true
}

// DeleteSession invalidates a session cookie on logout.
func (m *Manager) DeleteSession(val string) {
	parts := strings.SplitN(val, ".", 2)
	if len(parts) < 1 || parts[0] == "" {
		return
	}
	m.mu.Lock()
	delete(m.sessions, parts[0])
	m.mu.Unlock()
}

// VerifyBearer validates an Authorization: Bearer <token> header.
func (m *Manager) VerifyBearer(authHeader string) (Principal, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(authHeader, prefix) {
		return Principal{}, false
	}
	raw := strings.TrimSpace(strings.TrimPrefix(authHeader, prefix))
	if raw == "" {
		return Principal{}, false
	}
	hash := HashSecret(raw)
	for _, tok := range m.tokens {
		if subtle.ConstantTimeCompare([]byte(strings.ToLower(tok.TokenSHA256)), []byte(hash)) == 1 {
			actor := tok.Actor
			if actor == "" {
				actor = tok.ID
			}
			return Principal{
				Kind:    "token",
				Actor:   actor,
				Role:    "machine",
				Read:    tok.Read,
				Actions: tok.Actions,
			}, true
		}
	}
	return Principal{}, false
}

// VerifyCSRF checks a submitted CSRF token against the session's token.
func VerifyCSRF(sess Session, token string) bool {
	if sess.CSRFToken == "" || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(sess.CSRFToken), []byte(token)) == 1
}

func (m *Manager) signSessionID(id string) string {
	return id + "." + m.signHMAC(id)
}

func (m *Manager) signHMAC(data string) string {
	mac := hmac.New(sha256.New, m.hmacKey)
	_, _ = mac.Write([]byte(data))
	return hex.EncodeToString(mac.Sum(nil))
}

func randomHex(nBytes int) string {
	buf := make([]byte, nBytes)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
