package mihomo

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	U "github.com/metacubex/mihomo/common/utils"
	C "github.com/metacubex/mihomo/constant"
	P "github.com/metacubex/mihomo/constant/provider"
)

type closeTrackingProxy struct {
	*fakeProxy
	dialerProxy string
	closeOnce   sync.Once
	closeCount  atomic.Int32
}

func (p *closeTrackingProxy) ProxyInfo() C.ProxyInfo {
	return C.ProxyInfo{ProviderName: "fixture", DialerProxy: p.dialerProxy}
}

func (p *closeTrackingProxy) Close() error {
	p.closeOnce.Do(func() { p.closeCount.Add(1) })
	return nil
}

type filterTestProvider struct {
	proxies []C.Proxy
	next    []C.Proxy
	version uint32
}

func (p *filterTestProvider) Name() string               { return "filter-test" }
func (p *filterTestProvider) VehicleType() P.VehicleType { return P.Inline }
func (p *filterTestProvider) Type() P.ProviderType       { return P.Proxy }
func (p *filterTestProvider) Initial() error             { return nil }
func (p *filterTestProvider) Proxies() []C.Proxy         { return append([]C.Proxy(nil), p.proxies...) }
func (p *filterTestProvider) Count() int                 { return len(p.proxies) }
func (p *filterTestProvider) Touch()                     {}
func (p *filterTestProvider) HealthCheck()               {}
func (p *filterTestProvider) Version() uint32            { return p.version }
func (p *filterTestProvider) HealthCheckURL() string     { return "" }
func (p *filterTestProvider) RegisterHealthCheckTask(string, U.IntRanges[uint16], string, uint) {
}

func (p *filterTestProvider) Update() error {
	if p.next != nil {
		p.proxies = p.next
		p.next = nil
	}
	p.version++
	return nil
}

func TestFilteredProviderClosesDialerProxyAdaptersImmediatelyAndAfterRefresh(t *testing.T) {
	safe := &closeTrackingProxy{fakeProxy: &fakeProxy{name: "Safe", adapterType: C.Socks5}}
	filtered := &closeTrackingProxy{
		fakeProxy: &fakeProxy{name: "Filtered", adapterType: C.WireGuard}, dialerProxy: "链式代理规则",
	}
	inner := &filterTestProvider{proxies: []C.Proxy{safe, filtered}}
	provider, err := newFilteredProxyProvider(inner, providerHostSource{inline: true})
	if err != nil {
		t.Fatal(err)
	}

	if filtered.closeCount.Load() != 1 || safe.closeCount.Load() != 0 {
		t.Fatalf("initial filter close counts: safe=%d filtered=%d", safe.closeCount.Load(), filtered.closeCount.Load())
	}
	if provider.Count() != 1 || provider.Proxies()[0].Name() != "Safe" {
		t.Fatalf("initial filtered Provider proxies = %#v", provider.Proxies())
	}
	filteredProvider := provider.(*filteredProxyProvider)
	filteredProvider.acceptCurrent()
	// Re-reading an unchanged Provider may re-evaluate it, but Close must remain
	// idempotent for an already filtered adapter.
	_ = provider.Proxies()
	if filtered.closeCount.Load() != 1 {
		t.Fatalf("unchanged filtered adapter closed %d times", filtered.closeCount.Load())
	}

	replacementSafe := &closeTrackingProxy{fakeProxy: &fakeProxy{name: "Safe 2", adapterType: C.Socks5}}
	replacementFiltered := &closeTrackingProxy{
		fakeProxy: &fakeProxy{name: "Filtered 2", adapterType: C.WireGuard}, dialerProxy: "链式代理规则",
	}
	inner.next = []C.Proxy{replacementSafe, replacementFiltered}
	if err := provider.Update(); err != nil {
		t.Fatal(err)
	}
	if replacementFiltered.closeCount.Load() != 1 || replacementSafe.closeCount.Load() != 0 {
		t.Fatalf("refresh filter close counts: safe=%d filtered=%d", replacementSafe.closeCount.Load(), replacementFiltered.closeCount.Load())
	}
	if provider.Count() != 1 || provider.Proxies()[0].Name() != "Safe" {
		t.Fatalf("staged Provider refresh leaked before commit: %#v", provider.Proxies())
	}
	if candidate := providerCandidateProxies(provider); len(candidate) != 1 || candidate[0].Name() != "Safe 2" {
		t.Fatalf("staged Provider candidate = %#v", candidate)
	}
	filteredProvider.acceptCurrent()
	if provider.Count() != 1 || provider.Proxies()[0].Name() != "Safe 2" {
		t.Fatalf("committed filtered Provider proxies = %#v", provider.Proxies())
	}
}

func TestFilteredProviderRejectClosesOnlyStagedAdapters(t *testing.T) {
	committed := &closeTrackingProxy{fakeProxy: &fakeProxy{name: "Committed", adapterType: C.WireGuard}}
	inner := &filterTestProvider{proxies: []C.Proxy{committed}}
	providerInterface, err := newFilteredProxyProvider(inner, providerHostSource{inline: true})
	if err != nil {
		t.Fatal(err)
	}
	provider := providerInterface.(*filteredProxyProvider)
	provider.acceptCurrent()

	rejected := &closeTrackingProxy{fakeProxy: &fakeProxy{name: "Rejected", adapterType: C.WireGuard}}
	inner.next = []C.Proxy{rejected}
	if err := provider.Update(); err != nil {
		t.Fatal(err)
	}
	if err := provider.rejectCurrent(errors.New("aggregate conflict")); err != nil {
		t.Fatal(err)
	}
	if committed.closeCount.Load() != 0 || rejected.closeCount.Load() != 1 {
		t.Fatalf("reject close counts: committed=%d rejected=%d", committed.closeCount.Load(), rejected.closeCount.Load())
	}
	if proxies := provider.Proxies(); len(proxies) != 1 || proxies[0].Name() != "Committed" {
		t.Fatalf("rejected Provider changed committed proxies: %#v", proxies)
	}
}

func TestProviderStateTransactionRollsBackEarlierAcceptedCaches(t *testing.T) {
	newStagedProvider := func(name string) (*filteredProxyProvider, providerHostSource, string) {
		t.Helper()
		directory := t.TempDir()
		path := filepath.Join(directory, "provider.yaml")
		accepted := "proxies:\n  - name: " + name + " Old\n    type: direct\n"
		candidate := "proxies:\n  - name: " + name + " Candidate\n    type: direct\n"
		if err := os.WriteFile(path, []byte(accepted), 0o600); err != nil {
			t.Fatal(err)
		}
		source := providerHostSource{path: path, acceptedPath: path + ".surgeeb-accepted"}
		if err := source.accept(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(candidate), 0o600); err != nil {
			t.Fatal(err)
		}
		provider := &filteredProxyProvider{
			inner:      &filterTestProvider{version: 1},
			hostSource: source,
			state:      filteredProviderState{initialized: true},
			staged:     &filteredProviderState{initialized: true, version: 1},
		}
		return provider, source, accepted
	}

	first, firstSource, firstAccepted := newStagedProvider("First")
	second, secondSource, _ := newStagedProvider("Second")
	transaction, err := prepareProviderStates(map[string]P.ProxyProvider{"a": first, "b": second})
	if err != nil {
		t.Fatal(err)
	}
	// Make the second Provider's prepared rename fail after the first Provider
	// has already advanced. The transaction must restore the first sidecar.
	if err := os.RemoveAll(filepath.Dir(secondSource.path)); err != nil {
		t.Fatal(err)
	}
	commitErr := transaction.commit()
	if commitErr == nil {
		t.Fatal("cache transaction unexpectedly committed after a prepared file disappeared")
	}
	_ = transaction.reject(commitErr)

	for _, path := range []string{firstSource.path, firstSource.acceptedPath} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != firstAccepted || strings.Contains(string(data), "Candidate") {
			t.Fatalf("rolled back cache %s = %q, %v", path, data, err)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(firstSource.path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".surgeeb-provider-cache-") {
			t.Fatalf("cache transaction left temporary file %q", entry.Name())
		}
	}
}
