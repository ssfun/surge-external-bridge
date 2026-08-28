package mihomo

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metacubex/mihomo/component/resolver"
	C "github.com/metacubex/mihomo/constant"
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

func TestHTTPProviderHostsRefreshIsTransactionalAndKeepsOriginalServer(t *testing.T) {
	manager := runtimeManager(t)
	content := "proxies:\n  - name: Host Node\n    type: socks5\n    server: edge.example.com\n    port: 1080\nhosts:\n  edge.example.com: 192.0.2.10\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(content))
	}))
	defer upstream.Close()
	definition := ProviderDefinition{StableID: "hosts-http", Name: "Hosts HTTP", Type: "http", URL: upstream.URL, RefreshSeconds: 3600}
	if err := manager.ApplyProviders([]ProviderDefinition{definition}); err != nil {
		t.Fatal(err)
	}
	initial := manager.Snapshot()
	if len(initial.Entries()) != 1 || !strings.Contains(initial.Entries()[0].Proxy.Addr(), "edge.example.com") {
		t.Fatalf("original proxy server was rewritten: %#v", initial.Entries())
	}
	assertRuntimeHostIP(t, manager, definition.StableID, "edge.example.com", "192.0.2.10")
	resolverBeforeRefresh := resolver.ProxyServerHostResolver
	stableRevision := initial.Revision()

	content = "proxies:\n  - name: Rejected Host Node\n    type: socks5\n    server: edge.example.com\n    port: 1080\nhosts:\n  edge.example.com: [192.0.2.20, invalid.example.com]\n"
	if err := manager.RefreshProvider(definition.StableID); err == nil {
		t.Fatal("invalid matched hosts refresh was accepted")
	}
	if got := manager.Snapshot(); got.Revision() != stableRevision || got.Entries()[0].ProxyName != "Host Node" {
		t.Fatalf("invalid hosts refresh replaced the last projection: %#v", got.Entries())
	}
	cache, err := os.ReadFile(C.Path.GetPathByHash("proxies", definition.URL))
	if err != nil || !strings.Contains(string(cache), "192.0.2.10") || strings.Contains(string(cache), "invalid.example.com") {
		t.Fatalf("invalid hosts refresh replaced the accepted restart cache: %v %q", err, cache)
	}
	assertRuntimeHostIP(t, manager, definition.StableID, "edge.example.com", "192.0.2.10")

	content = "proxies:\n  - name: Host Node 2\n    type: socks5\n    server: edge.example.com\n    port: 1080\nhosts:\n  edge.example.com: 192.0.2.20\n"
	if err := manager.RefreshProvider(definition.StableID); err != nil {
		t.Fatal(err)
	}
	if resolver.ProxyServerHostResolver != resolverBeforeRefresh {
		t.Fatal("Provider refresh replaced the live proxy-server resolver instead of swapping its immutable hosts snapshot")
	}
	if got := manager.Snapshot(); len(got.Entries()) != 1 || got.Entries()[0].ProxyName != "Host Node 2" {
		t.Fatalf("valid hosts recovery was not projected: %#v", got.Entries())
	}
	assertRuntimeHostIP(t, manager, definition.StableID, "edge.example.com", "192.0.2.20")

	conflicting := ProviderDefinition{
		StableID: "hosts-conflict", Name: "Hosts Conflict", Type: "inline",
		Payload: []map[string]any{{"name": "Conflict", "type": "socks5", "server": "edge.example.com", "port": 1081}},
		Hosts:   map[string]any{"edge.example.com": "192.0.2.30"},
	}
	beforeConflict := manager.Snapshot().Revision()
	if err := manager.ApplyProviders([]ProviderDefinition{definition, conflicting}); err == nil {
		t.Fatal("conflicting Provider hosts were accepted")
	}
	if manager.Snapshot().Revision() != beforeConflict {
		t.Fatal("conflicting Provider hosts replaced the live projection")
	}
	assertRuntimeHostIP(t, manager, definition.StableID, "edge.example.com", "192.0.2.20")
}

func assertRuntimeHostIP(t *testing.T, manager *Manager, stableID, host, want string) {
	t.Helper()
	if count := manager.ProviderHostsCount(stableID); count != 1 {
		t.Fatalf("Provider hosts count=%d, want 1", count)
	}
	hostResolver, ok := resolver.ProxyServerHostResolver.(*providerHostResolver)
	if !ok {
		t.Fatalf("proxy server resolver = %T", resolver.ProxyServerHostResolver)
	}
	target, err := hostResolver.resolve(host)
	if err != nil || len(target.ips) != 1 || target.ips[0] != netip.MustParseAddr(want) {
		t.Fatalf("runtime host target = %#v, %v, want %s", target, err, want)
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

func TestManagerOwnedProviderHealthChecksDoNotRaceRefresh(t *testing.T) {
	manager := runtimeManager(t)
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer health.Close()
	var contentMu sync.RWMutex
	content := "proxies:\n  - name: Direct 0\n    type: direct\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		contentMu.RLock()
		defer contentMu.RUnlock()
		_, _ = w.Write([]byte(content))
	}))
	defer upstream.Close()
	definition := ProviderDefinition{
		StableID: "refresh-health-race", Name: "Refresh Health Race", Type: "http", URL: upstream.URL, RefreshSeconds: 3600,
		HealthCheck: true, HealthCheckURL: health.URL, HealthCheckSeconds: 60, HealthCheckTimeout: 1000, ExpectedStatus: "200-399",
	}
	if err := manager.ApplyProviders([]ProviderDefinition{definition}); err != nil {
		t.Fatal(err)
	}
	key, _ := ProviderKey(definition.StableID)
	if got := manager.config.Providers[key].HealthCheckURL(); got != health.URL {
		t.Fatalf("disabling Mihomo's automatic ticker lost health-check URL %q", got)
	}
	for index := 1; index <= 20; index++ {
		contentMu.Lock()
		content = fmt.Sprintf("proxies:\n  - name: Direct %d\n    type: direct\n", index)
		contentMu.Unlock()
		start := make(chan struct{})
		errorsFound := make(chan error, 2)
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			errorsFound <- manager.RefreshProvider(definition.StableID)
		}()
		go func() {
			defer wait.Done()
			<-start
			errorsFound <- manager.HealthCheckProvider(definition.StableID)
		}()
		close(start)
		wait.Wait()
		close(errorsFound)
		for err := range errorsFound {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	waitForHealthTasks(t, manager)
}

func TestFileProviderWatcherCannotRaceManagerOwnedHealthChecks(t *testing.T) {
	manager := runtimeManager(t)
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer health.Close()
	path := filepath.Join(sharedRuntime.home, "file-health-race.yaml")
	write := func(index int) {
		t.Helper()
		content := fmt.Sprintf("proxies:\n  - name: Direct %d\n    type: direct\n", index)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(0)
	definition := ProviderDefinition{
		StableID: "file-health-race", Name: "File Health Race", Type: "file", FilePath: path,
		HealthCheck: true, HealthCheckURL: health.URL, HealthCheckSeconds: 60, HealthCheckTimeout: 1000, ExpectedStatus: "200-399",
	}
	if err := manager.ApplyProviders([]ProviderDefinition{definition}); err != nil {
		t.Fatal(err)
	}
	key, _ := ProviderKey(definition.StableID)
	initialVersion := manager.config.Providers[key].Version()
	for index := 1; index <= 40; index++ {
		write(index)
		if err := manager.HealthCheckProvider(definition.StableID); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && manager.config.Providers[key].Version() == initialVersion {
		time.Sleep(10 * time.Millisecond)
	}
	if manager.config.Providers[key].Version() == initialVersion {
		t.Fatal("File Provider watcher did not update proxies during the race regression")
	}
	waitForHealthTasks(t, manager)
}

func TestLazyHealthCheckRequiresRealProviderActivityAfterInitialRun(t *testing.T) {
	manager := runtimeManager(t)
	var requests atomic.Int32
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer health.Close()
	definition := ProviderDefinition{
		StableID: "lazy-health", Name: "Lazy Health", Type: "inline",
		Payload:     []map[string]any{{"name": "Direct", "type": "direct"}},
		HealthCheck: true, HealthCheckURL: health.URL, HealthCheckSeconds: 60, HealthCheckTimeout: 1000,
		HealthCheckLazy: true, ExpectedStatus: "200-399",
	}
	if err := manager.ApplyProviders([]ProviderDefinition{definition}); err != nil {
		t.Fatal(err)
	}
	forceHealthDue(manager, definition.StableID)
	manager.pollProviders()
	waitForRequestCount(t, &requests, 1)
	waitForHealthTasks(t, manager)

	forceHealthDue(manager, definition.StableID)
	manager.pollProviders()
	time.Sleep(50 * time.Millisecond)
	if got := requests.Load(); got != 1 {
		t.Fatalf("idle Lazy Provider ran another health check: requests=%d", got)
	}

	manager.store.TouchProvider(definition.StableID)
	forceHealthDue(manager, definition.StableID)
	manager.pollProviders()
	waitForRequestCount(t, &requests, 2)
	waitForHealthTasks(t, manager)
}

func TestHealthCheckNetworkWorkDoesNotBlockProviderRefresh(t *testing.T) {
	manager := runtimeManager(t)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHealth := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseHealth()
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer health.Close()
	definition := ProviderDefinition{
		StableID: "nonblocking-health", Name: "Nonblocking Health", Type: "inline",
		Payload:     []map[string]any{{"name": "Direct", "type": "direct"}},
		HealthCheck: true, HealthCheckURL: health.URL, HealthCheckSeconds: 60, HealthCheckTimeout: 5000, ExpectedStatus: "200-399",
	}
	if err := manager.ApplyProviders([]ProviderDefinition{definition}); err != nil {
		t.Fatal(err)
	}
	if err := manager.HealthCheckProvider(definition.StableID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("health check did not start")
	}
	started := time.Now()
	if err := manager.RefreshProvider(definition.StableID); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("Provider refresh waited for health-check network work: %v", elapsed)
	}
	releaseHealth()
	waitForHealthTasks(t, manager)
}

func TestApplyProvidersCancelsHealthTasksBeforeReplacingProxies(t *testing.T) {
	manager := runtimeManager(t)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHealth := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseHealth()
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer health.Close()
	definition := ProviderDefinition{
		StableID: "replace-health", Name: "Replace Health", Type: "inline",
		Payload:     []map[string]any{{"name": "Old Direct", "type": "direct"}},
		HealthCheck: true, HealthCheckURL: health.URL, HealthCheckSeconds: 60, HealthCheckTimeout: 5000, ExpectedStatus: "200-399",
	}
	if err := manager.ApplyProviders([]ProviderDefinition{definition}); err != nil {
		t.Fatal(err)
	}
	if err := manager.HealthCheckProvider(definition.StableID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("health check did not start")
	}
	replacement := definition
	replacement.Payload = []map[string]any{{"name": "New Direct", "type": "direct"}}
	replacement.HealthCheck = false
	started := time.Now()
	if err := manager.ApplyProviders([]ProviderDefinition{replacement}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("Provider replacement did not promptly cancel health work: %v", elapsed)
	}
	manager.healthMu.Lock()
	active := len(manager.healthTasks)
	manager.healthMu.Unlock()
	if active != 0 {
		t.Fatalf("Provider replacement retained %d health tasks", active)
	}
	releaseHealth()
}

func forceHealthDue(manager *Manager, stableID string) {
	key, _ := ProviderKey(stableID)
	manager.mu.Lock()
	manager.nextHealth[key] = time.Now().Add(-time.Second)
	manager.mu.Unlock()
}

func waitForRequestCount(t *testing.T, requests *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if requests.Load() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("health-check requests=%d, want at least %d", requests.Load(), want)
}

func waitForHealthTasks(t *testing.T, manager *Manager) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		manager.healthMu.Lock()
		active := len(manager.healthTasks)
		manager.healthMu.Unlock()
		if active == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("Provider health-check tasks did not finish")
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

func TestConfigureWhenStoppedReplacesAllRuntimeInputs(t *testing.T) {
	manager, err := NewManager(ManagerOptions{
		HomeDir: t.TempDir(), ControllerSocket: filepath.Join(t.TempDir(), "controller.sock"), ControllerSecret: "controller-secret",
		SocksBind: "127.0.0.1", SocksAdvertise: "127.0.0.1", SocksPort: 1080,
		MasterKey: []byte("01234567890123456789012345678901"),
	})
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("abcdefghijklmnopqrstuvwxyzABCDEF")
	definitions := []ProviderDefinition{{StableID: "candidate", Name: "Candidate", Type: "inline"}}
	if err := manager.ConfigureWhenStopped("127.0.0.2", "socks.surge.eb", 2080, true, key, definitions); err != nil {
		t.Fatal(err)
	}
	key[0] = 'x'
	definitions[0].Name = "mutated"
	manager.mu.RLock()
	options := manager.options
	manager.mu.RUnlock()
	if options.SocksBind != "127.0.0.2" || options.SocksAdvertise != "socks.surge.eb" || options.SocksPort != 2080 || !options.PrefixProvider {
		t.Fatalf("projection options were not replaced together: %#v", options)
	}
	if string(options.MasterKey) != "abcdefghijklmnopqrstuvwxyzABCDEF" || len(options.Providers) != 1 || options.Providers[0].Name != "Candidate" {
		t.Fatalf("key or Providers were not defensively replaced: key=%q providers=%#v", options.MasterKey, options.Providers)
	}
}

func TestManagerStopRestartKeepsProcessHostResolver(t *testing.T) {
	if os.Getenv("SURGEEB_RESOLVER_LIFECYCLE_HELPER") == "" {
		command := exec.Command(os.Args[0], "-test.run=^TestManagerStopRestartKeepsProcessHostResolver$", "-test.v")
		command.Env = append(os.Environ(), "SURGEEB_RESOLVER_LIFECYCLE_HELPER=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("resolver lifecycle helper failed: %v\n%s", err, output)
		}
		return
	}
	home, err := os.MkdirTemp("/tmp", "surgeeb-resolver-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(ManagerOptions{
		HomeDir: home, ControllerSocket: filepath.Join(home, "controller.sock"), ControllerSecret: "controller-secret",
		SocksBind: "127.0.0.1", SocksAdvertise: "127.0.0.1", SocksPort: port,
		MasterKey: []byte("01234567890123456789012345678901"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	stable := resolver.ProxyServerHostResolver
	if stable != sharedProviderHostResolver {
		t.Fatalf("runtime resolver = %T %p, want shared %p", stable, stable, sharedProviderHostResolver)
	}
	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
	if resolver.ProxyServerHostResolver != stable {
		t.Fatal("stopping the Manager replaced the process proxy-server resolver")
	}
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	if resolver.ProxyServerHostResolver != stable {
		t.Fatal("restarting the Manager replaced the process proxy-server resolver")
	}
	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestAsyncFailClosedWaitsForApplyMutex(t *testing.T) {
	if os.Getenv("SURGEEB_FAIL_CLOSED_HELPER") == "" {
		command := exec.Command(os.Args[0], "-test.run=^TestAsyncFailClosedWaitsForApplyMutex$", "-test.v")
		command.Env = append(os.Environ(), "SURGEEB_FAIL_CLOSED_HELPER=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("fail-closed helper failed: %v\n%s", err, output)
		}
		return
	}
	home, err := os.MkdirTemp("/tmp", "sfc-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(ManagerOptions{
		HomeDir: home, ControllerSocket: filepath.Join(home, "controller.sock"), ControllerSecret: "controller-secret",
		SocksBind: "127.0.0.1", SocksAdvertise: "127.0.0.1", SocksPort: port,
		MasterKey: []byte("01234567890123456789012345678901"), PollInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	manager.applyMu.Lock()
	manager.mu.RLock()
	config := manager.config
	manager.mu.RUnlock()
	done := manager.failClosedAsync(config, errors.New("security invariant failed"))
	select {
	case <-done:
		manager.applyMu.Unlock()
		t.Fatal("asynchronous fail-closed bypassed applyMu")
	case <-time.After(25 * time.Millisecond):
	}
	if state := manager.Status().State; state != "running" {
		manager.applyMu.Unlock()
		t.Fatalf("manager changed state while applyMu was held: %s", state)
	}
	manager.applyMu.Unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("asynchronous fail-closed did not complete")
	}
	if state := manager.Status().State; state != "error" {
		t.Fatalf("fail-closed state=%s, want error", state)
	}
	if err := manager.Stop(); err != nil {
		t.Fatal(err)
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
	if err := manager.ConfigureWhenStopped("127.0.0.1", "127.0.0.1", port, true, []byte("01234567890123456789012345678901"), nil); err != nil {
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
