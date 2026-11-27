package auth

import (
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"
)

const (
	bootstrapLifetime = 20 * time.Minute
	MinPasswordLength = 15
	MaxPasswordLength = 128
)

var (
	ErrSetupCode       = errors.New("the setup code is invalid, expired, or already used")
	ErrCurrentPassword = errors.New("the current password is incorrect")
	ErrInactiveSession = errors.New("sign in again to change your password")
	validUsername      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
)

type Bootstrap struct {
	CodeSHA256 string    `json:"code_sha256"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// InitManagedConfig creates a private state file. The caller delivers the code to the owner.
func InitManagedConfig(path string) (string, time.Time, error) {
	code := randomHex(24)
	expires := time.Now().UTC().Add(bootstrapLifetime).Truncate(time.Second)
	cfg := Config{Bootstrap: &Bootstrap{CodeSHA256: HashSecret(code), ExpiresAt: expires}}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", time.Time{}, err
	}
	if err := writeAuthConfig(file, cfg); err != nil {
		_ = os.Remove(path)
		return "", time.Time{}, err
	}
	return code, expires, nil
}

func NewManagedManager(path string) (*Manager, error) {
	cfg, err := LoadConfig(path)
	if err != nil {
		return nil, err
	}
	if cfg.Bootstrap != nil {
		digest, err := hex.DecodeString(cfg.Bootstrap.CodeSHA256)
		if err != nil || len(digest) != 32 || cfg.Bootstrap.ExpiresAt.IsZero() || len(cfg.Users) != 0 || len(cfg.Tokens) != 0 {
			return nil, fmt.Errorf("invalid initial account setup configuration")
		}
	} else if len(cfg.Users) == 0 && len(cfg.Tokens) == 0 {
		return nil, fmt.Errorf("managed auth requires a setup code, user, or token")
	}
	names := make(map[string]bool, len(cfg.Users))
	for _, user := range cfg.Users {
		if names[user.Username] {
			return nil, fmt.Errorf("managed auth requires unique usernames")
		}
		names[user.Username] = true
	}
	m := NewManager(cfg)
	m.enabled = true
	m.storePath = path
	m.managed = cfg
	return m, nil
}

func (m *Manager) ManagesAccounts() bool {
	return m != nil && m.storePath != ""
}

func (m *Manager) NeedsAccountSetup() bool {
	if !m.ManagesAccounts() {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.managed.Bootstrap != nil
}

func (m *Manager) CreateFirstAccount(code, username, password string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	setup := m.managed.Bootstrap
	if setup == nil || !m.now().Before(setup.ExpiresAt) ||
		subtle.ConstantTimeCompare([]byte(HashSecret(code)), []byte(setup.CodeSHA256)) != 1 {
		return ErrSetupCode
	}
	if !validUsername.MatchString(username) {
		return fmt.Errorf("use 1–64 letters, numbers, periods, underscores or hyphens, starting with a letter or number")
	}
	if err := validateNewPassword(password); err != nil {
		return err
	}
	next := m.managed
	next.Bootstrap = nil
	next.Users = []UserConfig{{Username: username, Role: "operator", PasswordHash: HashPassword(password)}}
	return m.saveAccounts(next)
}

func (m *Manager) ChangePassword(sessionID, current, password string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[sessionID]
	if !m.ManagesAccounts() || !ok || !m.now().Before(sess.ExpiresAt) {
		return ErrInactiveSession
	}
	index := slices.IndexFunc(m.users, func(user UserConfig) bool { return user.Username == sess.Username })
	if index == -1 || !VerifyPassword(current, m.users[index].PasswordHash) {
		return ErrCurrentPassword
	}
	if current == password {
		return fmt.Errorf("choose a different password")
	}
	if err := validateNewPassword(password); err != nil {
		return err
	}
	next := m.managed
	next.Users = slices.Clone(m.users)
	next.Users[index].PasswordHash = HashPassword(password)
	if err := m.saveAccounts(next); err != nil {
		return err
	}
	for id, active := range m.sessions {
		if active.Username == sess.Username {
			delete(m.sessions, id)
		}
	}
	return nil
}

func validateNewPassword(password string) error {
	length := utf8.RuneCountInString(password)
	if !utf8.ValidString(password) || length < MinPasswordLength || length > MaxPasswordLength {
		return fmt.Errorf("use a password between %d and %d characters", MinPasswordLength, MaxPasswordLength)
	}
	return nil
}

// AccountStoreError distinguishes persistence failures from invalid form input.
type AccountStoreError struct{ Err error }

func (e *AccountStoreError) Error() string { return "save account state: " + e.Err.Error() }
func (e *AccountStoreError) Unwrap() error { return e.Err }

// saveAccounts updates the on-disk state before publishing the new credentials in memory.
// The caller holds m.mu.
func (m *Manager) saveAccounts(cfg Config) error {
	file, err := os.CreateTemp(filepath.Dir(m.storePath), "auth-pending-*.json")
	if err != nil {
		return &AccountStoreError{Err: err}
	}
	defer os.Remove(file.Name())
	if err := writeAuthConfig(file, cfg); err != nil {
		return &AccountStoreError{Err: err}
	}
	if err := os.Rename(file.Name(), m.storePath); err != nil {
		return &AccountStoreError{Err: err}
	}
	m.managed = cfg
	m.users = cfg.Users
	return nil
}

func writeAuthConfig(file *os.File, cfg Config) error {
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	err := encoder.Encode(cfg)
	if err == nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}
