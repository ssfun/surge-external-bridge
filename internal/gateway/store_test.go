package gateway

import (
	"encoding/json"
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
	if len(config.ProjectionKey) != 24 {
		t.Fatalf("generated projection key length=%d, want 24", len(config.ProjectionKey))
	}
	if strings.ContainsAny(config.ProjectionKey, "+/=") {
		t.Fatalf("generated projection key is not URL-safe: %q", config.ProjectionKey)
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
	if !strings.Contains(string(encoded), `"socks_host"`) || !strings.Contains(string(encoded), `"policy_host"`) || !strings.Contains(string(encoded), `"projection_key"`) || strings.Contains(string(encoded), `"stable_id"`) || strings.Contains(string(encoded), `"virtual_host"`) || strings.Contains(string(encoded), `"policy_base_url"`) {
		t.Fatalf("gateway.json does not persist independent publication hosts: %s", encoded)
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
	if err := os.WriteFile(filepath.Join(dir, "gateway.json"), []byte(`{"schema_version":4}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(dir).Load(); err == nil {
		t.Fatal("unsupported schema was accepted or migrated")
	}
}

func TestStoreMigratesSchemaTwoAndRetainsInlineHostsInSchemaThree(t *testing.T) {
	dir := t.TempDir()
	legacy := mustDefaultConfig(t)
	legacy.SchemaVersion = 2
	legacy.Providers = []Provider{{
		Name: "Inline", Type: "inline", Enabled: true,
		Payload: InlinePayload{{"name": "Node", "type": "socks5", "server": "edge.example.com", "port": 1080}},
	}}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gateway.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewStore(dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SchemaVersion != SchemaVersion || len(loaded.Providers) != 1 || loaded.Providers[0].Hosts != nil {
		t.Fatalf("schema two migration = %#v", loaded)
	}

	loaded.Providers[0].Hosts = map[string]any{"edge.example.com": "origin.example.com"}
	loaded.Providers[0].Prefix = "机场前缀"
	if err := NewStore(dir).Save(loaded); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewStore(dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Providers[0].Hosts["edge.example.com"] != "origin.example.com" {
		t.Fatalf("inline hosts were not retained: %#v", reloaded.Providers[0].Hosts)
	}
	if reloaded.Providers[0].Prefix != "机场前缀" {
		t.Fatalf("Provider prefix was not retained: %#v", reloaded.Providers[0])
	}
}

func TestStoreMigratesSingleVirtualHostToIndependentPublishedHosts(t *testing.T) {
	dir := t.TempDir()
	legacy := mustDefaultConfig(t)
	legacy.SchemaVersion = 1
	legacy.SocksHost = ""
	legacy.PolicyHost = ""
	data, err := json.Marshal(configV1{Config: legacy, VirtualHost: "legacy.surge.eb"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gateway.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := NewStore(dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SchemaVersion != SchemaVersion || loaded.SocksHost != "legacy.surge.eb" || loaded.PolicyHost != "legacy.surge.eb" {
		t.Fatalf("migrated hosts = schema %d, SOCKS %q, Policy %q", loaded.SchemaVersion, loaded.SocksHost, loaded.PolicyHost)
	}
	persisted, err := os.ReadFile(filepath.Join(dir, "gateway.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), `"virtual_host"`) || !strings.Contains(string(persisted), `"socks_host": "legacy.surge.eb"`) || !strings.Contains(string(persisted), `"policy_host": "legacy.surge.eb"`) {
		t.Fatalf("schema migration was not persisted with independent hosts: %s", persisted)
	}
}

func TestStoreRejectsIncompleteCurrentSchemaInsteadOfNormalizing(t *testing.T) {
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

func TestStoreMigratesUnsafeGatewayCredentialsToRepairableState(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	config, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	config.Mode = ModeGateway
	config.HTTPBind = "0.0.0.0:18080"
	config.SocksBind = "0.0.0.0"
	config.SocksHost = "192.168.50.10"
	config.PolicyHost = "policy.surge.eb"
	config.ManagementToken = "management-token-1234"
	config.PolicyToken = "unsafe"
	config.Providers = []Provider{{
		Name: "Legacy Auth", Type: "http", URL: "https://example.com/subscription", Enabled: true,
		Headers: map[string][]string{"Authorization": {"Bearer legacy-secret"}}, RefreshSeconds: 21600, SizeLimit: 16 << 20,
	}}
	assignProviderIDs(&config)
	if err := store.Save(config); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PolicyToken == "unsafe" || len(loaded.PolicyToken) != 24 {
		t.Fatalf("legacy Policy Token was not replaced: %q", loaded.PolicyToken)
	}
	if loaded.Providers[0].Enabled {
		t.Fatal("Provider with redirect-sensitive credentials remained enabled")
	}
	if got := len(store.Notices()); got != 2 {
		t.Fatalf("security migration notices=%d, want 2", got)
	}
	if err := ValidateConfig(loaded); err != nil {
		t.Fatalf("migrated configuration is not repairable: %v", err)
	}
	encoded, err := os.ReadFile(filepath.Join(dir, "gateway.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"policy_token": "unsafe"`) || strings.Contains(string(encoded), `"enabled": true`) {
		t.Fatalf("security migration was not persisted: %s", encoded)
	}
}

func TestStoreMigratesUnsafePolicyTokenInLocalMode(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	config, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	config.PolicyToken = "unsafe"
	if err := store.Save(config); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PolicyToken == "unsafe" || len(loaded.PolicyToken) != 24 {
		t.Fatalf("legacy local Policy Token was not replaced: %q", loaded.PolicyToken)
	}
	if got := len(store.Notices()); got != 1 {
		t.Fatalf("security migration notices=%d, want 1", got)
	}
	if err := ValidateConfig(loaded); err != nil {
		t.Fatalf("migrated local configuration is invalid: %v", err)
	}
}
