package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestManagedBootstrapExpiresAndCanOnlyBeUsedOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	code, expiry, err := InitManagedConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := InitManagedConfig(path); !errors.Is(err, os.ErrExist) {
		t.Fatalf("initializer should preserve existing state, got %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(raw), code) {
		t.Fatal("setup state must store only the code hash")
	}
	m, err := NewManagedManager(path)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Enabled() || !m.NeedsAccountSetup() {
		t.Fatal("pending setup must enforce authentication")
	}
	m.now = func() time.Time { return expiry }
	password := "a fixture passphrase"
	if err := m.CreateFirstAccount(code, "owner", password); !errors.Is(err, ErrSetupCode) {
		t.Fatalf("expired code should fail, got %v", err)
	}
	m.now = func() time.Time { return expiry.Add(-time.Minute) }
	results := make(chan error, 2)
	var attempts sync.WaitGroup
	for range cap(results) {
		attempts.Go(func() { results <- m.CreateFirstAccount(code, "owner", password) })
	}
	attempts.Wait()
	close(results)
	var successes int
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrSetupCode) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("expected one account creation, got %d", successes)
	}
	reloaded, err := NewManagedManager(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.NeedsAccountSetup() {
		t.Fatal("the code must stay consumed across restarts")
	}
	if session, _, ok := reloaded.AuthenticateUser("owner", password); !ok || session.Role != "operator" {
		t.Fatal("the new operator should be able to sign in after restart")
	}
}

func TestManagedPasswordPersistenceFailurePreservesSessionsAndCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	code, _, err := InitManagedConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManagedManager(path)
	if err != nil {
		t.Fatal(err)
	}
	current, replacement := "old fixture passphrase", "new fixture passphrase"
	if err := m.CreateFirstAccount(code, "owner", current); err != nil {
		t.Fatal(err)
	}
	session, cookie, ok := m.AuthenticateUser("owner", current)
	if !ok {
		t.Fatal("could not sign in")
	}
	m.storePath = t.TempDir() // An existing directory cannot be replaced by the state file.
	var saveError *AccountStoreError
	if err := m.ChangePassword(session.ID, current, replacement); !errors.As(err, &saveError) {
		t.Fatalf("expected a persistence error, got %v", err)
	}
	if _, ok := m.VerifySessionCookie(cookie); !ok {
		t.Fatal("a failed password write should not revoke the session")
	}
	if _, _, ok := m.AuthenticateUser("owner", current); !ok {
		t.Fatal("a failed password write should leave the current password usable")
	}
}
