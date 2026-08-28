package mihomo

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	C "github.com/metacubex/mihomo/constant"
)

func TestExtractProviderHostsKeepsOnlyProxyServerAliasClosure(t *testing.T) {
	set, err := extractProviderHosts(
		[]map[string]any{{"server": "edge.example.com"}},
		map[string]any{
			"edge.example.com":   "origin.example.com",
			"origin.example.com": "192.0.2.10",
			"unused.example.com": "192.0.2.20",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.mappings) != 2 || set.mappings["unused.example.com"].pattern != "" {
		t.Fatalf("relevant hosts mappings = %#v", set.mappings)
	}
	target := set.expected["edge.example.com"]
	if len(target.ips) != 1 || target.ips[0] != netip.MustParseAddr("192.0.2.10") {
		t.Fatalf("proxy server target = %#v", target)
	}
}

func TestProviderHostResolverSupportsHostnameAliasAndIPArrays(t *testing.T) {
	aliasSet, err := extractProviderHosts(
		[]map[string]any{{"server": "edge.example.com"}},
		map[string]any{"edge.example.com": "origin.example.com"},
	)
	if err != nil {
		t.Fatal(err)
	}
	aliasResolver, err := newProviderHostResolver(aliasSet.mappings)
	if err != nil {
		t.Fatal(err)
	}
	if target, err := aliasResolver.resolve("edge.example.com"); err != nil || target.domain != "origin.example.com" {
		t.Fatalf("hostname alias target = %#v, %v", target, err)
	}

	ipSet, err := extractProviderHosts(
		[]map[string]any{{"server": "edge.example.com"}},
		map[string]any{"edge.example.com": []any{"2001:db8::10", "192.0.2.10"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	ipResolver, err := newProviderHostResolver(ipSet.mappings)
	if err != nil {
		t.Fatal(err)
	}
	ips, err := ipResolver.LookupIPv4(context.Background(), "edge.example.com")
	if err != nil || len(ips) != 1 || ips[0] != netip.MustParseAddr("192.0.2.10") {
		t.Fatalf("IPv4 hosts result = %v, %v", ips, err)
	}
}

func TestExtractProviderHostsRejectsMatchedCycles(t *testing.T) {
	_, err := extractProviderHosts(
		[]map[string]any{{"server": "a.example.com"}},
		map[string]any{"a.example.com": "b.example.com", "b.example.com": "a.example.com"},
	)
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle error = %v", err)
	}
}

func TestBuildProviderHostResolverRejectsCrossProviderConflicts(t *testing.T) {
	home := shortTempDir(t)
	definitions := []ProviderDefinition{
		{
			StableID: "first", Name: "First", Type: "inline",
			Payload: []map[string]any{{"name": "First", "type": "socks5", "server": "edge.example.com", "port": 1080}},
			Hosts:   map[string]any{"edge.example.com": "first.example.com"},
		},
		{
			StableID: "second", Name: "Second", Type: "inline",
			Payload: []map[string]any{{"name": "Second", "type": "socks5", "server": "edge.example.com", "port": 1081}},
			Hosts:   map[string]any{"edge.example.com": "second.example.com"},
		},
	}
	cfg, err := BuildControlledConfig(home, filepath.Join(home, "controller.sock"), "controller-secret", definitions, NewSnapshotStore(nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := buildProviderHostResolver(cfg.Providers, definitions); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("cross-Provider conflict error = %v", err)
	}
}

func TestFilteredProviderRejectRestoresPreviousProxyAndHostsVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provider.yaml")
	writeSource := func(node, target string) {
		t.Helper()
		content := "proxies:\n  - name: " + node + "\n    type: socks5\n    server: edge.example.com\n    port: 1080\nhosts:\n  edge.example.com: " + target + "\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeSource("Old", "old.example.com")
	oldProxy := &fakeProxy{name: "Old", adapterType: C.Socks5}
	inner := &filterTestProvider{proxies: []C.Proxy{oldProxy}}
	providerInterface, err := newFilteredProxyProvider(inner, providerHostSource{path: path})
	if err != nil {
		t.Fatal(err)
	}
	provider := providerInterface.(*filteredProxyProvider)
	if err := provider.Initial(); err != nil {
		t.Fatal(err)
	}
	provider.acceptCurrent()

	writeSource("New", "new.example.com")
	inner.next = []C.Proxy{&fakeProxy{name: "New", adapterType: C.Socks5}}
	if err := provider.Update(); err != nil {
		t.Fatal(err)
	}
	provider.rejectCurrent(errors.New("aggregate hosts conflict"))
	if got := provider.Proxies(); len(got) != 1 || got[0].Name() != "Old" {
		t.Fatalf("rejected proxy version = %#v", got)
	}
	state, hostError := provider.hostState()
	if hostError == "" || state.expected["edge.example.com"].domain != "old.example.com" {
		t.Fatalf("rejected hosts state = %#v, %q", state, hostError)
	}
}

func TestProviderHostSourceRestoresAcceptedHTTPCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "provider-cache")
	source := providerHostSource{path: path, acceptedPath: path + ".surgeeb-accepted"}
	accepted := []byte("proxies:\n  - name: Accepted\n    type: direct\n")
	if err := os.WriteFile(path, accepted, 0o600); err != nil {
		t.Fatal(err)
	}
	acceptedModTime := time.Now().Add(-24 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, acceptedModTime, acceptedModTime); err != nil {
		t.Fatal(err)
	}
	if err := source.accept(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(source.acceptedPath); err != nil || !info.ModTime().Equal(acceptedModTime) {
		t.Fatalf("accepted cache mtime = %v, %v, want %v", info, err, acceptedModTime)
	}
	if err := os.WriteFile(path, []byte("rejected cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := source.prepare(); err != nil {
		t.Fatal(err)
	}
	if restored, err := os.ReadFile(path); err != nil || string(restored) != string(accepted) {
		t.Fatalf("startup cache restore = %q, %v", restored, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("restored cache permissions = %v, %v", info, err)
	} else if !info.ModTime().Equal(acceptedModTime) {
		t.Fatalf("restored cache mtime = %v, want %v", info.ModTime(), acceptedModTime)
	}
}
