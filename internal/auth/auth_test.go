package auth

import "testing"

func TestBrowserSessionCSRFAndScopedTokens(t *testing.T) {
	m := NewManager(Config{
		Users: []UserConfig{
			{Username: "viewer1", Role: "viewer", PasswordSHA256: HashSecret("view-pass")},
			{Username: "op1", Role: "operator", PasswordSHA256: HashSecret("op-pass")},
		},
		Tokens: []TokenConfig{
			{ID: "ha-read", Actor: "home-assistant-sensors", TokenSHA256: HashSecret("read-tok"), Read: true},
			{
				ID:          "ha-act",
				Actor:       "home-assistant-actions",
				TokenSHA256: HashSecret("act-tok"),
				Read:        false,
				Actions:     map[string][]string{"fixture-web": {"restart"}},
			},
		},
	})

	if !m.Enabled() {
		t.Fatal("expected auth manager enabled")
	}

	// Wrong password fails.
	if _, _, ok := m.AuthenticateUser("op1", "wrong"); ok {
		t.Fatal("expected wrong password to fail")
	}

	// Operator session succeeds and verifies cookie + CSRF.
	sess, cookie, ok := m.AuthenticateUser("op1", "op-pass")
	if !ok {
		t.Fatal("expected op1 login to succeed")
	}
	gotSess, ok := m.VerifySessionCookie(cookie)
	if !ok || gotSess.Role != "operator" {
		t.Fatalf("expected valid operator session, got %+v", gotSess)
	}
	if !VerifyCSRF(gotSess, sess.CSRFToken) || VerifyCSRF(gotSess, "bad-csrf") {
		t.Fatal("CSRF verification mismatch")
	}
	m.DeleteSession(cookie)
	if _, ok := m.VerifySessionCookie(cookie); ok {
		t.Fatal("expected deleted session to be rejected")
	}

	// Scoped tokens.
	readP, ok := m.VerifyBearer("Bearer read-tok")
	if !ok || !readP.Read || readP.CanAct("fixture-web", "restart") {
		t.Fatalf("read-only token should read but not act: %+v", readP)
	}
	actP, ok := m.VerifyBearer("Bearer act-tok")
	if !ok || actP.Read || !actP.CanAct("fixture-web", "restart") || actP.CanAct("fixture-web", "stop") || actP.CanAct("fixture-db", "restart") {
		t.Fatalf("action token scope mismatch: %+v", actP)
	}
}
