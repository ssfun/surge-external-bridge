package gateway

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreProviderUploadUsesPrivateManagedPath(t *testing.T) {
	dir := t.TempDir()
	app := &App{store: NewStore(dir)}
	content := "proxies:\n  - name: Uploaded\n    type: direct\n"
	relative, err := app.StoreProviderUpload(strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.IsAbs(relative) || filepath.Dir(filepath.FromSlash(relative)) != "uploads" {
		t.Fatalf("upload path is not private and relative: %q", relative)
	}
	path := filepath.Join(dir, "mihomo", filepath.FromSlash(relative))
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("upload mode = %v, want regular 0600", info.Mode())
	}
	encoded, err := os.ReadFile(path)
	if err != nil || string(encoded) != content {
		t.Fatalf("upload content = %q, err=%v", encoded, err)
	}
	app.DiscardProviderUpload(relative)
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("managed upload was not removed: %v", err)
	}
}

func TestStoreProviderUploadRejectsEmptyAndOversizedFiles(t *testing.T) {
	app := &App{store: NewStore(t.TempDir())}
	for name, content := range map[string][]byte{
		"empty":     nil,
		"oversized": bytes.Repeat([]byte{'x'}, int(MaxProviderUploadSize)+1),
	} {
		t.Run(name, func(t *testing.T) {
			if path, err := app.StoreProviderUpload(bytes.NewReader(content)); err == nil {
				t.Fatalf("invalid upload was accepted at %q", path)
			}
		})
	}
	uploads := filepath.Join(app.store.Dir(), "mihomo", "uploads")
	entries, err := os.ReadDir(uploads)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed uploads left private files: %#v", entries)
	}
}

func TestDiscardProviderUploadNeverRemovesUnmanagedPaths(t *testing.T) {
	dir := t.TempDir()
	app := &App{store: NewStore(dir)}
	external := filepath.Join(dir, "external.yaml")
	if err := os.WriteFile(external, []byte("proxies: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	app.DiscardProviderUpload(external)
	app.DiscardProviderUpload("../external.yaml")
	if _, err := os.Stat(external); err != nil {
		t.Fatalf("unmanaged file was removed: %v", err)
	}
}
