package mihomo

import (
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
	provider := newFilteredProxyProvider(inner)

	if filtered.closeCount.Load() != 1 || safe.closeCount.Load() != 0 {
		t.Fatalf("initial filter close counts: safe=%d filtered=%d", safe.closeCount.Load(), filtered.closeCount.Load())
	}
	if provider.Count() != 1 || provider.Proxies()[0].Name() != "Safe" {
		t.Fatalf("initial filtered Provider proxies = %#v", provider.Proxies())
	}
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
	if provider.Count() != 1 || provider.Proxies()[0].Name() != "Safe 2" {
		t.Fatalf("refreshed filtered Provider proxies = %#v", provider.Proxies())
	}
}
