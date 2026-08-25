package mihomo

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	C "github.com/metacubex/mihomo/constant"
)

func TestBuildControlledConfigHasOnlyPrivateControllerAndRouter(t *testing.T) {
	home := shortTempDir(t)
	store := NewSnapshotStore(nil)
	cfg, err := BuildControlledConfig(home, filepath.Join(home, "controller.sock"), "controller-secret", nil, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateControlledConfig(cfg, home); err != nil {
		t.Fatal(err)
	}
	if cfg.Proxies[RouterName] == nil {
		t.Fatal("surgeeb-router was not injected")
	}
	if _, exists := cfg.Providers["surgeeb-provider-unexpected"]; exists {
		t.Fatal("unexpected product Provider was constructed")
	}
}

func TestBuildControlledConfigCreatesNativeFileProviderWithoutApplyingProjectionFilters(t *testing.T) {
	home := shortTempDir(t)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	providerPath := filepath.Join(home, "manual.yaml")
	if err := os.WriteFile(providerPath, []byte("proxies: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	definitions := []ProviderDefinition{{
		StableID: "stable_1", Name: "Manual", Type: "file", FilePath: providerPath,
		IncludeName: "only-projection", IncludeTypes: []C.AdapterType{C.Vless},
	}}
	cfg, err := BuildControlledConfig(home, filepath.Join(home, "controller.sock"), "controller-secret", definitions, NewSnapshotStore(nil))
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ProviderKey("stable_1")
	if cfg.Providers[key] == nil {
		t.Fatal("native Mihomo Provider was not constructed")
	}
	views, err := ProviderViews(cfg, definitions)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].IncludeName != "only-projection" || len(views[0].IncludeTypes) != 1 {
		t.Fatalf("projection filters were not retained in the projection layer: %#v", views)
	}
}

func TestBuildControlledConfigRejectsUnsafeProviderAndControllerPaths(t *testing.T) {
	home := shortTempDir(t)
	store := NewSnapshotStore(nil)
	if _, err := BuildControlledConfig(home, filepath.Join(home, "..", "controller.sock"), "secret", nil, store); err == nil {
		t.Fatal("escaped Controller path was accepted")
	}
	definitions := []ProviderDefinition{{StableID: "p", Type: "file", FilePath: filepath.Join(home, "..", "subscription.yaml")}}
	if _, err := BuildControlledConfig(home, filepath.Join(home, "controller.sock"), "secret", definitions, store); err == nil {
		t.Fatal("escaped Provider path was accepted")
	}
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	if err := os.WriteFile(outside, []byte("proxies: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(home, "linked.yaml")
	if err := os.Symlink(outside, linked); err != nil {
		t.Fatal(err)
	}
	definitions = []ProviderDefinition{{StableID: "p", Type: "file", FilePath: linked}}
	if _, err := BuildControlledConfig(home, filepath.Join(home, "controller.sock"), "secret", definitions, store); err == nil {
		t.Fatal("Provider symbolic link escaping Mihomo HomeDir was accepted")
	}
}

func TestBuildControlledConfigRejectsNonHTTPSubscriptionURL(t *testing.T) {
	home := shortTempDir(t)
	definitions := []ProviderDefinition{{StableID: "p", Type: "http", URL: "file:///etc/passwd"}}
	if _, err := BuildControlledConfig(home, filepath.Join(home, "controller.sock"), "secret", definitions, NewSnapshotStore(nil)); err == nil {
		t.Fatal("non-HTTP subscription URL was accepted")
	}
}

func TestBuildControlledConfigCreatesNativeInlineProvider(t *testing.T) {
	home := shortTempDir(t)
	definitions := []ProviderDefinition{{StableID: "inline", Name: "Inline", Type: "inline", Payload: []map[string]any{{
		"name": "Node A", "type": "vless", "server": "127.0.0.1", "port": 65530,
		"uuid": "11111111-1111-4111-8111-111111111111", "network": "tcp", "tls": false,
	}}}}
	cfg, err := BuildControlledConfig(home, filepath.Join(home, "controller.sock"), "controller-secret", definitions, NewSnapshotStore(nil))
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ProviderKey("inline")
	if cfg.Providers[key] == nil || cfg.Providers[key].Count() != 1 {
		t.Fatalf("inline Provider was not initialized: %#v", cfg.Providers[key])
	}
}

func TestBuildControlledConfigCreatesRealityVisionVLESSProvider(t *testing.T) {
	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := base64.RawURLEncoding.EncodeToString(privateKey.PublicKey().Bytes())
	home := shortTempDir(t)
	definitions := []ProviderDefinition{{StableID: "reality", Name: "Reality", Type: "inline", Payload: []map[string]any{{
		"name": "Reality Vision", "type": "vless", "server": "127.0.0.1", "port": 65503,
		"uuid": "33333333-3333-4333-8333-333333333333", "network": "tcp", "tls": true,
		"servername": "www.cloudflare.com", "client-fingerprint": "chrome", "flow": "xtls-rprx-vision",
		"reality-opts": map[string]any{"public-key": publicKey, "short-id": "0123456789abcdef"},
	}}}}
	cfg, err := BuildControlledConfig(home, filepath.Join(home, "controller.sock"), "controller-secret", definitions, NewSnapshotStore(nil))
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ProviderKey("reality")
	provider := cfg.Providers[key]
	if provider == nil || provider.Count() != 1 || provider.Proxies()[0].Type() != C.Vless {
		t.Fatalf("Reality/Vision VLESS Provider was not initialized: %#v", provider)
	}
}
