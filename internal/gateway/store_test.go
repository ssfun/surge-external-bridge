package gateway

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStoreCreatesFreshPrivateConfiguration(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gateway")
	store := NewStore(dir)
	unsaved, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(unsaved); err == nil {
		t.Fatal("Save unexpectedly succeeded before Store.Load created the data directory")
	}
	config, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	want := mustDefaultConfig(t)
	want.ProjectionKey = config.ProjectionKey
	if !reflect.DeepEqual(config, want) {
		t.Fatalf("fresh config = %#v, want defaults", config)
	}
	if len(config.ProjectionKey) != 64 {
		t.Fatalf("generated projection key length=%d, want 64", len(config.ProjectionKey))
	}
	info, err := os.Stat(filepath.Join(dir, "gateway.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("gateway.json mode=%o, want 600", info.Mode().Perm())
	}
	encoded, err := os.ReadFile(filepath.Join(dir, "gateway.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"virtual_host"`) || !strings.Contains(string(encoded), `"projection_key"`) || strings.Contains(string(encoded), `"stable_id"`) || strings.Contains(string(encoded), `"socks_advertise"`) || strings.Contains(string(encoded), `"policy_base_url"`) {
		t.Fatalf("gateway.json does not use virtual_host as its single publication source: %s", encoded)
	}
	directory, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if directory.Mode().Perm() != 0o700 {
		t.Fatalf("data directory mode=%o, want 700", directory.Mode().Perm())
	}
}

func TestStoreRejectsSymbolicLinkBoundaries(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedDir := filepath.Join(root, "linked")
	if err := os.Symlink(realDir, linkedDir); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(linkedDir).Load(); err == nil {
		t.Fatal("symbolic-link data directory was accepted")
	}

	dir := filepath.Join(root, "store")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "outside.json")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "gateway.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(dir).Load(); err == nil {
		t.Fatal("symbolic-link gateway.json was accepted")
	}
}

func TestStoreDoesNotReadLegacyConfiguration(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"subscriptions":[{"name":"legacy"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := NewStore(dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	want := mustDefaultConfig(t)
	want.ProjectionKey = config.ProjectionKey
	if !reflect.DeepEqual(config, want) {
		t.Fatalf("legacy file affected fresh configuration: %#v", config)
	}
}

func TestStoreRejectsUnsupportedSchemaWithoutMigration(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gateway.json"), []byte(`{"schema_version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(dir).Load(); err == nil {
		t.Fatal("unsupported schema was accepted or migrated")
	}
}

func TestStoreRejectsIncompleteCurrentSchemaInsteadOfNormalizing(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{
  "schema_version":1,
  "mode":"local",
  "http_bind":"127.0.0.1:18080",
  "socks_bind":"127.0.0.1",
  "socks_port":1080,
  "socks_advertise":"127.0.0.1",
  "policy_base_url":"http://127.0.0.1:18080",
  "prefix_provider":true,
	"projection_types":["*"],
	"node_test_url":"https://www.gstatic.com/generate_204",
	"node_test_udp_address":"8.8.8.8:53",
	"node_test_timeout_seconds":15,
  "providers":[]
}`)
	if err := os.WriteFile(filepath.Join(dir, "gateway.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewStore(dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateConfig(loaded); err == nil {
		t.Fatal("legacy independent publish fields were accepted or normalized")
	}
}
