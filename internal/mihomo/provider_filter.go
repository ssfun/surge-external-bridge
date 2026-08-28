package mihomo

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
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

type filteredProviderState struct {
	initialized bool
	version     uint32
	proxies     []C.Proxy
	report      ProviderFilterReport
	hosts       providerHostSet
}

// filteredProxyProvider commits native proxy content and its source hosts as a
// single version. If hosts parsing fails, the previously accepted proxies and
// mappings stay visible even though the native Provider has already refreshed.
type filteredProxyProvider struct {
	inner      P.ProxyProvider
	hostSource providerHostSource

	syncMu sync.Mutex
	mu     sync.RWMutex
	state  filteredProviderState
	staged *filteredProviderState

	rejectedVersion uint32
	lastHostError   string
}

func newFilteredProxyProvider(inner P.ProxyProvider, source providerHostSource) (P.ProxyProvider, error) {
	provider := &filteredProxyProvider{inner: inner, hostSource: source}
	if source.inline {
		if err := provider.sync(true); err != nil {
			return nil, err
		}
	}
	return provider, nil
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
	if err := p.hostSource.prepare(); err != nil {
		return err
	}
	if err := p.inner.Initial(); err != nil {
		return err
	}
	return p.sync(true)
}

func (p *filteredProxyProvider) Update() error {
	p.mu.RLock()
	rejectedVersion := p.rejectedVersion
	rejectedError := p.lastHostError
	p.mu.RUnlock()
	if err := p.inner.Update(); err != nil {
		return err
	}
	if rejectedVersion != 0 && rejectedVersion == p.inner.Version() && rejectedError != "" {
		return errors.New(rejectedError)
	}
	return p.sync(true)
}

func (p *filteredProxyProvider) Proxies() []C.Proxy {
	p.mu.RLock()
	defer p.mu.RUnlock()
	state := p.visibleStateLocked()
	return append([]C.Proxy(nil), state.proxies...)
}

func (p *filteredProxyProvider) Count() int { return len(p.Proxies()) }

func (p *filteredProxyProvider) HealthCheck() {
	// The product API uses Manager.startProviderHealthCheckLocked, which tests
	// Proxies(). Keeping the private native endpoint inert prevents an accidental
	// health check from reaching source nodes that were intentionally filtered.
}

func (p *filteredProxyProvider) Version() uint32 {
	_ = p.sync(false)
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.staged != nil {
		return p.staged.version
	}
	return p.state.version
}

func (p *filteredProxyProvider) FilterReport() ProviderFilterReport {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return cloneProviderFilterReport(p.visibleStateLocked().report)
}

func (p *filteredProxyProvider) hostState() (providerHostSet, string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.visibleStateLocked().hosts.clone(), p.lastHostError
}

func (p *filteredProxyProvider) candidateState() filteredProviderState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.staged != nil {
		return cloneFilteredProviderState(*p.staged)
	}
	return cloneFilteredProviderState(p.state)
}

func (p *filteredProxyProvider) visibleStateLocked() filteredProviderState {
	if p.state.initialized || p.staged == nil {
		return p.state
	}
	return *p.staged
}

func (p *filteredProxyProvider) prepareCurrent() (*providerCacheUpdate, bool, error) {
	p.mu.RLock()
	hasStaged := p.staged != nil
	p.mu.RUnlock()
	if !hasStaged {
		return nil, false, nil
	}
	cache, err := p.hostSource.prepareAcceptance()
	return cache, true, err
}

func (p *filteredProxyProvider) acceptCurrent() {
	p.mu.Lock()
	if p.staged != nil {
		p.state = *p.staged
		p.staged = nil
	}
	p.rejectedVersion = 0
	p.lastHostError = ""
	p.mu.Unlock()
}

func (p *filteredProxyProvider) rejectCurrent(err error) error {
	p.mu.Lock()
	staged := p.staged
	p.staged = nil
	p.rejectedVersion = p.inner.Version()
	p.lastHostError = err.Error()
	committed := append([]C.Proxy(nil), p.state.proxies...)
	p.mu.Unlock()
	if staged != nil {
		closeUncommittedProxies(staged.proxies, committed)
	}
	if restoreErr := p.hostSource.reject(); restoreErr != nil {
		return restoreErr
	}
	return nil
}

func (p *filteredProxyProvider) sourceProxies() []C.Proxy {
	return append([]C.Proxy(nil), p.inner.Proxies()...)
}

func (p *filteredProxyProvider) Close() error {
	source := p.sourceProxies()
	p.mu.RLock()
	committed := append([]C.Proxy(nil), p.state.proxies...)
	var staged []C.Proxy
	if p.staged != nil {
		staged = append([]C.Proxy(nil), p.staged.proxies...)
	}
	p.mu.RUnlock()
	closeUncommittedProxies(committed, source)
	retained := append(source, committed...)
	closeUncommittedProxies(staged, retained)
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
	hosts, hostError := p.hostState()
	runtime["proxies"] = p.Proxies()
	runtime["count"] = report.AvailableCount
	runtime["filteredCount"] = report.FilteredCount()
	runtime["filteredNodes"] = report.FilteredNodes
	runtime["hostsCount"] = len(hosts.mappings)
	if hostError != "" {
		runtime["hostsError"] = hostError
	}
	return json.Marshal(runtime)
}

func (p *filteredProxyProvider) sync(force bool) error {
	p.syncMu.Lock()
	defer p.syncMu.Unlock()
	version := p.inner.Version()
	p.mu.RLock()
	current := p.state.initialized && p.state.version == version && p.staged == nil
	staged := p.staged != nil && p.staged.version == version
	rejected := !force && p.rejectedVersion == version && p.lastHostError != ""
	rejectedError := p.lastHostError
	p.mu.RUnlock()
	if rejected {
		return errors.New(rejectedError)
	}
	if !force && (current || staged) {
		return nil
	}

	source := p.inner.Proxies()
	hosts, err := p.hostSource.load()
	if err != nil {
		p.mu.Lock()
		staged := p.staged
		p.staged = nil
		p.rejectedVersion = version
		p.lastHostError = err.Error()
		committed := append([]C.Proxy(nil), p.state.proxies...)
		p.mu.Unlock()
		if staged != nil {
			closeUncommittedProxies(staged.proxies, committed)
		}
		closeUncommittedProxies(source, committed)
		return errors.Join(err, p.hostSource.reject())
	}
	hosts.addProxyAddresses(source)
	available := make([]C.Proxy, 0, len(source))
	filtered := make([]FilteredProviderNode, 0)
	for _, proxy := range source {
		if proxy == nil {
			continue
		}
		dialerProxy := strings.TrimSpace(proxy.ProxyInfo().DialerProxy)
		if dialerProxy != "" {
			filtered = append(filtered, FilteredProviderNode{Name: proxy.Name(), DialerProxy: dialerProxy})
			// Mihomo has already constructed the adapter. Close filtered stateful
			// adapters immediately so their worker goroutines cannot leak.
			_ = proxy.Close()
			continue
		}
		available = append(available, proxy)
	}
	candidate := filteredProviderState{
		initialized: true,
		version:     version,
		proxies:     available,
		report: ProviderFilterReport{
			SourceCount: len(source), AvailableCount: len(available), FilteredNodes: filtered,
		},
		hosts: hosts,
	}
	p.mu.Lock()
	previousStaged := p.staged
	committed := append([]C.Proxy(nil), p.state.proxies...)
	p.staged = &candidate
	p.rejectedVersion = 0
	p.lastHostError = ""
	p.mu.Unlock()
	if previousStaged != nil {
		retained := append(committed, candidate.proxies...)
		closeUncommittedProxies(previousStaged.proxies, retained)
	}
	return nil
}

func closeUncommittedProxies(candidate, committed []C.Proxy) {
	for _, proxy := range candidate {
		if proxy == nil || containsProxy(committed, proxy) {
			continue
		}
		_ = proxy.Close()
	}
}

func containsProxy(proxies []C.Proxy, candidate C.Proxy) bool {
	candidateValue := reflect.ValueOf(candidate)
	for _, proxy := range proxies {
		value := reflect.ValueOf(proxy)
		if !value.IsValid() || !candidateValue.IsValid() || value.Type() != candidateValue.Type() {
			continue
		}
		if value.Type().Comparable() && value.Interface() == candidateValue.Interface() {
			return true
		}
	}
	return false
}

func cloneFilteredProviderState(state filteredProviderState) filteredProviderState {
	state.proxies = append([]C.Proxy(nil), state.proxies...)
	state.report = cloneProviderFilterReport(state.report)
	state.hosts = state.hosts.clone()
	return state
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

func providerCandidateProxies(provider P.ProxyProvider) []C.Proxy {
	if filtered, ok := provider.(*filteredProxyProvider); ok {
		return filtered.candidateState().proxies
	}
	return append([]C.Proxy(nil), provider.Proxies()...)
}

func providerHostError(provider P.ProxyProvider) string {
	if filtered, ok := provider.(*filteredProxyProvider); ok {
		_, hostError := filtered.hostState()
		return hostError
	}
	return ""
}

type providerStateTransactionEntry struct {
	provider *filteredProxyProvider
	cache    *providerCacheUpdate
}

type providerStateTransaction struct {
	entries []providerStateTransactionEntry
}

func prepareProviderStates(providers map[string]P.ProxyProvider) (*providerStateTransaction, error) {
	transaction := &providerStateTransaction{}
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		filtered, ok := providers[name].(*filteredProxyProvider)
		if !ok {
			continue
		}
		cache, staged, err := filtered.prepareCurrent()
		if !staged {
			continue
		}
		transaction.entries = append(transaction.entries, providerStateTransactionEntry{provider: filtered, cache: cache})
		if err != nil {
			return transaction, fmt.Errorf("stage accepted Provider %q cache: %w", name, err)
		}
	}
	return transaction, nil
}

func (t *providerStateTransaction) commit() error {
	if t == nil {
		return nil
	}
	for index := range t.entries {
		if err := t.entries[index].cache.commit(); err != nil {
			errorsFound := []error{err}
			for rollbackIndex := index - 1; rollbackIndex >= 0; rollbackIndex-- {
				if rollbackErr := t.entries[rollbackIndex].cache.rollback(); rollbackErr != nil {
					errorsFound = append(errorsFound, rollbackErr)
				}
			}
			for cleanupIndex := index; cleanupIndex < len(t.entries); cleanupIndex++ {
				t.entries[cleanupIndex].cache.finish()
			}
			return errors.Join(errorsFound...)
		}
	}
	return nil
}

func (t *providerStateTransaction) accept() {
	if t == nil {
		return
	}
	for index := range t.entries {
		t.entries[index].provider.acceptCurrent()
		t.entries[index].cache.finish()
	}
}

func (t *providerStateTransaction) reject(err error) error {
	if t == nil {
		return err
	}
	errorsFound := []error{err}
	for index := len(t.entries) - 1; index >= 0; index-- {
		if rollbackErr := t.entries[index].cache.rollback(); rollbackErr != nil {
			errorsFound = append(errorsFound, rollbackErr)
		}
	}
	for index := range t.entries {
		if rejectErr := t.entries[index].provider.rejectCurrent(err); rejectErr != nil {
			errorsFound = append(errorsFound, rejectErr)
		}
	}
	return errors.Join(errorsFound...)
}

func rejectProviderStates(providers map[string]P.ProxyProvider, keys map[string]struct{}, err error) error {
	errorsFound := []error{err}
	for key := range keys {
		if filtered, ok := providers[key].(*filteredProxyProvider); ok {
			if rejectErr := filtered.rejectCurrent(err); rejectErr != nil {
				errorsFound = append(errorsFound, rejectErr)
			}
		}
	}
	return errors.Join(errorsFound...)
}
