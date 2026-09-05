package gateway

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	M "github.com/ssfun/surge-external-bridge/internal/mihomo"
)

func newRunningTestApp(t *testing.T) *App {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "seb-policy-path-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	config := mustDefaultConfig(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	config.SocksPort = uint16(listener.Addr().(*net.TCPAddr).Port)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
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
	t.Cleanup(func() { _ = application.Close() })
	return application
}

func TestSettingsPatchPreservesTokenRotatedAfterStaleSettingsRead(t *testing.T) {
	application := newRunningTestApp(t)
	settings := application.Config().Settings()
	settings.PolicyToken = "original-policy-token-1234"
	if err := application.UpdateSettings(settings); err != nil {
		t.Fatal(err)
	}
	stale := application.Config().Settings()
	rotated, err := application.RegeneratePolicyPathToken(DefaultPolicyPathID)
	if err != nil {
		t.Fatal(err)
	}
	stale.NodeTestTimeout++
	if err := application.UpdateSettingsPatch(stale, nil, nil); err != nil {
		t.Fatal(err)
	}
	current := application.Config().Settings()
	if current.PolicyToken != rotated.Token {
		t.Fatalf("settings patch restored stale Policy Token %q; want rotated %q", current.PolicyToken, rotated.Token)
	}
	if current.NodeTestTimeout != stale.NodeTestTimeout {
		t.Fatalf("non-secret settings patch was not applied: timeout=%d, want %d", current.NodeTestTimeout, stale.NodeTestTimeout)
	}
}

func TestPolicyPathUpdateWithoutTokenPreservesConcurrentRotation(t *testing.T) {
	application := newRunningTestApp(t)
	settings := application.Config().Settings()
	settings.PolicyToken = "original-policy-token-1234"
	if err := application.UpdateSettings(settings); err != nil {
		t.Fatal(err)
	}
	stale, ok := application.PolicyPath(DefaultPolicyPathID)
	if !ok {
		t.Fatal("default Policy Path not found")
	}
	rotated, err := application.RegeneratePolicyPathToken(DefaultPolicyPathID)
	if err != nil {
		t.Fatal(err)
	}
	stale.Name = "Renamed after stale read"
	stale.Token = ""
	updated, err := application.UpdatePolicyPath(DefaultPolicyPathID, stale)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Token != rotated.Token {
		t.Fatalf("Policy Path update restored stale Token %q; want rotated %q", updated.Token, rotated.Token)
	}
	if updated.Name != stale.Name {
		t.Fatalf("Policy Path name=%q, want %q", updated.Name, stale.Name)
	}
}

func TestPolicyPathPublicationWaitsForCommit(t *testing.T) {
	application := newRunningTestApp(t)
	application.publicationMu.Lock()
	locked := true
	defer func() {
		if locked {
			application.publicationMu.Unlock()
		}
	}()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, _, err := application.ProxiesForPath(DefaultPolicyPathID)
		done <- err
	}()
	<-started
	select {
	case err := <-done:
		t.Fatalf("Policy Path publication escaped an in-progress config apply: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	application.publicationMu.Unlock()
	locked = false
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Policy Path publication did not resume after config apply completed")
	}
}

func TestPolicyPathTokenAuthenticationUsesPostApplyConfig(t *testing.T) {
	application := newRunningTestApp(t)
	settings := application.Config().Settings()
	settings.PolicyToken = "original-policy-token-1234"
	if err := application.UpdateSettings(settings); err != nil {
		t.Fatal(err)
	}

	application.publicationMu.Lock()
	locked := true
	defer func() {
		if locked {
			application.publicationMu.Unlock()
		}
	}()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, _, err := application.ProxiesForToken("original-policy-token-1234")
		done <- err
	}()
	<-started
	select {
	case err := <-done:
		t.Fatalf("Policy Token authentication escaped an in-progress config apply: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	application.mu.Lock()
	application.config.PolicyPaths[0].Token = "rotated-policy-token-1234"
	application.mu.Unlock()
	application.publicationMu.Unlock()
	locked = false

	select {
	case err := <-done:
		if !errors.Is(err, ErrInvalidPolicyToken) {
			t.Fatalf("old Policy Token error=%v, want ErrInvalidPolicyToken", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Policy Token authentication did not resume after config apply completed")
	}
	if _, _, err := application.ProxiesForToken("rotated-policy-token-1234"); err != nil {
		t.Fatalf("rotated Policy Token was rejected: %v", err)
	}
}

func TestConfiguredProjectionIdentityMatchesAcrossIndependentApps(t *testing.T) {
	waitEntry := func(application *App) M.Entry {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if entries := application.Snapshot().Entries(); len(entries) == 1 {
				return entries[0]
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("projection did not become ready: %#v", application.Status())
		return M.Entry{}
	}
	newApp := func() *App {
		t.Helper()
		dir, err := os.MkdirTemp("/tmp", "surgeeb-deterministic-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		config := mustDefaultConfig(t)
		config.ProjectionKey = "shared-projection-key-for-all-devices"
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		config.SocksPort = uint16(listener.Addr().(*net.TCPAddr).Port)
		_ = listener.Close()
		config.Providers = []Provider{{
			Name: "Shared Provider", Type: "inline", Enabled: true,
			Payload: []map[string]any{{
				"name": "Shared Node", "type": "vless", "server": "127.0.0.1", "port": 65502,
				"uuid": "22222222-2222-4222-8222-222222222222", "network": "tcp", "tls": false,
			}},
		}}
		assignProviderIDs(&config)
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
		t.Cleanup(func() { _ = application.Close() })
		return application
	}

	first := newApp()
	firstProvider := first.Config().Providers[0]
	encoded, err := os.ReadFile(filepath.Join(first.DataDir(), "gateway.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"stable_id"`) {
		t.Fatalf("gateway.json persisted a derived Provider ID: %s", encoded)
	}
	firstEntry := waitEntry(first)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := newApp()
	secondProvider := second.Config().Providers[0]
	if firstProvider.StableID == "" || firstProvider.StableID != secondProvider.StableID {
		t.Fatalf("Provider names did not produce the same internal ID: %q != %q", firstProvider.StableID, secondProvider.StableID)
	}
	secondEntry := waitEntry(second)
	if firstEntry.Username != secondEntry.Username || firstEntry.Password != secondEntry.Password || firstEntry.PublicID != secondEntry.PublicID {
		t.Fatalf("same open configuration produced different identities: %#v != %#v", firstEntry, secondEntry)
	}
	settings := second.Config().Settings()
	settings.ProjectionKey = "different-projection-key-for-device"
	if err := second.UpdateSettings(settings); err != nil {
		t.Fatal(err)
	}
	changed := waitEntry(second)
	if changed.Username == firstEntry.Username || changed.Password == firstEntry.Password {
		t.Fatal("changing projection_key retained the old identity")
	}
	if changed.PublicID != firstEntry.PublicID {
		t.Fatal("changing projection_key changed the name-derived public node ID")
	}
}

func TestStoppedSettingsStartFailureRestoresPersistedManagerInputs(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "seb-settings-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	config := mustDefaultConfig(t)
	oldPortReservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	config.SocksPort = uint16(oldPortReservation.Addr().(*net.TCPAddr).Port)
	if err := oldPortReservation.Close(); err != nil {
		t.Fatal(err)
	}
	config.Providers = []Provider{{
		Name: "Persisted", Type: "inline", Enabled: true,
		Payload: []map[string]any{{
			"name": "Persisted Node", "type": "vless", "server": "127.0.0.1", "port": 65502,
			"uuid": "22222222-2222-4222-8222-222222222222", "network": "tcp", "tls": false,
		}},
	}}
	assignProviderIDs(&config)
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
	before := application.Snapshot().Entries()
	if len(before) != 1 {
		t.Fatalf("initial projection entries=%d, want 1", len(before))
	}
	if err := application.manager.Stop(); err != nil {
		t.Fatal(err)
	}
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	settings := config.Settings()
	settings.SocksPort = uint16(occupied.Addr().(*net.TCPAddr).Port)
	settings.SocksHost = "candidate.surge.eb"
	settings.ProjectionKey = "candidate-projection-key-value"
	if err := application.UpdateSettings(settings); err == nil {
		t.Fatal("occupied candidate SOCKS port unexpectedly started")
	}
	if state := application.Status().State; state != "stopped" {
		t.Fatalf("failed stopped update left manager state=%s, want stopped", state)
	}
	if persisted := application.Config(); persisted.SocksPort != config.SocksPort || persisted.SocksHost != config.SocksHost || persisted.ProjectionKey != config.ProjectionKey {
		t.Fatalf("failed update changed persisted app config: %#v", persisted.Settings())
	}
	if err := application.manager.Start(); err != nil {
		t.Fatal(err)
	}
	if address := application.Status().SocksAddress; address != net.JoinHostPort(config.SocksBind, fmt.Sprint(config.SocksPort)) {
		t.Fatalf("restored manager started at %s, want persisted address", address)
	}
	after := application.Snapshot().Entries()
	if len(after) != 1 || after[0].Username != before[0].Username || after[0].Password != before[0].Password || after[0].SocksHost != config.SocksHost {
		t.Fatalf("restored projection differs from persisted inputs: before=%#v after=%#v", before, after)
	}
}

func TestNewReconcilesOrphanedProviderUploads(t *testing.T) {
	dir := t.TempDir()
	config := mustDefaultConfig(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	config.SocksPort = uint16(listener.Addr().(*net.TCPAddr).Port)
	_ = listener.Close()
	config.Providers = []Provider{{
		Name: "Active Upload", Type: "file", FilePath: "uploads/provider-active.yaml", Enabled: false,
	}}
	uploads := filepath.Join(dir, "mihomo", "uploads")
	if err := os.MkdirAll(uploads, 0o700); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(uploads, "provider-active.yaml")
	orphan := filepath.Join(uploads, "provider-orphan.yaml")
	for path, name := range map[string]string{active: "Active", orphan: "Orphan"} {
		content := "proxies:\n  - name: " + name + "\n    type: direct\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
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
	if _, err := os.Stat(active); err != nil {
		t.Fatalf("startup removed the configured upload: %v", err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("startup did not remove the orphaned upload: %v", err)
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
	config := mustDefaultConfig(t)
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
