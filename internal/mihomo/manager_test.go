package mihomo

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/metacubex/mihomo/transport/socks5"
	"github.com/metacubex/mihomo/tunnel"
)

var sharedRuntime struct {
	once    sync.Once
	manager *Manager
	home    string
	err     error
}

func TestMain(m *testing.M) {
	code := m.Run()
	if sharedRuntime.manager != nil {
		_ = sharedRuntime.manager.Stop()
	}
	if sharedRuntime.home != "" {
		_ = os.RemoveAll(sharedRuntime.home)
	}
	os.Exit(code)
}

func runtimeManager(t *testing.T) *Manager {
	t.Helper()
	sharedRuntime.once.Do(func() {
		sharedRuntime.home, sharedRuntime.err = os.MkdirTemp("/tmp", "surgeeb-")
		if sharedRuntime.err != nil {
			return
		}
		port, err := freePort()
		if err != nil {
			sharedRuntime.err = err
			return
		}
		sharedRuntime.manager, sharedRuntime.err = NewManager(ManagerOptions{
			HomeDir: sharedRuntime.home, ControllerSocket: filepath.Join(sharedRuntime.home, "controller.sock"),
			ControllerSecret: "controller-secret", SocksBind: "127.0.0.1", SocksAdvertise: "127.0.0.1",
			SocksPort: port, MasterKey: []byte("01234567890123456789012345678901"), PollInterval: 10 * time.Millisecond,
		})
		if sharedRuntime.err == nil {
			sharedRuntime.err = sharedRuntime.manager.Start()
		}
	})
	if sharedRuntime.err != nil {
		t.Fatal(sharedRuntime.err)
	}
	return sharedRuntime.manager
}

func TestManagerStartsNoTunCoreThenAuthenticatedListener(t *testing.T) {
	manager := runtimeManager(t)
	status := manager.Status()
	if status.State != "running" || status.ProjectionCount != 0 {
		t.Fatalf("unexpected status: %#v", status)
	}
	connection, err := net.DialTimeout("tcp", status.SocksAddress, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	target := socks5.ParseAddr("example.com:443")
	if _, err := socks5.ClientHandshake(connection, target, socks5.CmdConnect, &socks5.User{Username: "unknown", Password: "wrong"}); err == nil {
		t.Fatal("empty projection listener accepted an unknown identity")
	}
}

func TestNativeProviderRefreshProjectsSuccessAndRetainsLastSuccessOnFailure(t *testing.T) {
	manager := runtimeManager(t)
	content := "vless://11111111-1111-4111-8111-111111111111@127.0.0.1:65501?type=tcp&security=none#Node%20A\n" +
		"vless://22222222-2222-4222-8222-222222222222@127.0.0.1:65502?type=tcp&security=none#Node%20B\n"
	statusCode := http.StatusOK
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(statusCode)
		_, _ = w.Write([]byte(content))
	}))
	defer upstream.Close()
	definitions := []ProviderDefinition{{StableID: "p1", Name: "Airport", Type: "http", URL: upstream.URL, RefreshSeconds: 3600}}
	if err := manager.ApplyProviders(definitions); err != nil {
		t.Fatal(err)
	}
	initial := manager.Snapshot()
	listenerBeforeRefresh := manager.listener
	if got := len(initial.Entries()); got != 2 {
		t.Fatalf("initial projection count=%d, want 2", got)
	}
	initialA := entryNamed(t, initial, "Node A")

	content = "vless://33333333-3333-4333-8333-333333333333@127.0.0.1:65503?type=tcp&security=none#Node%20A\n"
	if err := manager.RefreshProvider("p1"); err != nil {
		t.Fatal(err)
	}
	if manager.listener != listenerBeforeRefresh {
		t.Fatal("native Provider content refresh replaced the product SOCKS listener")
	}
	contracted := manager.Snapshot()
	if got := len(contracted.Entries()); got != 1 {
		t.Fatalf("contracted projection count=%d, want 1", got)
	}
	contractedA := entryNamed(t, contracted, "Node A")
	if initialA.Username != contractedA.Username || initialA.Password != contractedA.Password {
		t.Fatal("same-name node changed deterministic credentials after Provider content replacement")
	}
	stableRevision := contracted.Revision()

	statusCode = http.StatusBadGateway
	if err := manager.RefreshProvider("p1"); err == nil {
		t.Fatal("failed upstream refresh was reported as success")
	}
	afterFailure := manager.Snapshot()
	if len(afterFailure.Entries()) != 1 || afterFailure.Revision() != stableRevision {
		t.Fatalf("failed refresh replaced last successful projection: count=%d revision=%s want=%s", len(afterFailure.Entries()), afterFailure.Revision(), stableRevision)
	}
	nextRefresh, lastError := manager.ProviderState("p1")
	if nextRefresh.IsZero() || lastError == "" {
		t.Fatalf("failed refresh state missing: next=%v error=%q", nextRefresh, lastError)
	}
	statusCode = http.StatusOK
	if err := manager.RefreshProvider("p1"); err != nil {
		t.Fatal(err)
	}
	if _, lastError = manager.ProviderState("p1"); lastError != "" {
		t.Fatalf("successful refresh retained stale error %q", lastError)
	}
}

func TestApplyProvidersValidatesProjectionBeforeReplacingCoreTopology(t *testing.T) {
	manager := runtimeManager(t)
	definition := ProviderDefinition{StableID: "transaction", Name: "Transaction", Type: "inline", Payload: []map[string]any{{
		"name": "Current Node", "type": "vless", "server": "127.0.0.1", "port": 65530,
		"uuid": "11111111-1111-4111-8111-111111111111", "network": "tcp", "tls": false,
	}}}
	if err := manager.ApplyProviders([]ProviderDefinition{definition}); err != nil {
		t.Fatal(err)
	}
	beforeSnapshot := manager.Snapshot()
	beforeListener := manager.listener
	key, _ := ProviderKey(definition.StableID)
	beforeProvider := tunnel.Providers()[key]
	if beforeProvider == nil {
		t.Fatal("current Provider topology is missing")
	}
	candidate := definition
	candidate.IncludeName = "("
	candidate.Payload = []map[string]any{{
		"name": "Uncommitted Node", "type": "vless", "server": "127.0.0.1", "port": 65531,
		"uuid": "22222222-2222-4222-8222-222222222222", "network": "tcp", "tls": false,
	}}
	if err := manager.ApplyProviders([]ProviderDefinition{candidate}); err == nil {
		t.Fatal("invalid Projection candidate was applied")
	}
	afterSnapshot := manager.Snapshot()
	if afterSnapshot.Revision() != beforeSnapshot.Revision() || len(afterSnapshot.Entries()) != 1 || afterSnapshot.Entries()[0].ProxyName != "Current Node" {
		t.Fatalf("failed candidate changed the published Snapshot: %#v", afterSnapshot.Entries())
	}
	if manager.listener != beforeListener || tunnel.Providers()[key] != beforeProvider {
		t.Fatal("failed candidate replaced the SOCKS listener or Mihomo Provider topology")
	}
}

func TestNativeHTTPProviderParsesURIBase64AndClashYAML(t *testing.T) {
	manager := runtimeManager(t)
	listenerBeforeApply := manager.listener
	controllerBeforeApply, err := os.Stat(filepath.Join(sharedRuntime.home, "controller.sock"))
	if err != nil {
		t.Fatal(err)
	}
	uri := "vless://11111111-1111-4111-8111-111111111111@127.0.0.1:65501?type=tcp&security=none#URI%20Node\n"
	cases := []struct{ name, content string }{
		{"URI", uri},
		{"Base64 URI", base64.StdEncoding.EncodeToString([]byte(uri))},
		{"Clash YAML", "port: 7890\ntun:\n  enable: true\ndns:\n  enable: true\n  listen: 0.0.0.0:53\nlisteners:\n  - name: forbidden\n    type: socks\n    port: 7891\nproxies:\n  - name: YAML Node\n    type: vless\n    server: 127.0.0.1\n    port: 65501\n    uuid: 11111111-1111-4111-8111-111111111111\n    network: tcp\n    tls: false\n"},
	}
	for index, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(item.content)) }))
			defer upstream.Close()
			definition := ProviderDefinition{StableID: fmt.Sprintf("format-%d", index), Name: "Provider", Type: "http", URL: upstream.URL, RefreshSeconds: 3600}
			if err := manager.ApplyProviders([]ProviderDefinition{definition}); err != nil {
				t.Fatal(err)
			}
			if got := len(manager.Snapshot().Entries()); got != 1 {
				t.Fatalf("projected %d nodes, want 1", got)
			}
		})
	}
	controllerAfterApply, err := os.Stat(filepath.Join(sharedRuntime.home, "controller.sock"))
	if err != nil {
		t.Fatal(err)
	}
	if manager.listener != listenerBeforeApply || !os.SameFile(controllerBeforeApply, controllerAfterApply) {
		t.Fatal("Provider definition ApplyConfig recreated a fixed listener or private Controller")
	}
}

func TestNativeHTTPProviderDoesNotForwardSensitiveHeadersToUnrelatedRedirectHost(t *testing.T) {
	manager := runtimeManager(t)
	received := make(chan http.Header, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Clone()
		_, _ = w.Write([]byte("vless://11111111-1111-4111-8111-111111111111@127.0.0.1:65501?type=tcp&security=none#Redirect%20Node\n"))
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer origin.Close()
	// A hostname change is the trust boundary used by Mihomo's HTTP client.
	// Both names still resolve to this local test process.
	originURL := strings.Replace(origin.URL, "127.0.0.1", "localhost", 1) + "/subscription?token=subscription-secret"
	definition := ProviderDefinition{
		StableID: "redirect-header", Name: "Redirect Header", Type: "http", URL: originURL, RefreshSeconds: 3600,
		Headers: map[string][]string{"Authorization": {"Bearer subscription-secret"}, "Cookie": {"session=subscription-secret"}},
	}
	if err := manager.ApplyProviders([]ProviderDefinition{definition}); err != nil {
		t.Fatal(err)
	}
	select {
	case header := <-received:
		if header.Get("Authorization") != "" || header.Get("Cookie") != "" {
			t.Fatalf("sensitive Provider Headers crossed an unrelated redirect host: %#v", header)
		}
		if referer := header.Get("Referer"); referer == "" || strings.Contains(referer, "subscription-secret") || strings.Contains(referer, "/subscription") {
			t.Fatalf("Provider URL credentials crossed a redirect in Referer %q", referer)
		}
	case <-time.After(time.Second):
		t.Fatal("redirect target was not requested")
	}
}

func TestScheduledRefreshUsesSerializedNativeProviderUpdate(t *testing.T) {
	manager := runtimeManager(t)
	var contentMu sync.RWMutex
	content := "vless://11111111-1111-4111-8111-111111111111@127.0.0.1:65501?type=tcp&security=none#Scheduled%20A\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		contentMu.RLock()
		defer contentMu.RUnlock()
		_, _ = w.Write([]byte(content))
	}))
	defer upstream.Close()
	definition := ProviderDefinition{StableID: "scheduled", Name: "Scheduled", Type: "http", URL: upstream.URL, RefreshSeconds: 1}
	if err := manager.ApplyProviders([]ProviderDefinition{definition}); err != nil {
		t.Fatal(err)
	}
	contentMu.Lock()
	content = "vless://22222222-2222-4222-8222-222222222222@127.0.0.1:65502?type=tcp&security=none#Scheduled%20B\n"
	contentMu.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if entries := manager.Snapshot().Entries(); len(entries) == 1 && entries[0].ProxyName == "Scheduled B" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("scheduled native Update did not publish the new Provider content: %#v", manager.Snapshot().Entries())
}

func TestProjectionSettingsRotateCredentialsAndRebindListenerWithoutCoreRestart(t *testing.T) {
	manager := runtimeManager(t)
	definitions := []ProviderDefinition{{StableID: "inline", Name: "Inline", Type: "inline", Payload: []map[string]any{{
		"name": "Node A", "type": "vless", "server": "127.0.0.1", "port": 65530,
		"uuid": "11111111-1111-4111-8111-111111111111", "network": "tcp", "tls": false,
	}}}}
	if err := manager.ApplyProviders(definitions); err != nil {
		t.Fatal(err)
	}
	firstPort := manager.options.SocksPort
	before := manager.Snapshot().Entries()[0]
	startedAt := manager.Status().StartedAt
	rotatedKey := []byte("abcdefghijklmnopqrstuvwxyzABCDEF")
	if err := manager.ApplyProjectionSettings("127.0.0.1", "127.0.0.1", firstPort, true, rotatedKey); err != nil {
		t.Fatal(err)
	}
	afterRotation := manager.Snapshot().Entries()[0]
	if before.Username == afterRotation.Username || before.Password == afterRotation.Password {
		t.Fatal("Projection key rotation retained old credentials")
	}
	if manager.Status().StartedAt != startedAt {
		t.Fatal("projection-only change restarted Embedded Mihomo")
	}
	secondPort, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.ApplyProjectionSettings("127.0.0.1", "127.0.0.1", secondPort, true, rotatedKey); err != nil {
		t.Fatal(err)
	}
	if got := manager.Status().SocksAddress; got != net.JoinHostPort("127.0.0.1", fmt.Sprint(secondPort)) {
		t.Fatalf("SOCKS listener did not rebind: %s", got)
	}
	if connection, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(firstPort)), 100*time.Millisecond); err == nil {
		_ = connection.Close()
		t.Fatal("old SOCKS listener still accepted connections after rebind")
	}
}

func TestProviderCacheSurvivesProcessRestart(t *testing.T) {
	if os.Getenv("SURGEEB_CACHE_HELPER") != "" {
		runProviderCacheHelper(t)
		return
	}
	dir, err := os.MkdirTemp("/tmp", "surgeeb-cache-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	content := "vless://11111111-1111-4111-8111-111111111111@127.0.0.1:65501?type=tcp&security=none#Cached%20Node\n"
	status := http.StatusOK
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(content))
	}))
	defer upstream.Close()
	run := func(phase string) {
		command := exec.Command(os.Args[0], "-test.run=^TestProviderCacheSurvivesProcessRestart$", "-test.v")
		command.Env = append(os.Environ(), "SURGEEB_CACHE_HELPER="+phase, "SURGEEB_CACHE_HOME="+dir, "SURGEEB_CACHE_URL="+upstream.URL)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("cache helper %s failed: %v\n%s", phase, err, output)
		}
	}
	run("seed")
	status = http.StatusBadGateway
	run("restore")
}

func TestManagerRecoversFromSOCKSBindFailureWithoutCoreRestart(t *testing.T) {
	if os.Getenv("SURGEEB_RECOVERY_HELPER") != "" {
		runManagerRecoveryHelper(t)
		return
	}
	command := exec.Command(os.Args[0], "-test.run=^TestManagerRecoversFromSOCKSBindFailureWithoutCoreRestart$", "-test.v")
	command.Env = append(os.Environ(), "SURGEEB_RECOVERY_HELPER=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("manager recovery helper failed: %v\n%s", err, output)
	}
}

func runManagerRecoveryHelper(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "surgeeb-recovery-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	blocker, udpBlocker, err := reserveSOCKSPort()
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(blocker.Addr().(*net.TCPAddr).Port)
	manager, err := NewManager(ManagerOptions{
		HomeDir: home, ControllerSocket: filepath.Join(home, "controller.sock"), ControllerSecret: "controller-secret",
		SocksBind: "127.0.0.1", SocksAdvertise: "127.0.0.1", SocksPort: port,
		MasterKey: []byte("01234567890123456789012345678901"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()
	if err := manager.Start(); err == nil {
		t.Fatal("occupied SOCKS port unexpectedly started")
	}
	if manager.Status().State != "error" {
		t.Fatalf("failed start state = %s", manager.Status().State)
	}
	if _, err := os.Stat(filepath.Join(home, "controller.sock")); err != nil {
		t.Fatalf("private Controller was not retained for repair: %v", err)
	}
	if err := blocker.Close(); err != nil {
		t.Fatal(err)
	}
	if err := udpBlocker.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.ConfigureProjectionWhenStopped("127.0.0.1", "127.0.0.1", port, true, []byte("01234567890123456789012345678901")); err != nil {
		t.Fatal(err)
	}
	if err := manager.StartWithProviders(nil); err != nil {
		t.Fatal(err)
	}
	if manager.Status().State != "running" {
		t.Fatalf("recovered state = %s", manager.Status().State)
	}
}

func runProviderCacheHelper(t *testing.T) {
	home, upstream := os.Getenv("SURGEEB_CACHE_HOME"), os.Getenv("SURGEEB_CACHE_URL")
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(ManagerOptions{
		HomeDir: home, ControllerSocket: filepath.Join(home, "controller.sock"), ControllerSecret: "controller-secret",
		SocksBind: "127.0.0.1", SocksAdvertise: "127.0.0.1", SocksPort: port,
		MasterKey: []byte("01234567890123456789012345678901"),
		Providers: []ProviderDefinition{{StableID: "cache", Name: "Cache", Type: "http", URL: upstream, RefreshSeconds: 3600}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()
	entries := manager.Snapshot().Entries()
	if len(entries) != 1 || entries[0].ProxyName != "Cached Node" {
		t.Fatalf("phase %s restored %#v", os.Getenv("SURGEEB_CACHE_HELPER"), entries)
	}
	if !PrivateTreeProtected(home) {
		t.Fatalf("phase %s left Mihomo Provider cache or Controller permissions unprotected", os.Getenv("SURGEEB_CACHE_HELPER"))
	}
}

func entryNamed(t *testing.T, snapshot *Snapshot, name string) Entry {
	t.Helper()
	for _, entry := range snapshot.Entries() {
		if entry.ProxyName == name {
			return entry
		}
	}
	t.Fatalf("entry %q not found", name)
	return Entry{}
}

func freePort() (uint16, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return uint16(port), nil
}

// reserveSOCKSPort prevents a process using the independent UDP namespace from
// taking the TCP-selected port between the intentional bind failure and the
// recovery attempt. The Manager still fails on TCP first; both reservations are
// released before retrying the real dual-protocol listener.
func reserveSOCKSPort() (net.Listener, net.PacketConn, error) {
	for range 100 {
		tcp, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, nil, err
		}
		udp, err := net.ListenPacket("udp", tcp.Addr().String())
		if err == nil {
			return tcp, udp, nil
		}
		_ = tcp.Close()
	}
	return nil, nil, fmt.Errorf("reserve matching TCP and UDP test port")
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "surgeeb-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
