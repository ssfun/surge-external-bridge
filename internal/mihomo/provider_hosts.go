package mihomo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/metacubex/mihomo/component/resolver"
	"github.com/metacubex/mihomo/component/trie"
	C "github.com/metacubex/mihomo/constant"
	P "github.com/metacubex/mihomo/constant/provider"
	D "github.com/miekg/dns"
	"gopkg.in/yaml.v3"
)

type providerHostSource struct {
	inline       bool
	path         string
	acceptedPath string
	proxies      []map[string]any
	hosts        map[string]any
}

type providerCacheUpdate struct {
	acceptedPath   string
	candidatePath  string
	previousPath   string
	previousExists bool
	committed      bool
}

type providerHostMapping struct {
	pattern string
	value   resolver.HostValue
}

type hostTarget struct {
	domain string
	ips    []netip.Addr
}

type providerHostSet struct {
	mappings map[string]providerHostMapping
	expected map[string]hostTarget
}

func (s providerHostSet) clone() providerHostSet {
	clone := providerHostSet{
		mappings: make(map[string]providerHostMapping, len(s.mappings)),
		expected: make(map[string]hostTarget, len(s.expected)),
	}
	for key, mapping := range s.mappings {
		mapping.value.IPs = append([]netip.Addr(nil), mapping.value.IPs...)
		clone.mappings[key] = mapping
	}
	for server, target := range s.expected {
		target.ips = append([]netip.Addr(nil), target.ips...)
		clone.expected[server] = target
	}
	return clone
}

func (s *providerHostSet) addProxyAddresses(proxies []C.Proxy) {
	if s.expected == nil {
		s.expected = make(map[string]hostTarget)
	}
	for _, proxy := range proxies {
		if proxy == nil {
			continue
		}
		host, _, err := net.SplitHostPort(proxy.Addr())
		host = strings.ToLower(strings.TrimSuffix(host, "."))
		if err == nil && host != "" {
			if _, exists := s.expected[host]; !exists {
				s.expected[host] = hostTarget{domain: host}
			}
		}
	}
}

func newProviderHostSource(_ string, definition ProviderDefinition) providerHostSource {
	switch strings.ToLower(strings.TrimSpace(definition.Type)) {
	case "inline":
		return providerHostSource{inline: true, proxies: definition.Payload, hosts: definition.Hosts}
	case "file":
		return providerHostSource{path: C.Path.Resolve(definition.FilePath)}
	case "http", "":
		path := C.Path.GetPathByHash("proxies", definition.URL)
		return providerHostSource{path: path, acceptedPath: path + ".surgeeb-accepted"}
	default:
		return providerHostSource{}
	}
}

func (s providerHostSource) prepare() error {
	if s.acceptedPath == "" {
		return nil
	}
	if _, err := os.Stat(s.acceptedPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect accepted Provider cache: %w", err)
	}
	if err := replacePrivateFile(s.acceptedPath, s.path); err != nil {
		return fmt.Errorf("restore accepted Provider cache: %w", err)
	}
	return nil
}

func (s providerHostSource) accept() error {
	update, err := s.prepareAcceptance()
	if err != nil || update == nil {
		return err
	}
	if err := update.commit(); err != nil {
		_ = update.rollback()
		return err
	}
	update.finish()
	return nil
}

func (s providerHostSource) prepareAcceptance() (*providerCacheUpdate, error) {
	if s.acceptedPath == "" {
		return nil, nil
	}
	update := &providerCacheUpdate{acceptedPath: s.acceptedPath}
	candidatePath, err := copyPrivateFileToTemp(s.path, filepath.Dir(s.acceptedPath))
	if err != nil {
		return nil, fmt.Errorf("stage accepted Provider cache: %w", err)
	}
	update.candidatePath = candidatePath
	if _, err := os.Stat(s.acceptedPath); errors.Is(err, os.ErrNotExist) {
		return update, nil
	} else if err != nil {
		update.finish()
		return nil, fmt.Errorf("inspect previous accepted Provider cache: %w", err)
	}
	previousPath, err := copyPrivateFileToTemp(s.acceptedPath, filepath.Dir(s.acceptedPath))
	if err != nil {
		update.finish()
		return nil, fmt.Errorf("stage previous accepted Provider cache: %w", err)
	}
	update.previousPath = previousPath
	update.previousExists = true
	return update, nil
}

func (u *providerCacheUpdate) commit() error {
	if u == nil || u.candidatePath == "" {
		return nil
	}
	if err := os.Rename(u.candidatePath, u.acceptedPath); err != nil {
		return fmt.Errorf("save accepted Provider cache: %w", err)
	}
	u.candidatePath = ""
	u.committed = true
	return nil
}

func (u *providerCacheUpdate) rollback() error {
	if u == nil {
		return nil
	}
	var rollbackErr error
	if u.committed {
		if u.previousExists {
			if err := os.Rename(u.previousPath, u.acceptedPath); err != nil {
				rollbackErr = fmt.Errorf("restore previous accepted Provider cache: %w", err)
			} else {
				u.previousPath = ""
				u.committed = false
			}
		} else if err := os.Remove(u.acceptedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			rollbackErr = fmt.Errorf("remove uncommitted accepted Provider cache: %w", err)
		} else {
			u.committed = false
		}
	}
	if rollbackErr != nil {
		if u.candidatePath != "" {
			_ = os.Remove(u.candidatePath)
			u.candidatePath = ""
		}
		return rollbackErr
	}
	u.finish()
	return rollbackErr
}

func (u *providerCacheUpdate) finish() {
	if u == nil {
		return
	}
	if u.candidatePath != "" {
		_ = os.Remove(u.candidatePath)
		u.candidatePath = ""
	}
	if u.previousPath != "" {
		_ = os.Remove(u.previousPath)
		u.previousPath = ""
	}
}

func (s providerHostSource) reject() error {
	if s.acceptedPath == "" {
		return nil
	}
	if _, err := os.Stat(s.acceptedPath); errors.Is(err, os.ErrNotExist) {
		if removeErr := os.Remove(s.path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("discard rejected Provider cache: %w", removeErr)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect accepted Provider cache: %w", err)
	}
	if err := replacePrivateFile(s.acceptedPath, s.path); err != nil {
		return fmt.Errorf("roll back rejected Provider cache: %w", err)
	}
	return nil
}

func replacePrivateFile(source, target string) error {
	temporaryPath, err := copyPrivateFileToTemp(source, filepath.Dir(target))
	if err != nil {
		return err
	}
	defer os.Remove(temporaryPath)
	return os.Rename(temporaryPath, target)
}

func copyPrivateFileToTemp(source, directory string) (string, error) {
	data, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(source)
	if err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(directory, ".surgeeb-provider-cache-*")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return "", err
	}
	if _, err := temporary.Write(data); err != nil {
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if err := os.Chtimes(temporaryPath, info.ModTime(), info.ModTime()); err != nil {
		return "", err
	}
	keep = true
	return temporaryPath, nil
}

func (s providerHostSource) load() (providerHostSet, error) {
	if s.inline {
		return extractProviderHosts(s.proxies, s.hosts)
	}
	if strings.TrimSpace(s.path) == "" {
		return providerHostSet{}, nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return providerHostSet{}, fmt.Errorf("read Provider source for hosts: %w", err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil || !yamlDocumentHasKey(&root, "hosts") {
		// Mihomo Providers also accept URI lists and Base64 URI payloads. They do
		// not have a top-level hosts section, so leave their native parser as the
		// sole format authority instead of rejecting an otherwise valid source.
		return providerHostSet{mappings: map[string]providerHostMapping{}, expected: map[string]hostTarget{}}, nil
	}
	var document struct {
		Proxies []map[string]any `yaml:"proxies"`
		Hosts   map[string]any   `yaml:"hosts"`
	}
	if err := yaml.Unmarshal(data, &document); err != nil {
		return providerHostSet{}, fmt.Errorf("parse Provider source for hosts: %w", err)
	}
	return extractProviderHosts(document.Proxies, document.Hosts)
}

func yamlDocumentHasKey(document *yaml.Node, key string) bool {
	if document == nil || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return false
	}
	content := document.Content[0].Content
	for index := 0; index+1 < len(content); index += 2 {
		if content[index].Value == key {
			return true
		}
	}
	return false
}

type rawHostMapping struct {
	pattern string
	raw     any
}

func extractProviderHosts(proxies []map[string]any, rawHosts map[string]any) (providerHostSet, error) {
	result := providerHostSet{mappings: map[string]providerHostMapping{}, expected: map[string]hostTarget{}}
	servers := make(map[string]struct{})
	for _, proxy := range proxies {
		server, ok := proxy["server"].(string)
		server = strings.TrimSpace(server)
		if ok && server != "" {
			servers[strings.ToLower(strings.TrimSuffix(server, "."))] = struct{}{}
		}
	}
	if len(rawHosts) == 0 || len(servers) == 0 {
		for server := range servers {
			result.expected[server] = hostTarget{domain: server}
		}
		return result, nil
	}
	patterns := make([]string, 0, len(rawHosts))
	for pattern := range rawHosts {
		patterns = append(patterns, pattern)
	}
	sort.Strings(patterns)
	rawTrie := trie.New[rawHostMapping]()
	seenPatterns := make(map[string]string, len(patterns))
	for _, pattern := range patterns {
		canonical := strings.ToLower(pattern)
		if previous, exists := seenPatterns[canonical]; exists {
			return providerHostSet{}, fmt.Errorf("hosts patterns %q and %q differ only by case", previous, pattern)
		}
		if err := rawTrie.Insert(pattern, rawHostMapping{pattern: canonical, raw: rawHosts[pattern]}); err != nil {
			return providerHostSet{}, fmt.Errorf("invalid hosts pattern %q: %w", pattern, err)
		}
		seenPatterns[canonical] = pattern
	}

	orderedServers := make([]string, 0, len(servers))
	for server := range servers {
		orderedServers = append(orderedServers, server)
	}
	sort.Strings(orderedServers)
	for _, server := range orderedServers {
		target, err := collectHostChain(rawTrie, server, result.mappings)
		if err != nil {
			return providerHostSet{}, fmt.Errorf("hosts mapping for proxy server %q: %w", server, err)
		}
		result.expected[server] = target
	}
	return result, nil
}

func collectHostChain(rawTrie *trie.DomainTrie[rawHostMapping], server string, collected map[string]providerHostMapping) (hostTarget, error) {
	current := server
	seen := map[string]struct{}{}
	for steps := 0; steps <= 64; steps++ {
		canonical := strings.ToLower(strings.TrimSuffix(current, "."))
		if _, exists := seen[canonical]; exists {
			return hostTarget{}, errors.New("domain alias cycle")
		}
		seen[canonical] = struct{}{}
		node := rawTrie.Search(canonical)
		if node == nil {
			return hostTarget{domain: canonical}, nil
		}
		raw := node.Data()
		mapping, exists := collected[raw.pattern]
		if !exists {
			value, err := normalizeProviderHostValue(raw.raw)
			if err != nil {
				return hostTarget{}, fmt.Errorf("%s: %w", raw.pattern, err)
			}
			mapping = providerHostMapping{pattern: raw.pattern, value: value}
			collected[raw.pattern] = mapping
		}
		if !mapping.value.IsDomain {
			return hostTarget{ips: append([]netip.Addr(nil), mapping.value.IPs...)}, nil
		}
		current = mapping.value.Domain
	}
	return hostTarget{}, errors.New("domain alias chain is too deep")
}

func normalizeProviderHostValue(raw any) (resolver.HostValue, error) {
	values := make([]string, 0)
	switch typed := raw.(type) {
	case string:
		values = append(values, typed)
	case []string:
		values = append(values, typed...)
	case []any:
		for _, item := range typed {
			value, ok := item.(string)
			if !ok {
				return resolver.HostValue{}, errors.New("value must be a hostname, IP, or IP array")
			}
			values = append(values, value)
		}
	default:
		return resolver.HostValue{}, errors.New("value must be a hostname, IP, or IP array")
	}
	value, err := resolver.NewHostValue(values)
	if err != nil {
		return resolver.HostValue{}, err
	}
	if value.IsDomain {
		value.Domain = strings.ToLower(value.Domain)
	}
	return value, nil
}

func buildProviderHostResolver(cfgProviders map[string]P.ProxyProvider, definitions []ProviderDefinition) (*providerHostResolver, map[string]int, error) {
	merged := make(map[string]providerHostMapping)
	owners := make(map[string]string)
	counts := make(map[string]int, len(definitions))
	expected := make(map[string]hostTarget)
	for _, definition := range definitions {
		key, err := ProviderKey(definition.StableID)
		if err != nil {
			return nil, nil, err
		}
		provider, ok := cfgProviders[key].(*filteredProxyProvider)
		if !ok {
			continue
		}
		state := provider.candidateState().hosts
		_, hostError := provider.hostState()
		if hostError != "" && stateEmpty(state) {
			return nil, nil, fmt.Errorf("Provider %q hosts: %s", definition.Name, hostError)
		}
		counts[definition.StableID] = len(state.mappings)
		for pattern, mapping := range state.mappings {
			if current, exists := merged[pattern]; exists && !equalHostValue(current.value, mapping.value) {
				return nil, nil, fmt.Errorf("hosts mapping %q conflicts between Providers %q and %q", pattern, owners[pattern], definition.Name)
			}
			merged[pattern] = mapping
			owners[pattern] = definition.Name
		}
		for server, target := range state.expected {
			if current, exists := expected[server]; exists && !equalHostTarget(current, target) {
				return nil, nil, fmt.Errorf("proxy server %q resolves differently across Providers", server)
			}
			expected[server] = target
		}
	}
	hostResolver, err := newProviderHostResolver(merged)
	if err != nil {
		return nil, nil, err
	}
	for server, want := range expected {
		got, err := hostResolver.resolve(server)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve proxy server %q: %w", server, err)
		}
		if !equalHostTarget(got, want) {
			return nil, nil, fmt.Errorf("hosts mappings from another Provider change proxy server %q", server)
		}
	}
	return hostResolver, counts, nil
}

func stateEmpty(state providerHostSet) bool {
	return len(state.mappings) == 0 && len(state.expected) == 0
}

func equalHostValue(left, right resolver.HostValue) bool {
	return left.IsDomain == right.IsDomain && left.Domain == right.Domain && equalIPs(left.IPs, right.IPs)
}

func equalHostTarget(left, right hostTarget) bool {
	return left.domain == right.domain && equalIPs(left.ips, right.ips)
}

func equalIPs(left, right []netip.Addr) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

type providerHostResolver struct {
	hosts atomic.Pointer[trie.DomainTrie[resolver.HostValue]]
}

var sharedProviderHostResolver = func() *providerHostResolver {
	hostResolver, err := newProviderHostResolver(nil)
	if err != nil {
		panic(err)
	}
	return hostResolver
}()

func newProviderHostResolver(mappings map[string]providerHostMapping) (*providerHostResolver, error) {
	tree := trie.New[resolver.HostValue]()
	patterns := make([]string, 0, len(mappings))
	for pattern := range mappings {
		patterns = append(patterns, pattern)
	}
	sort.Strings(patterns)
	for _, pattern := range patterns {
		if err := tree.Insert(pattern, mappings[pattern].value); err != nil {
			return nil, err
		}
	}
	tree.Optimize()
	hostResolver := &providerHostResolver{}
	hostResolver.hosts.Store(tree)
	return hostResolver, nil
}

func (r *providerHostResolver) replace(candidate *providerHostResolver) {
	if candidate == nil {
		tree := trie.New[resolver.HostValue]()
		tree.Optimize()
		r.hosts.Store(tree)
		return
	}
	r.hosts.Store(candidate.hosts.Load())
}

func (r *providerHostResolver) resolve(host string) (hostTarget, error) {
	current := strings.ToLower(strings.TrimSuffix(host, "."))
	seen := map[string]struct{}{}
	hosts := r.hosts.Load()
	for steps := 0; steps <= 64; steps++ {
		if _, exists := seen[current]; exists {
			return hostTarget{}, errors.New("domain alias cycle")
		}
		seen[current] = struct{}{}
		if hosts == nil {
			return hostTarget{domain: current}, nil
		}
		node := hosts.Search(current)
		if node == nil {
			return hostTarget{domain: current}, nil
		}
		value := node.Data()
		if !value.IsDomain {
			return hostTarget{ips: append([]netip.Addr(nil), value.IPs...)}, nil
		}
		current = strings.ToLower(value.Domain)
	}
	return hostTarget{}, errors.New("domain alias chain is too deep")
}

func (r *providerHostResolver) lookup(ctx context.Context, host string, family int) ([]netip.Addr, error) {
	target, err := r.resolve(host)
	if err != nil {
		return nil, err
	}
	if len(target.ips) > 0 {
		filtered := make([]netip.Addr, 0, len(target.ips))
		for _, ip := range target.ips {
			if family == 0 || family == 4 && ip.Is4() || family == 6 && ip.Is6() {
				filtered = append(filtered, ip)
			}
		}
		if len(filtered) == 0 {
			return nil, resolver.ErrIPVersion
		}
		return filtered, nil
	}
	switch family {
	case 4:
		return resolver.SystemResolver.LookupIPv4(ctx, target.domain)
	case 6:
		return resolver.SystemResolver.LookupIPv6(ctx, target.domain)
	default:
		return resolver.SystemResolver.LookupIP(ctx, target.domain)
	}
}

func (r *providerHostResolver) LookupIP(ctx context.Context, host string) ([]netip.Addr, error) {
	return r.lookup(ctx, host, 0)
}
func (r *providerHostResolver) LookupIPv4(ctx context.Context, host string) ([]netip.Addr, error) {
	return r.lookup(ctx, host, 4)
}
func (r *providerHostResolver) LookupIPv6(ctx context.Context, host string) ([]netip.Addr, error) {
	return r.lookup(ctx, host, 6)
}
func (r *providerHostResolver) ResolveECH(ctx context.Context, host string) ([]byte, error) {
	// hosts changes only the transport address. ECH, like TLS SNI, remains
	// bound to the Provider's original server name.
	return resolver.SystemResolver.ResolveECH(ctx, host)
}
func (r *providerHostResolver) ExchangeContext(ctx context.Context, message *D.Msg) (*D.Msg, error) {
	return resolver.SystemResolver.ExchangeContext(ctx, message)
}
func (r *providerHostResolver) Invalid() bool    { return true }
func (r *providerHostResolver) ClearCache()      {}
func (r *providerHostResolver) ResetConnection() {}
