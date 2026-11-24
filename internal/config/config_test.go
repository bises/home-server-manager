package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFixtureConfig(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "fixtures", "manager.json"))
	if err != nil {
		t.Fatalf("Load fixture config: %v", err)
	}
	if !cfg.ReadOnly {
		t.Fatalf("expected read_only=true in Phase 1 fixture")
	}
	if len(cfg.Services) != 4 {
		t.Fatalf("expected 4 fixture services, got %d", len(cfg.Services))
	}
	db, ok := cfg.FindService("fixture-db")
	if !ok || !db.Protected {
		t.Fatalf("expected fixture-db to be present and protected")
	}
}

func TestValidateRejectsDuplicates(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "dup.json")
	content := `{
  "services": [
    {"id": "a", "stack": "s1", "service": "svc"},
    {"id": "a", "stack": "s2", "service": "svc2"}
  ]
}`
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatalf("expected error on duplicate service id")
	}
}
