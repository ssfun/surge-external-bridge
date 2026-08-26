package gateway

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestStoreCreatesFreshPrivateConfiguration(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gateway")
	store := NewStore(dir)
	if err := store.Save(DefaultConfig()); err == nil {
		t.Fatal("Save unexpectedly succeeded before Store.Load created the data directory")
	}
	config, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(config, DefaultConfig()) {
		t.Fatalf("fresh config = %#v, want defaults", config)
	}
	info, err := os.Stat(filepath.Join(dir, "gateway.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("gateway.json mode=%o, want 600", info.Mode().Perm())
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
	if !reflect.DeepEqual(config, DefaultConfig()) {
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
  "projection_types":["vless"],
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
		t.Fatal("legacy projection scope was accepted or normalized")
	}
}
