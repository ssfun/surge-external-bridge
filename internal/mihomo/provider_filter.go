package mihomo

import (
	"encoding/json"
	"strings"
	"sync"

	U "github.com/metacubex/mihomo/common/utils"
	C "github.com/metacubex/mihomo/constant"
	P "github.com/metacubex/mihomo/constant/provider"
)

// FilteredProviderNode describes a source node that SurgeEB deliberately does
// not expose. Provider nodes cannot safely resolve dialer-proxy references in
// the product-owned Mihomo topology because user proxy groups are not imported.
type FilteredProviderNode struct {
	Name        string `json:"name"`
	DialerProxy string `json:"dialer_proxy"`
}

type ProviderFilterReport struct {
	SourceCount    int                    `json:"source_count"`
	AvailableCount int                    `json:"available_count"`
	FilteredNodes  []FilteredProviderNode `json:"filtered_nodes,omitempty"`
}

func (r ProviderFilterReport) FilteredCount() int { return len(r.FilteredNodes) }

// filteredProxyProvider keeps native Mihomo parsing and refresh behavior while
// presenting only nodes that the controlled runtime can execute safely. The
// cache follows the native Provider version so file-watcher updates are
// filtered before projection, health checks, or Controller API access.
type filteredProxyProvider struct {
	inner P.ProxyProvider

	mu          sync.RWMutex
	initialized bool
	version     uint32
	proxies     []C.Proxy
	report      ProviderFilterReport
}

func newFilteredProxyProvider(inner P.ProxyProvider) P.ProxyProvider {
	provider := &filteredProxyProvider{inner: inner}
	provider.sync(true)
	return provider
}

func (p *filteredProxyProvider) Name() string               { return p.inner.Name() }
func (p *filteredProxyProvider) VehicleType() P.VehicleType { return p.inner.VehicleType() }
func (p *filteredProxyProvider) Type() P.ProviderType       { return p.inner.Type() }
func (p *filteredProxyProvider) Touch()                     { p.inner.Touch() }
func (p *filteredProxyProvider) HealthCheckURL() string     { return p.inner.HealthCheckURL() }
func (p *filteredProxyProvider) RegisterHealthCheckTask(_ string, _ U.IntRanges[uint16], _ string, _ uint) {
	// SurgeEB owns Provider health-check scheduling and runs it against Proxies(),
	// which is already filtered. Do not register Mihomo's native all-node task.
}

func (p *filteredProxyProvider) Initial() error {
	if err := p.inner.Initial(); err != nil {
		return err
	}
	p.sync(true)
	return nil
}

func (p *filteredProxyProvider) Update() error {
	if err := p.inner.Update(); err != nil {
		return err
	}
	p.sync(true)
	return nil
}

func (p *filteredProxyProvider) Proxies() []C.Proxy {
	p.sync(false)
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]C.Proxy(nil), p.proxies...)
}

func (p *filteredProxyProvider) Count() int { return len(p.Proxies()) }

func (p *filteredProxyProvider) HealthCheck() {
	// The product API uses Manager.startProviderHealthCheckLocked, which tests
	// Proxies(). Keeping the private native endpoint inert prevents an accidental
	// health check from reaching source nodes that were intentionally filtered.
}

func (p *filteredProxyProvider) Version() uint32 {
	p.sync(false)
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.version
}

func (p *filteredProxyProvider) FilterReport() ProviderFilterReport {
	p.sync(false)
	p.mu.RLock()
	defer p.mu.RUnlock()
	return cloneProviderFilterReport(p.report)
}

func (p *filteredProxyProvider) sourceProxies() []C.Proxy {
	return append([]C.Proxy(nil), p.inner.Proxies()...)
}

func (p *filteredProxyProvider) Close() error {
	if closer, ok := p.inner.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func (p *filteredProxyProvider) MarshalJSON() ([]byte, error) {
	runtime := make(map[string]any)
	if encoded, err := json.Marshal(p.inner); err == nil {
		_ = json.Unmarshal(encoded, &runtime)
	}
	report := p.FilterReport()
	runtime["proxies"] = p.Proxies()
	runtime["count"] = report.AvailableCount
	runtime["filteredCount"] = report.FilteredCount()
	runtime["filteredNodes"] = report.FilteredNodes
	return json.Marshal(runtime)
}

func (p *filteredProxyProvider) sync(force bool) {
	version := p.inner.Version()
	p.mu.RLock()
	current := p.initialized && p.version == version
	p.mu.RUnlock()
	if !force && current {
		return
	}

	source := p.inner.Proxies()
	available := make([]C.Proxy, 0, len(source))
	filtered := make([]FilteredProviderNode, 0)
	for _, proxy := range source {
		if proxy == nil {
			continue
		}
		dialerProxy := strings.TrimSpace(proxy.ProxyInfo().DialerProxy)
		if dialerProxy != "" {
			filtered = append(filtered, FilteredProviderNode{Name: proxy.Name(), DialerProxy: dialerProxy})
			// Mihomo has already constructed the adapter before the Provider
			// wrapper can inspect ProxyInfo. Close it immediately so stateful
			// adapters such as WireGuard do not retain their worker goroutines.
			// Native Provider adapters are auto-close wrappers, so later Provider
			// shutdown remains safe and idempotent.
			_ = proxy.Close()
			continue
		}
		available = append(available, proxy)
	}

	p.mu.Lock()
	p.initialized = true
	p.version = version
	p.proxies = available
	p.report = ProviderFilterReport{
		SourceCount: len(source), AvailableCount: len(available), FilteredNodes: filtered,
	}
	p.mu.Unlock()
}

func cloneProviderFilterReport(report ProviderFilterReport) ProviderFilterReport {
	report.FilteredNodes = append([]FilteredProviderNode(nil), report.FilteredNodes...)
	return report
}

func providerFilterReport(provider P.ProxyProvider) ProviderFilterReport {
	if filtered, ok := provider.(*filteredProxyProvider); ok {
		return filtered.FilterReport()
	}
	if provider == nil {
		return ProviderFilterReport{}
	}
	count := provider.Count()
	return ProviderFilterReport{SourceCount: count, AvailableCount: count}
}

func providerSourceProxies(provider P.ProxyProvider) []C.Proxy {
	if filtered, ok := provider.(*filteredProxyProvider); ok {
		return filtered.sourceProxies()
	}
	return append([]C.Proxy(nil), provider.Proxies()...)
}
