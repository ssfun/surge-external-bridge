package mihomo

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	C "github.com/metacubex/mihomo/constant"
	P "github.com/metacubex/mihomo/constant/provider"
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

func TestControlledProvidersFilterDialerProxyNodesAcrossSourcesAndRefresh(t *testing.T) {
	source := func(safeName, chainedName string) string {
		return "proxies:\n" +
			"  - name: " + safeName + "\n    type: socks5\n    server: 127.0.0.1\n    port: 1080\n" +
			"  - name: " + chainedName + "\n    type: socks5\n    server: 127.0.0.1\n    port: 1081\n    dialer-proxy: 链式代理规则\n"
	}
	inlinePayload := []map[string]any{
		{"name": "Inline Safe", "type": "socks5", "server": "127.0.0.1", "port": 1080},
		{"name": "Inline Chained", "type": "socks5", "server": "127.0.0.1", "port": 1081, "dialer-proxy": "链式代理规则"},
	}

	var httpSource atomic.Value
	httpSource.Store(source("HTTP Safe", "HTTP Chained"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(httpSource.Load().(string)))
	}))
	defer server.Close()

	home := shortTempDir(t)
	filePath := filepath.Join(home, "filtered-provider.yaml")
	if err := os.WriteFile(filePath, []byte(source("File Safe", "File Chained")), 0o600); err != nil {
		t.Fatal(err)
	}
	definitions := []ProviderDefinition{
		{StableID: "inline-filter", Name: "Inline", Type: "inline", Payload: inlinePayload},
		{StableID: "file-filter", Name: "File", Type: "file", FilePath: filePath},
		{StableID: "http-filter", Name: "HTTP", Type: "http", URL: server.URL},
	}
	cfg, err := BuildControlledConfig(home, filepath.Join(home, "controller.sock"), "controller-secret", definitions, NewSnapshotStore(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeProviders(cfg.Providers); err != nil {
		t.Fatal(err)
	}
	defer closeProviders(cfg.Providers)

	for _, definition := range definitions {
		key, _ := ProviderKey(definition.StableID)
		assertFilteredProvider(t, cfg.Providers[key], definition.Name+" Safe", definition.Name+" Chained")
	}

	if err := os.WriteFile(filePath, []byte(source("File Safe 2", "File Chained 2")), 0o600); err != nil {
		t.Fatal(err)
	}
	fileKey, _ := ProviderKey("file-filter")
	if err := cfg.Providers[fileKey].Update(); err != nil {
		t.Fatal(err)
	}
	assertFilteredProvider(t, cfg.Providers[fileKey], "File Safe 2", "File Chained 2")

	httpSource.Store(source("HTTP Safe 2", "HTTP Chained 2"))
	httpKey, _ := ProviderKey("http-filter")
	if err := cfg.Providers[httpKey].Update(); err != nil {
		t.Fatal(err)
	}
	assertFilteredProvider(t, cfg.Providers[httpKey], "HTTP Safe 2", "HTTP Chained 2")
}

func TestProviderStateMarksAllDialerProxyNodesUnavailable(t *testing.T) {
	home := shortTempDir(t)
	definitions := []ProviderDefinition{{StableID: "all-filtered", Name: "All Filtered", Type: "inline", Payload: []map[string]any{
		{"name": "Only Chained", "type": "socks5", "server": "127.0.0.1", "port": 1081, "dialer-proxy": "链式代理规则"},
	}}}
	cfg, err := BuildControlledConfig(home, filepath.Join(home, "controller.sock"), "controller-secret", definitions, NewSnapshotStore(nil))
	if err != nil {
		t.Fatal(err)
	}
	manager := &Manager{config: cfg, nextPull: map[string]time.Time{}, providerErrors: map[string]string{}}
	_, lastError := manager.ProviderState("all-filtered")
	if !strings.Contains(lastError, "全部 1 个节点") {
		t.Fatalf("all-filtered Provider state = %q", lastError)
	}
	views, err := ProviderViews(cfg, definitions)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || len(views[0].Proxies) != 0 {
		t.Fatalf("all-filtered Provider remained projectable: %#v", views)
	}
}

func TestControlledProviderFiltersWireGuardDialerProxyBeforeUse(t *testing.T) {
	// Mihomo's log level is process-global and unsynchronised. Initialize the
	// controlled core before constructing a WireGuard adapter whose shutdown
	// workers may emit deferred logs after Close returns.
	_ = runtimeManager(t)
	home := shortTempDir(t)
	definitions := []ProviderDefinition{{StableID: "warp-filter", Name: "WARP", Type: "inline", Payload: []map[string]any{
		{"name": "Safe", "type": "direct"},
		{
			"name": "WARP Chained", "type": "wireguard", "server": "162.159.192.1", "port": 2480,
			"ip": "172.16.0.2", "private-key": "eCtXsJZ27+4PbhDkHnB923tkUn2Gj59wZw5wFA75MnU=",
			"public-key": "Cr8hWlKvtDt7nrvf+f0brNQQzabAqrjfBvas9pmowjo=", "udp": true,
			"dialer-proxy": "链式代理规则",
		},
	}}}
	cfg, err := BuildControlledConfig(home, filepath.Join(home, "controller.sock"), "controller-secret", definitions, NewSnapshotStore(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer closeProviders(cfg.Providers)
	key, _ := ProviderKey("warp-filter")
	report := providerFilterReport(cfg.Providers[key])
	if cfg.Providers[key].Count() != 1 || report.FilteredCount() != 1 || report.FilteredNodes[0].Name != "WARP Chained" {
		t.Fatalf("WireGuard dialer-proxy was not filtered before use: %#v", report)
	}
}

func assertFilteredProvider(t *testing.T, provider P.ProxyProvider, safeName, filteredName string) {
	t.Helper()
	report := providerFilterReport(provider)
	if report.SourceCount != 2 || report.AvailableCount != 1 || report.FilteredCount() != 1 {
		t.Fatalf("filter report = %#v", report)
	}
	if provider.Count() != 1 || provider.Proxies()[0].Name() != safeName {
		t.Fatalf("available proxies = %#v, want %q", provider.Proxies(), safeName)
	}
	if report.FilteredNodes[0].Name != filteredName || report.FilteredNodes[0].DialerProxy != "链式代理规则" {
		t.Fatalf("filtered nodes = %#v", report.FilteredNodes)
	}
	encoded, err := json.Marshal(provider)
	if err != nil {
		t.Fatal(err)
	}
	var runtime struct {
		Proxies       []map[string]any `json:"proxies"`
		FilteredCount int              `json:"filteredCount"`
	}
	if err := json.Unmarshal(encoded, &runtime); err != nil {
		t.Fatal(err)
	}
	if len(runtime.Proxies) != 1 || runtime.FilteredCount != 1 {
		t.Fatalf("Controller runtime leaked filtered proxies: %s", encoded)
	}
}

func TestProviderMappingLeavesHealthCheckExecutionToManager(t *testing.T) {
	home := shortTempDir(t)
	mapping, err := providerMapping(home, ProviderDefinition{
		StableID: "health", Name: "Health", Type: "inline",
		Payload:     []map[string]any{{"name": "Direct", "type": "direct"}},
		HealthCheck: true, HealthCheckURL: "https://example.com/generate_204", HealthCheckSeconds: 300,
		HealthCheckTimeout: 5000, HealthCheckLazy: true, ExpectedStatus: "200-399",
	})
	if err != nil {
		t.Fatal(err)
	}
	health, ok := mapping["health-check"].(map[string]any)
	if !ok {
		t.Fatalf("health-check mapping = %#v", mapping["health-check"])
	}
	if enabled, _ := health["enable"].(bool); enabled {
		t.Fatal("Mihomo automatic health-check ticker remained enabled")
	}
	if health["url"] != "https://example.com/generate_204" || health["timeout"] != 5000 || health["expected-status"] != "200-399" {
		t.Fatalf("Manager-owned health check lost Mihomo execution settings: %#v", health)
	}
	if _, exists := health["interval"]; exists {
		t.Fatalf("Mihomo automatic health-check interval remained configured: %#v", health)
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
