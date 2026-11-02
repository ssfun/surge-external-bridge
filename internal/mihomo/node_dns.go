package mihomo

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/metacubex/mihomo/component/resolver"
	MDNS "github.com/metacubex/mihomo/dns"
)

// NodeDNSConfig is a product-owned outbound-node policy, never a Mihomo DNS
// server configuration. Empty Nameservers deliberately retains system DNS.
type NodeDNSConfig struct {
	Nameservers          []string      `json:"nameservers"`
	BootstrapNameservers []string      `json:"bootstrap_nameservers"`
	Rules                []NodeDNSRule `json:"rules"`
}

type NodeDNSRule struct {
	Domain      string   `json:"domain"`
	Nameservers []string `json:"nameservers"`
}

func (c NodeDNSConfig) Clone() NodeDNSConfig {
	c.Nameservers = append([]string(nil), c.Nameservers...)
	c.BootstrapNameservers = append([]string(nil), c.BootstrapNameservers...)
	c.Rules = append([]NodeDNSRule(nil), c.Rules...)
	for i := range c.Rules {
		c.Rules[i].Nameservers = append([]string(nil), c.Rules[i].Nameservers...)
	}
	return c
}

type nodeDNSRule struct {
	domain   string
	kind     byte // 0 exact, '*' descendants only, '+' apex and descendants
	resolver resolver.Resolver
}

type nodeDNSPolicy struct {
	fallback resolver.Resolver
	rules    []nodeDNSRule // exact first; then most-specific suffix, '*' before '+' on ties
}

type parsedNodeDNS struct {
	main, bootstrap []MDNS.NameServer
	rules           [][]MDNS.NameServer
	domains         []string
	kinds           []byte
}

func ValidateNodeDNS(c NodeDNSConfig) error { _, err := parseNodeDNS(c); return err }

func parseNodeDNS(c NodeDNSConfig) (parsedNodeDNS, error) {
	var parsed parsedNodeDNS
	if len(c.Rules) > 256 {
		return parsed, errors.New("node DNS supports at most 256 domain rules")
	}
	parseList := func(values []string, bootstrap bool) ([]MDNS.NameServer, error) {
		if len(values) > 16 {
			return nil, errors.New("node DNS supports at most 16 nameservers per list")
		}
		servers := make([]MDNS.NameServer, 0, len(values))
		seen := map[string]bool{}
		for _, value := range values {
			server, err := parseNodeNameserver(value, bootstrap)
			if err != nil {
				return nil, err
			}
			key := server.Net + "://" + server.Addr
			if seen[key] {
				return nil, errors.New("duplicate node DNS nameserver")
			}
			seen[key] = true
			servers = append(servers, server)
		}
		return servers, nil
	}
	var err error
	if parsed.bootstrap, err = parseList(c.BootstrapNameservers, true); err != nil {
		return parsed, fmt.Errorf("node DNS bootstrap: %w", err)
	}
	if parsed.main, err = parseList(c.Nameservers, false); err != nil {
		return parsed, fmt.Errorf("node DNS default: %w", err)
	}
	seen := map[string]bool{}
	for i, rule := range c.Rules {
		domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(rule.Domain), "."))
		kind := byte(0)
		if strings.HasPrefix(domain, "*.") || strings.HasPrefix(domain, "+.") {
			kind = domain[0]
			domain = domain[2:]
		}
		if !validNodeDNSDomain(domain) {
			return parsed, fmt.Errorf("node DNS rule %d: use an exact domain, *.domain, or +.domain", i+1)
		}
		key := string(kind) + domain
		if seen[key] {
			return parsed, fmt.Errorf("node DNS rule %d: duplicate domain pattern", i+1)
		}
		seen[key] = true
		if len(rule.Nameservers) == 0 {
			return parsed, fmt.Errorf("node DNS rule %d: at least one nameserver is required", i+1)
		}
		servers, err := parseList(rule.Nameservers, false)
		if err != nil {
			return parsed, fmt.Errorf("node DNS rule %d: %w", i+1, err)
		}
		parsed.rules = append(parsed.rules, servers)
		parsed.domains = append(parsed.domains, domain)
		parsed.kinds = append(parsed.kinds, kind)
	}
	if len(parsed.bootstrap) == 0 {
		lists := append([][]MDNS.NameServer{parsed.main}, parsed.rules...)
		for _, list := range lists {
			for _, server := range list {
				host := ""
				if server.Net == "https" {
					u, _ := url.Parse(server.Addr)
					host = u.Hostname()
				} else {
					host, _, _ = net.SplitHostPort(server.Addr)
				}
				if _, err := netip.ParseAddr(host); err != nil {
					return parsed, errors.New("node DNS encrypted hostname upstreams require a literal-IP UDP/TCP bootstrap nameserver")
				}
			}
		}
	}
	return parsed, nil
}

func validNodeDNSDomain(domain string) bool {
	if len(domain) == 0 || len(domain) > 253 {
		return false
	}
	if _, err := netip.ParseAddr(domain); err == nil {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func parseNodeNameserver(raw string, bootstrap bool) (MDNS.NameServer, error) {
	invalid := func() (MDNS.NameServer, error) {
		return MDNS.NameServer{}, errors.New("use an IP[:port], udp://IP[:port], tcp://IP[:port], tls://host[:port], or https://host[:port]/dns-query; credentials, query strings, fragments and proxy options are not supported")
	}
	if raw == "" || raw != strings.TrimSpace(raw) || strings.ContainsAny(raw, "\r\n\t\\") {
		return invalid()
	}
	value := raw
	if !strings.Contains(value, "://") {
		if ip, err := netip.ParseAddr(value); err == nil {
			value = net.JoinHostPort(ip.String(), "53")
		}
		value = "udp://" + value
	}
	u, err := url.Parse(value)
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.Host == "" || u.RawPath != "" {
		return invalid()
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "udp" && scheme != "tcp" && scheme != "tls" && scheme != "https" {
		return invalid()
	}
	if bootstrap && scheme != "udp" && scheme != "tcp" {
		return MDNS.NameServer{}, errors.New("bootstrap must use literal-IP UDP or TCP")
	}
	host := strings.ToLower(u.Hostname())
	ip, ipErr := netip.ParseAddr(host)
	if ipErr != nil && (scheme == "udp" || scheme == "tcp" || !validNodeDNSDomain(host)) {
		return invalid()
	}
	if ipErr == nil && (ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast()) {
		return invalid()
	}
	port := u.Port()
	if port == "" {
		if strings.HasSuffix(u.Host, ":") {
			return invalid()
		}
		switch scheme {
		case "tls":
			port = "853"
		case "https":
			port = "443"
		default:
			port = "53"
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return invalid()
	}
	// Reject unbracketed IPv6 and other ambiguous authority spellings.
	if strings.Count(u.Host, ":") > 1 && !strings.HasPrefix(u.Host, "[") {
		return invalid()
	}
	address := net.JoinHostPort(host, strconv.Itoa(n))
	if scheme == "https" {
		if u.Path == "" {
			u.Path = "/dns-query"
		}
		if !strings.HasPrefix(u.Path, "/") {
			return invalid()
		}
		u.Scheme = scheme
		u.Host = address
		address = u.String()
	} else if u.Path != "" {
		return invalid()
	}
	return MDNS.NameServer{Net: scheme, Addr: address}, nil
}

func compileNodeDNS(c NodeDNSConfig) (*nodeDNSPolicy, error) {
	parsed, err := parseNodeDNS(c)
	if err != nil {
		return nil, err
	}
	policy := &nodeDNSPolicy{}
	makeResolver := func(servers []MDNS.NameServer) resolver.Resolver {
		if len(servers) == 0 {
			return nil
		}
		return MDNS.NewResolver(MDNS.Config{Main: servers, Default: parsed.bootstrap, IPv6: true, CacheMaxSize: 1024}).Resolver
	}
	policy.fallback = makeResolver(parsed.main)
	for i, servers := range parsed.rules {
		policy.rules = append(policy.rules, nodeDNSRule{domain: parsed.domains[i], kind: parsed.kinds[i], resolver: makeResolver(servers)})
	}
	sort.Slice(policy.rules, func(i, j int) bool {
		a, b := policy.rules[i], policy.rules[j]
		if (a.kind == 0) != (b.kind == 0) {
			return a.kind == 0
		}
		if len(a.domain) != len(b.domain) {
			return len(a.domain) > len(b.domain)
		}
		if a.domain != b.domain {
			return a.domain < b.domain
		}
		return a.kind < b.kind // '*' before '+'
	})
	return policy, nil
}

func (p *nodeDNSPolicy) forDomain(host string) resolver.Resolver {
	if p == nil {
		return resolver.SystemResolver
	}
	for _, rule := range p.rules {
		if host == rule.domain && rule.kind != '*' || rule.kind != 0 && strings.HasSuffix(host, "."+rule.domain) {
			return rule.resolver
		}
	}
	if p.fallback != nil {
		return p.fallback
	}
	return resolver.SystemResolver
}
