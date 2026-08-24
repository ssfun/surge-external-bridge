package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ssfun/vless2surge/internal/domain"
)

func TestAtomicFilesArePrivate(t *testing.T) {
	dir := t.TempDir()
	store := New(filepath.Join(dir, "data"))
	config, state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveConfig(config); err != nil {
		t.Fatal(err)
	}
	state.Registry["secret"] = domain.Identity{Password: "do-not-leak"}
	if err := store.SaveState(&state); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.json", "state.json"} {
		info, err := os.Stat(filepath.Join(dir, "data", name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s permissions are %o", name, info.Mode().Perm())
		}
	}
}

func TestLegacyUnversionedStateMigratesToSchemaOne(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := map[string]any{
		"schema_version":  0,
		"auto_start":      true,
		"last_exit_clean": true,
		"applied": map[string]any{
			"id":    "legacy-revision",
			"nodes": []any{},
		},
	}
	content, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "state.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	_, state, err := New(directory).Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.SchemaVersion != domain.SchemaVersion || !state.AutoStart || state.Applied == nil || state.Applied.ID != "legacy-revision" {
		t.Fatalf("legacy state was not preserved during migration: %+v", state)
	}
	if state.Snapshots == nil || state.Registry == nil {
		t.Fatal("migration did not initialize required maps")
	}
}

func TestFutureStateSchemaFailsWithRecoveryGuidance(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte(`{"schema_version":999,"last_exit_clean":true}`)
	if err := os.WriteFile(filepath.Join(directory, "state.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := New(directory).Load()
	if err == nil || !strings.Contains(err.Error(), "restore a compatible backup") {
		t.Fatalf("future schema error lacks recovery guidance: %v", err)
	}
}

func TestCorruptStateIsNotOverwritten(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "state.json")
	original := []byte(`{"schema_version":`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := New(directory).Load(); err == nil {
		t.Fatal("corrupt state unexpectedly loaded")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatal("corrupt state was modified while reporting the load failure")
	}
}
