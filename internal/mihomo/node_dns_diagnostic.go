package mihomo

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/metacubex/mihomo/component/resolver"
)

var ErrDiagnosticNodeNotFound = errors.New("Node not found")

type DNSFamilyResult struct {
	Source     string   `json:"source"`
	Addresses  []string `json:"addresses"`
	Error      string   `json:"error"`
	DurationMS int64    `json:"duration_ms"`
}

type NodeDNSDiagnostic struct {
	NodeID               string          `json:"node_id"`
	Name                 string          `json:"name"`
	TestedAt             time.Time       `json:"tested_at"`
	Hostname             string          `json:"hostname"`
	EffectiveDomain      string          `json:"effective_domain"`
	Aliases              []string        `json:"aliases"`
	Source               string          `json:"source"`
	MatchedRule          string          `json:"matched_rule"`
	Nameservers          []string        `json:"nameservers"`
	BootstrapNameservers []string        `json:"bootstrap_nameservers"`
	CacheStatus          string          `json:"cache_status"`
	CacheNote            string          `json:"cache_note"`
	BypassRequested      bool            `json:"bypass_requested"`
	BypassApplied        bool            `json:"bypass_applied"`
	IPv4                 DNSFamilyResult `json:"ipv4"`
	IPv6                 DNSFamilyResult `json:"ipv6"`
	DurationMS           int64           `json:"duration_ms"`
}

// DiagnoseNodeDNS captures one accepted node/hosts/policy generation. Network
// work happens after releasing the lock; later reloads cannot alter this result.
func (m *Manager) DiagnoseNodeDNS(ctx context.Context, id string, bypass bool) (NodeDNSDiagnostic, error) {
	// Publication uses applyMu, including provider refresh. Respect cancellation
	// while waiting for an in-progress reload instead of blocking past timeout.
	for !m.applyMu.TryLock() {
		select {
		case <-ctx.Done():
			return NodeDNSDiagnostic{}, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := ctx.Err(); err != nil {
		m.applyMu.Unlock()
		return NodeDNSDiagnostic{}, err
	}
	entry, ok := m.store.Load().EntryByID(strings.TrimSpace(id))
	snapshot := m.hostResolver.state.Load()
	var host string
	var err error
	if ok {
		host, _, err = net.SplitHostPort(entry.Proxy.Addr())
	}
	m.applyMu.Unlock()
	if !ok {
		return NodeDNSDiagnostic{}, ErrDiagnosticNodeNotFound
	}
	if err != nil {
		return NodeDNSDiagnostic{}, err
	}
	started := time.Now()
	result := NodeDNSDiagnostic{NodeID: entry.PublicID, Name: entry.DisplayName, TestedAt: started.UTC(), Hostname: host, Aliases: []string{}, Nameservers: []string{}, BootstrapNameservers: []string{}, BypassRequested: bypass, CacheStatus: "not_applicable"}
	native4, native6 := []string{}, []string{}
	if native, found := resolver.DefaultHosts.Search(host, false); found {
		for _, ip := range native.IPs {
			if ip.Is4() {
				native4 = append(native4, ip.String())
			} else {
				native6 = append(native6, ip.String())
			}
		}
	}
	if len(native4) > 0 && len(native6) > 0 {
		result.Source = "native_hosts"
		result.IPv4 = DNSFamilyResult{Addresses: native4, Source: "native_hosts"}
		result.IPv6 = DNSFamilyResult{Addresses: native6, Source: "native_hosts"}
		result.DurationMS = time.Since(started).Milliseconds()
		return result, nil
	}
	target, err := traceProviderHost(snapshot, host, &result.Aliases)
	if err != nil {
		return result, err
	}
	result.EffectiveDomain = target.domain
	if ip, parseErr := netip.ParseAddr(host); parseErr == nil {
		target = hostTarget{ips: []netip.Addr{ip.Unmap()}}
		result.Source = "literal"
	} else if len(target.ips) > 0 {
		result.Source = "hosts"
	}
	result.IPv4.Addresses, result.IPv6.Addresses = []string{}, []string{}
	if len(target.ips) > 0 {
		for _, ip := range target.ips {
			if ip.Is4() {
				result.IPv4.Addresses = append(result.IPv4.Addresses, ip.String())
			} else {
				result.IPv6.Addresses = append(result.IPv6.Addresses, ip.String())
			}
		}
		result.IPv4.Source, result.IPv6.Source = result.Source, result.Source
		if len(native4) > 0 {
			result.IPv4.Addresses, result.IPv4.Source = native4, "native_hosts"
			result.Source = "mixed_hosts"
		}
		if len(native6) > 0 {
			result.IPv6.Addresses, result.IPv6.Source = native6, "native_hosts"
			result.Source = "mixed_hosts"
		}
		if len(result.IPv4.Addresses) == 0 {
			result.IPv4.Error = resolver.ErrIPVersion.Error()
		}
		if len(result.IPv6.Addresses) == 0 {
			result.IPv6.Error = resolver.ErrIPVersion.Error()
		}
		result.DurationMS = time.Since(started).Milliseconds()
		return result, nil
	}
	var policy *nodeDNSPolicy
	if snapshot != nil {
		policy = snapshot.dns
	}
	selected, source, rule, nameservers := policy.selectDomain(target.domain)
	result.Source, result.MatchedRule = source, rule
	result.Nameservers = append(result.Nameservers, nameservers...)
	if policy != nil {
		result.BootstrapNameservers = append(result.BootstrapNameservers, policy.config.BootstrapNameservers...)
	}
	result.CacheStatus = "unknown"
	result.CacheNote = "Mihomo does not expose cache hits or the winning upstream; nameservers are the selected configured group."
	if bypass && source != "system" {
		fresh, compileErr := compileNodeDNS(NodeDNSConfig{Nameservers: nameservers, BootstrapNameservers: result.BootstrapNameservers})
		if compileErr != nil {
			return result, compileErr
		}
		selected = fresh.fallback
		defer selected.ResetConnection()
		result.BypassApplied, result.CacheStatus = true, "bypassed"
		result.CacheNote = "Fresh isolated resolver bypasses the application cache; upstream and OS bootstrap caches may still apply. Production cache is unchanged."
	} else if source == "system" {
		result.CacheNote = "System resolver cache state is unknown; OS/upstream cache bypass is unavailable."
	}
	query := func(family int) DNSFamilyResult {
		start := time.Now()
		part := DNSFamilyResult{Addresses: []string{}, Source: source}
		if family == 4 && len(native4) > 0 {
			part.Addresses, part.Source = native4, "native_hosts"
			return part
		}
		if family == 6 && len(native6) > 0 {
			part.Addresses, part.Source = native6, "native_hosts"
			return part
		}
		var ips []netip.Addr
		var queryErr error
		if family == 4 {
			ips, queryErr = selected.LookupIPv4(ctx, target.domain)
		} else {
			ips, queryErr = selected.LookupIPv6(ctx, target.domain)
		}
		for _, ip := range ips {
			part.Addresses = append(part.Addresses, ip.String())
		}
		if queryErr != nil {
			part.Error = queryErr.Error()
		}
		part.DurationMS = time.Since(start).Milliseconds()
		return part
	}
	v4, v6 := make(chan DNSFamilyResult, 1), make(chan DNSFamilyResult, 1)
	go func() { v4 <- query(4) }()
	go func() { v6 <- query(6) }()
	result.IPv4, result.IPv6 = <-v4, <-v6
	if len(native4) > 0 || len(native6) > 0 {
		result.Source = "mixed_hosts"
	}
	result.DurationMS = time.Since(started).Milliseconds()
	return result, nil
}
