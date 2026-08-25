package gateway

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppRecoversCorruptProjectionKeyThroughExplicitRotation(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "surgeeb-recovery-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	config := DefaultConfig()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	config.SocksPort = uint16(listener.Addr().(*net.TCPAddr).Port)
	_ = listener.Close()
	store := NewStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(config); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "projection.key")
	if err := os.WriteFile(keyPath, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}

	application, err := New(dir)
	if err != nil {
		t.Fatalf("management recovery mode did not start: %v", err)
	}
	defer application.Close()
	if status := application.Status(); status.State != "recovery" || status.ProjectionCount != 0 || status.SocksAddress != "" {
		t.Fatalf("corrupt key did not keep the data plane closed: %#v", status)
	}
	security := application.SecurityStatus()
	if !security.RecoveryRequired || security.MasterKeyProtected {
		t.Fatalf("unexpected recovery security status: %#v", security)
	}
	if err := application.RotateProjectionKey(); err != nil {
		t.Fatalf("explicit full rotation did not recover the key: %v", err)
	}
	key, err := os.ReadFile(keyPath)
	if err != nil || len(key) != 32 {
		t.Fatalf("recovered key is invalid: length=%d err=%v", len(key), err)
	}
	if status := application.Status(); status.State != "running" || status.SocksAddress == "" {
		t.Fatalf("data plane did not start after recovery: %#v", status)
	}
	security = application.SecurityStatus()
	if security.RecoveryRequired || !security.DataDirectoryProtected || !security.ConfigurationProtected || !security.MasterKeyProtected || !security.ControllerKeyProtected {
		t.Fatalf("security status did not become healthy: %#v", security)
	}
}

func TestProductEventsRedactConfiguredAndRedirectSecrets(t *testing.T) {
	application := &App{
		store: NewStore("/private/surge-external-bridge"),
		config: Config{
			ManagementToken: "management-secret-value",
			Providers: []Provider{{
				URL:     "https://provider.example/subscription-secret?token=configured",
				Headers: map[string][]string{"Authorization": {"Bearer upstream-secret"}},
			}},
		},
	}
	application.addEvent("warn", "refresh https://redirect.example/other-secret?token=redirected Authorization=Bearer upstream-secret management-secret-value")
	events := application.Events()
	if len(events) != 1 {
		t.Fatalf("event count=%d, want 1", len(events))
	}
	for _, forbidden := range []string{"redirect.example", "other-secret", "redirected", "upstream-secret", "management-secret-value"} {
		if strings.Contains(events[0].Message, forbidden) {
			t.Fatalf("event retained %q: %s", forbidden, events[0].Message)
		}
	}
}

func TestProviderSourceSwitchClearsObsoleteSecretsDurably(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("vless://11111111-1111-4111-8111-111111111111@127.0.0.1:65501?type=tcp&security=none#HTTP%20Node\n"))
	}))
	defer upstream.Close()
	dir, err := os.MkdirTemp("/tmp", "surgeeb-source-switch-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	config := DefaultConfig()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	config.SocksPort = uint16(listener.Addr().(*net.TCPAddr).Port)
	_ = listener.Close()
	store := NewStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(config); err != nil {
		t.Fatal(err)
	}
	application, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	provider, err := application.AddProvider(Provider{
		Name: "Switch", Type: "http", URL: upstream.URL + "/subscription-secret", Enabled: true,
		Headers: map[string][]string{"User-Agent": {"private-upstream-secret"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider, err = application.UpdateProvider(provider.StableID, Provider{
		Name: "Switch", Type: "inline", Enabled: true,
		Payload: []map[string]any{{
			"name": "Inline Node", "type": "vless", "server": "127.0.0.1", "port": 65502,
			"uuid": "22222222-2222-4222-8222-222222222222", "network": "tcp", "tls": false,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider.URL != "" || provider.Headers != nil || provider.RefreshSeconds != 0 || provider.SizeLimit != 0 {
		t.Fatalf("HTTP source state survived switch to Inline: %#v", provider)
	}
	encoded, err := os.ReadFile(filepath.Join(dir, "gateway.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"subscription-secret", "private-upstream-secret"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("durable configuration retained obsolete secret %q: %s", forbidden, encoded)
		}
	}

	provider, err = application.UpdateProvider(provider.StableID, Provider{
		Name: "Switch", Type: "http", URL: upstream.URL, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider.Payload != nil || provider.SizeLimit != 16<<20 || provider.RefreshSeconds != 21600 {
		t.Fatalf("Inline source state survived switch to HTTP or defaults were lost: %#v", provider)
	}
}
