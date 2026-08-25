package gateway

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreMigratesLegacyProviderConfigWithoutNodeState(t *testing.T) {
	dir := t.TempDir()
	legacy := `{
  "mode":"local","http_bind":"127.0.0.1:18080","socks_bind":"127.0.0.1",
  "socks_port":1080,"socks_advertise":"127.0.0.1","policy_base_url":"http://127.0.0.1:18080",
  "prefix_subscription":true,"exclude_name":"Expire",
  "subscriptions":[
    {"id":"sub_stable","name":"Airport","source_type":"url","url":"https://example.com/sub?token=secret","filter":"Hong Kong","enabled":true,"headers":{"Authorization":"Bearer secret"},"refresh_seconds":3600},
    {"id":"sub_manual","name":"Old manual","source_type":"manual","enabled":true}
  ]
}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"snapshots":{"secret":"must-not-migrate"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	config, migrated, err := NewStore(dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if !migrated || len(config.Providers) != 1 {
		t.Fatalf("migration result: migrated=%v Providers=%d", migrated, len(config.Providers))
	}
	provider := config.Providers[0]
	if provider.StableID != "sub_stable" || provider.IncludeName != "Hong Kong" || provider.ExcludeName != "Expire" {
		t.Fatalf("Provider identity or projection filters changed: %#v", provider)
	}
	if provider.Headers["Authorization"][0] != "Bearer secret" {
		t.Fatal("sensitive Provider Header was not migrated")
	}
	if config.NodeTestUDP != "8.8.8.8:53" {
		t.Fatalf("migrated UDP diagnostic target=%q, want 8.8.8.8:53", config.NodeTestUDP)
	}
	backup, err := os.ReadFile(filepath.Join(dir, "migration-v1-readonly", "state.json"))
	if err != nil || string(backup) != `{"snapshots":{"secret":"must-not-migrate"}}` {
		t.Fatalf("legacy state backup missing or changed: %q %v", backup, err)
	}
	backupInfo, err := os.Stat(filepath.Join(dir, "migration-v1-readonly", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if backupInfo.Mode().Perm() != 0o400 {
		t.Fatalf("legacy backup is not read-only: mode=%v", backupInfo.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(dir, "gateway.json")); err != nil {
		t.Fatal("new gateway config was not persisted")
	}
}

func TestStoreUsesPrivatePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gateway")
	store := NewStore(dir)
	if err := store.Save(DefaultConfig()); err == nil {
		t.Fatal("Save unexpectedly succeeded before Store.Load created the data directory")
	}
	if _, _, err := store.Load(); err != nil {
		t.Fatal(err)
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
	if _, _, err := NewStore(linkedDir).Load(); err == nil {
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
	if _, _, err := NewStore(dir).Load(); err == nil {
		t.Fatal("symbolic-link gateway.json was accepted")
	}
}

func TestStoreDoesNotCommitUnsupportedLegacyHeaders(t *testing.T) {
	dir := t.TempDir()
	legacy := `{
  "mode":"local","http_bind":"127.0.0.1:18080","socks_bind":"127.0.0.1",
  "socks_port":1080,"socks_advertise":"127.0.0.1","policy_base_url":"http://127.0.0.1:18080",
  "subscriptions":[{"id":"sub","name":"Provider","source_type":"url","url":"https://example.com/sub","enabled":true,"headers":{"X-Subscription-Token":"secret"},"refresh_seconds":3600}]
}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewStore(dir).Load(); err == nil {
		t.Fatal("unsupported legacy Header did not block migration")
	}
	if _, err := os.Stat(filepath.Join(dir, "gateway.json")); !os.IsNotExist(err) {
		t.Fatalf("failed migration committed gateway.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "migration-v1-readonly", "config.json")); err != nil {
		t.Fatalf("failed migration did not preserve read-only backup: %v", err)
	}
}

func TestStoreNormalizesProviderDefaultsForCurrentSchema(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{
  "schema_version":2,
  "mode":"local",
  "http_bind":"127.0.0.1:18080",
  "socks_bind":"127.0.0.1",
  "socks_port":1080,
  "socks_advertise":"127.0.0.1",
  "policy_base_url":"http://127.0.0.1:18080",
  "prefix_provider":true,
  "projection_types":["vless"],
  "providers":[{
    "stable_id":"provider-1",
    "name":"Provider",
    "type":"http",
    "url":"https://example.com/subscription",
    "enabled":true,
    "health_check":true
  }]
}`)
	if err := os.WriteFile(filepath.Join(dir, "gateway.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, migrated, err := NewStore(dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if migrated || len(loaded.Providers) != 1 {
		t.Fatalf("unexpected load result: migrated=%v Providers=%d", migrated, len(loaded.Providers))
	}
	provider := loaded.Providers[0]
	if provider.SizeLimit != 16<<20 {
		t.Fatalf("size limit=%d, want %d", provider.SizeLimit, 16<<20)
	}
	if provider.HealthCheckURL != "https://www.gstatic.com/generate_204" || provider.HealthCheckSeconds != 300 || provider.HealthCheckTimeout != 5000 || provider.ExpectedStatus != "200-399" {
		t.Fatalf("health defaults were not normalized: %#v", provider)
	}
	defaults := DefaultConfig()
	if loaded.NodeTestURL != defaults.NodeTestURL || loaded.NodeTestUDP != defaults.NodeTestUDP || loaded.NodeTestTimeout != defaults.NodeTestTimeout {
		t.Fatalf("diagnostic defaults were not normalized: %#v", loaded)
	}
}
