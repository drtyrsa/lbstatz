package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lbstatz.toml")
	writeFile(t, path, `
token = "tok"
username = "alice"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "tok" || cfg.Username != "alice" {
		t.Fatalf("unexpected values: %+v", cfg)
	}
	if want := filepath.Join(dir, dbName); cfg.DBPath != want {
		t.Fatalf("db path = %q, want %q", cfg.DBPath, want)
	}
}

func TestLoadExplicitDBPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lbstatz.toml")
	writeFile(t, path, `
username = "alice"
db_path = "/data/listens.db"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBPath != "/data/listens.db" {
		t.Fatalf("db path = %q", cfg.DBPath)
	}
}

func TestLoadMissingUsername(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lbstatz.toml")
	writeFile(t, path, `token = "tok"`)

	if _, err := Load(path); err == nil {
		t.Fatal("expected error for missing username")
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.toml")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
