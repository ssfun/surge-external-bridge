package management

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/metacubex/mihomo/component/ca"
	"github.com/metacubex/mihomo/component/resolver"
	MDNS "github.com/metacubex/mihomo/dns"
	D "github.com/miekg/dns"
	"github.com/ssfun/surge-external-bridge/internal/gateway"
	"golang.org/x/net/proxy"
)

// This is deliberately opt-in: the complete E2E is run once implementation is ready.
func TestNodeDNSEndToEnd(t *testing.T) {
	if os.Getenv("SURGEEB_NODE_DNS_E2E") != "1" {
		t.Skip("set SURGEEB_NODE_DNS_E2E=1 to run private DNS/SOCKS E2E")
	}
	var mu sync.Mutex
	logs := []map[string]any{}
	results := []string{}
	record := func(label, domain string, kind uint16) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, map[string]any{"upstream": label, "domain": domain, "qtype": D.TypeToString[kind]})
	}
	fixture := func(label string, fail bool) (string, string) {
		handler := D.HandlerFunc(func(w D.ResponseWriter, req *D.Msg) {
			q := req.Question[0]
			record(label, q.Name, q.Qtype)
			reply := new(D.Msg)
			reply.SetReply(req)
			if fail {
				reply.Rcode = D.RcodeNameError
			} else if q.Qtype == D.TypeA {
				reply.Answer = []D.RR{&D.A{Hdr: D.RR_Header{Name: q.Name, Rrtype: D.TypeA, Class: D.ClassINET, Ttl: 60}, A: net.ParseIP("127.0.0.1")}}
			}
			_ = w.WriteMsg(reply)
		})
		udp, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		tcp, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		us := &D.Server{PacketConn: udp, Handler: handler}
		ts := &D.Server{Listener: tcp, Handler: handler}
		go us.ActivateAndServe()
		go ts.ActivateAndServe()
		t.Cleanup(func() { _ = us.Shutdown(); _ = ts.Shutdown() })
		return "udp://" + udp.LocalAddr().String(), "tcp://" + tcp.Addr().String()
	}
	defaultNS, tcpNS := fixture("default", false)
	exactNS, _ := fixture("exact", false)
	wildcardNS, _ := fixture("wildcard", false)
	suffixNS, _ := fixture("suffix", false)
	narrowNS, _ := fixture("narrow", false)
	failNS, _ := fixture("failure", true)
	systemNS, _ := fixture("system", false)
	bootstrapNS, bootstrapTCP := fixture("bootstrap", false)
	application, _, endpoint := testManagementServer(t, "node-dns-e2e-token-123456")
	defer func() { _ = application.Close() }()
	oldSystem := resolver.SystemResolver
	resolver.SystemResolver = MDNS.NewResolver(MDNS.Config{Main: []MDNS.NameServer{{Net: "udp", Addr: strings.TrimPrefix(systemNS, "udp://")}}, IPv6: true}).Resolver
	defer func() { resolver.SystemResolver = oldSystem }()

	artifact := map[string]any{"test": "TestNodeDNSEndToEnd", "scope": "private loopback fixtures; OS DNS unchanged"}
	defer func() {
		mu.Lock()
		artifact["queries"] = append([]map[string]any(nil), logs...)
		mu.Unlock()
		artifact["passed_scenarios"] = results
		artifact["passed"] = !t.Failed()
		name := os.Getenv("SURGEEB_NODE_DNS_ARTIFACT")
		if name == "" {
			name = "/tmp/surgeeb-node-dns-e2e.json"
		}
		data, _ := json.MarshalIndent(artifact, "", "  ")
		if err := os.WriteFile(name, append(data, '\n'), 0600); err != nil {
			t.Error(err)
		}
		t.Logf("artifact: %s", name)
	}()
	request := func(method, path string, body any) (int, []byte) {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest(method, endpoint+path, bytes.NewReader(encoded))
		req.Header.Set("Authorization", "Bearer node-dns-e2e-token-123456")
		req.Header.Set("Origin", endpoint)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, data
	}
	settings := func() map[string]any {
		code, data := request("GET", "/api/settings", nil)
		if code != 200 {
			t.Fatalf("settings: %d %s", code, data)
		}
		var dto gateway.Settings
		if err := json.Unmarshal(data, &dto); err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(dto)
		var value map[string]any
		_ = json.Unmarshal(encoded, &value)
		return value
	}
	putDNS := func(dns map[string]any, wantOK bool) {
		body := settings()
		body["node_dns"] = dns
		code, data := request("PUT", "/api/settings", body)
		if (code == 200) != wantOK {
			t.Fatalf("DNS settings result: %d %s", code, data)
		}
	}
	rule := func(domain, ns string) map[string]any {
		return map[string]any{"domain": domain, "nameservers": []string{ns}}
	}
	policy := map[string]any{"nameservers": []string{defaultNS}, "bootstrap_nameservers": []string{bootstrapNS}, "rules": []any{rule("+.example.test", suffixNS), rule("*.example.test", wildcardNS), rule("*.deep.example.test", narrowNS), rule("one.example.test", exactNS), rule("failed.example.test", failNS)}}
	putDNS(policy, true)
	artifact["node_dns"] = policy

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "node-dns-e2e-ok") }))
	defer target.Close()
	targetAddr := strings.TrimPrefix(target.URL, "http://")
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		for {
			conn, err := upstream.Accept()
			if err != nil {
				return
			}
			go nodeDNSSOCKSFixture(conn, targetAddr)
		}
	}()
	_, port, _ := net.SplitHostPort(upstream.Addr().String())
	var numericPort int
	_, _ = fmt.Sscan(port, &numericPort)
	addNode := func(name, host string, hosts map[string]any) gateway.Provider {
		provider, err := application.AddProvider(gateway.Provider{Name: name, Type: "inline", Enabled: true, Payload: gateway.InlinePayload{{"name": name, "type": "socks5", "server": host, "port": numericPort}}, Hosts: hosts})
		if err != nil {
			t.Fatal(err)
		}
		return provider
	}
	nodes := map[string]string{"exact": "one.example.test", "deep": "a.b.deep.example.test", "wild": "a.b.example.test", "apex": "example.test", "default": "elsewhere.test", "failed": "failed.example.test", "hosts": "hosts.example.test", "alias": "alias.example.test", "system": "system.test"}
	providers := map[string]gateway.Provider{}
	for name, host := range nodes {
		var hosts map[string]any
		if name == "hosts" {
			hosts = map[string]any{host: "127.0.0.1"}
		}
		if name == "alias" {
			hosts = map[string]any{host: "one.example.test"}
		}
		providers[name] = addNode(name, host, hosts)
	}
	count := func(label, domain string) int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, q := range logs {
			if q["upstream"] == label && q["domain"] == domain+"." {
				n++
			}
		}
		return n
	}
	// Diagnostic failure scenarios (written before implementation): wrong rule after
	// alias, hosts leaking DNS, cache bypass reusing production cache, arbitrary
	// host injection, missing/deleted node, unauthenticated or cross-origin access.
	nodeID := func(name string) string {
		for _, entry := range application.Snapshot().Entries() {
			if entry.ProxyName == name {
				return entry.PublicID
			}
		}
		t.Fatalf("missing diagnostic node %s", name)
		return ""
	}
	diagnostics := []map[string]any{}
	diagnose := func(name string, bypass bool) map[string]any {
		code, raw := request("POST", "/api/nodes/"+nodeID(name)+"/dns-diagnose", map[string]any{"bypass_cache": bypass})
		if code != 200 {
			t.Fatalf("diagnostic %s: %d %s", name, code, raw)
		}
		var result map[string]any
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		diagnostics = append(diagnostics, result)
		artifact["diagnostics"] = diagnostics
		return result
	}
	for _, tc := range []struct{ name, source, rule string }{
		{"exact", "rule", "one.example.test"}, {"deep", "rule", "*.deep.example.test"},
		{"wild", "rule", "*.example.test"}, {"apex", "rule", "+.example.test"},
		{"default", "default", ""}, {"alias", "rule", "one.example.test"}, {"hosts", "hosts", ""},
	} {
		got := diagnose(tc.name, true)
		if got["hostname"] != nodes[tc.name] || got["source"] != tc.source || got["matched_rule"] != tc.rule {
			t.Fatalf("diagnostic selection: %#v", got)
		}
		if len(got["ipv4"].(map[string]any)["addresses"].([]any)) == 0 {
			t.Fatalf("no IPv4 result: %#v", got)
		}
	}
	before := count("exact", nodes["exact"])
	got := diagnose("exact", true)
	if count("exact", nodes["exact"]) <= before || got["cache_status"] != "bypassed" {
		t.Fatal("bypass did not query fixture")
	}
	got = diagnose("exact", false)
	if got["cache_status"] != "unknown" {
		t.Fatal("invented cache hit")
	}
	countA := func() int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, q := range logs {
			if q["upstream"] == "exact" && q["domain"] == nodes["exact"]+"." && q["qtype"] == "A" {
				n++
			}
		}
		return n
	}
	cachedA := countA()
	diagnose("exact", false)
	if countA() != cachedA {
		t.Fatal("normal diagnostic did not reuse live A cache")
	}
	diagnose("exact", true)
	if countA() <= cachedA {
		t.Fatal("bypass did not bypass live A cache")
	}
	bypassedA := countA()
	diagnose("exact", false)
	if countA() != bypassedA {
		t.Fatal("bypass disturbed production A cache")
	}
	localhost := addNode("diagnostic-localhost", "localhost", nil)
	got = diagnose("diagnostic-localhost", false)
	if got["ipv4"].(map[string]any)["source"] != "native_hosts" || got["ipv4"].(map[string]any)["addresses"].([]any)[0] != "127.0.0.1" {
		t.Fatalf("native hosts: %#v", got)
	}
	if err := application.DeleteProvider(localhost.StableID); err != nil {
		t.Fatal(err)
	}
	if count("wildcard", nodes["hosts"]) != 0 {
		t.Fatal("hosts diagnostic leaked DNS")
	}
	got = diagnose("failed", true)
	if got["ipv4"].(map[string]any)["error"] == "" || count("system", nodes["failed"]) != 0 {
		t.Fatal("diagnostic failure fallback")
	}
	path := "/api/nodes/" + nodeID("exact") + "/dns-diagnose"
	if code, _ := request("POST", path, map[string]any{"hostname": "injected.test"}); code != 400 {
		t.Fatalf("arbitrary host accepted: %d", code)
	}
	if code, _ := request("POST", "/api/nodes/missing/dns-diagnose", map[string]any{}); code != 404 {
		t.Fatalf("missing node: %d", code)
	}
	for _, tc := range []struct {
		token, origin string
		status        int
	}{{"", endpoint, 401}, {"node-dns-e2e-token-123456", "https://invalid.test", 403}} {
		req, _ := http.NewRequest("POST", endpoint+path, strings.NewReader(`{}`))
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		req.Header.Set("Origin", tc.origin)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.status {
			t.Fatalf("diagnostic protection: %d", resp.StatusCode)
		}
	}
	removed := addNode("diagnostic-deleted", "deleted.test", nil)
	removedID := nodeID("diagnostic-deleted")
	if err := application.DeleteProvider(removed.StableID); err != nil {
		t.Fatal(err)
	}
	if code, _ := request("POST", "/api/nodes/"+removedID+"/dns-diagnose", map[string]any{}); code != 404 {
		t.Fatalf("deleted node: %d", code)
	}
	literal := addNode("diagnostic-literal", "127.0.0.1", nil)
	got = diagnose("diagnostic-literal", true)
	if got["source"] != "literal" || got["cache_status"] != "not_applicable" {
		t.Fatalf("literal: %#v", got)
	}
	if err := application.DeleteProvider(literal.StableID); err != nil {
		t.Fatal(err)
	}
	putDNS(map[string]any{}, true)
	got = diagnose("system", true)
	if got["source"] != "system" || got["cache_status"] != "unknown" || got["bypass_applied"] != false {
		t.Fatalf("system bypass claim: %#v", got)
	}
	putDNS(policy, true)
	cancelled, cancelDiagnostic := context.WithCancel(context.Background())
	cancelDiagnostic()
	if _, err := application.DiagnoseNodeDNS(cancelled, nodeID("exact"), true); err == nil {
		t.Fatal("cancelled diagnostic performed work")
	}
	results = append(results, "node DNS diagnostic selection, bypass, failure isolation, authorization and deletion")
	probe := func(name string, success bool) {
		var username, password string
		for _, entry := range application.Snapshot().Entries() {
			if entry.ProxyName == name {
				username, password = entry.Username, entry.Password
			}
		}
		if username == "" {
			t.Fatalf("missing node %s", name)
		}
		d, err := proxy.SOCKS5("tcp", application.Status().SocksAddress, &proxy.Auth{User: username, Password: password}, &net.Dialer{Timeout: 4 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		client := &http.Client{Timeout: 7 * time.Second, Transport: &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return d.(proxy.ContextDialer).DialContext(ctx, network, address)
		}}}
		response, err := client.Get("http://destination.unrelated.test:" + strings.Split(targetAddr, ":")[1] + "/")
		if !success {
			if err == nil {
				response.Body.Close()
				t.Fatalf("%s unexpectedly connected", name)
			}
			return
		}
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		if string(data) != "node-dns-e2e-ok" {
			t.Fatalf("%s body=%s", name, data)
		}
	}
	for _, tc := range []struct{ name, label, domain string }{{"exact", "exact", nodes["exact"]}, {"deep", "narrow", nodes["deep"]}, {"wild", "wildcard", nodes["wild"]}, {"apex", "suffix", nodes["apex"]}, {"default", "default", nodes["default"]}, {"alias", "exact", nodes["exact"]}} {
		probe(tc.name, true)
		if count(tc.label, tc.domain) == 0 {
			t.Fatalf("%s did not use %s", tc.name, tc.label)
		}
		results = append(results, tc.name)
	}
	probe("hosts", true)
	if count("exact", nodes["hosts"]) != 0 || count("default", nodes["hosts"]) != 0 {
		t.Fatal("hosts lost priority")
	}
	results = append(results, "hosts priority")
	probe("failed", false)
	if count("system", nodes["failed"]) != 0 || count("default", nodes["failed"]) != 0 {
		t.Fatal("failed matched rule fell back")
	}
	results = append(results, "matched NXDOMAIN no fallback")
	if count("default", "destination.unrelated.test") != 0 {
		t.Fatal("destination DNS affected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	_, err = resolver.ProxyServerHostResolver.LookupIPv4(ctx, "download.unrelated.test")
	cancel()
	if err != nil || count("system", "download.unrelated.test") == 0 || count("default", "download.unrelated.test") != 0 {
		t.Fatal("unrelated dialer lookup affected")
	}
	results = append(results, "unrelated hostname guard")
	// A source node rejected by the dialer-proxy filter must not expand DNS scope.
	filteredProvider, err := application.AddProvider(gateway.Provider{Name: "filtered-scope", Type: "inline", Enabled: true, Payload: gateway.InlinePayload{
		{"name": "scope-accepted", "type": "socks5", "server": "one.example.test", "port": numericPort},
		{"name": "scope-filtered", "type": "socks5", "server": "filtered.example.test", "port": numericPort, "dialer-proxy": "unsupported-chain"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range application.Snapshot().Entries() {
		if entry.ProxyName == "scope-filtered" {
			t.Fatal("fixture node was not filtered")
		}
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	_, err = resolver.ProxyServerHostResolver.LookupIPv4(ctx, "filtered.example.test")
	cancel()
	if err != nil || count("system", "filtered.example.test") == 0 || count("wildcard", "filtered.example.test") != 0 || count("default", "filtered.example.test") != 0 {
		t.Fatal("filtered source node expanded DNS scope")
	}
	probe("scope-accepted", true)
	if err := application.DeleteProvider(filteredProvider.StableID); err != nil {
		t.Fatal(err)
	}
	results = append(results, "filtered node stays outside DNS scope")

	// Regression failure cases: parsing a live candidate must not write global
	// DNS settings while native lookups run; replacing old providers must not
	// close the new provider topology or interrupt an existing node's traffic.
	func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		done := make(chan error, 1)
		started := make(chan struct{})
		go func() {
			close(started)
			for ctx.Err() == nil {
				diagnostic, diagnosticErr := application.DiagnoseNodeDNS(ctx, nodeID("exact"), false)
				if ctx.Err() != nil {
					break
				}
				if diagnosticErr != nil || diagnostic.MatchedRule != "one.example.test" || len(diagnostic.IPv4.Addresses) == 0 {
					done <- fmt.Errorf("concurrent diagnostic: result=%+v error=%v", diagnostic, diagnosticErr)
					return
				}
				addresses, err := resolver.LookupIPWithResolver(ctx, "concurrent.unrelated.test", resolver.SystemResolver)
				if ctx.Err() != nil {
					break
				}
				if err != nil || len(addresses) == 0 {
					done <- fmt.Errorf("concurrent native DNS: addresses=%v error=%v", addresses, err)
					return
				}
			}
			done <- nil
		}()
		defer func() {
			cancel()
			if err := <-done; err != nil {
				t.Error(err)
			}
		}()
		<-started
		for i := 0; i < 4; i++ {
			p := addNode(fmt.Sprintf("concurrent-%d", i), "one.example.test", nil)
			probe(fmt.Sprintf("concurrent-%d", i), true)
			if err := application.DeleteProvider(p.StableID); err != nil {
				t.Fatal(err)
			}
			probe("exact", true)
		}
		if ctx.Err() != nil {
			t.Fatal("concurrent provider replacement exceeded deadline")
		}
	}()
	if count("system", "concurrent.unrelated.test") == 0 {
		t.Fatal("concurrent DNS fixture was not exercised")
	}
	results = append(results, "concurrent native DNS and eight live provider replacements")

	// Row order must not change precedence.
	reversed := []any{rule("failed.example.test", failNS), rule("one.example.test", exactNS), rule("*.deep.example.test", narrowNS), rule("*.example.test", wildcardNS), rule("+.example.test", suffixNS)}
	policy["rules"] = reversed
	putDNS(policy, true)
	probe("deep", true)
	probe("apex", true)
	results = append(results, "order independent")
	putDNS(map[string]any{"nameservers": []string{defaultNS}, "rules": []any{rule("*.example.test", wildcardNS)}}, true)
	beforeApex := count("default", nodes["apex"])
	probe("apex", true)
	if count("default", nodes["apex"]) <= beforeApex {
		t.Fatal("wildcard included apex")
	}
	results = append(results, "star excludes apex")
	putDNS(map[string]any{"rules": []any{rule("+.example.test", suffixNS)}}, true)
	probe("wild", true)
	if count("suffix", nodes["wild"]) == 0 {
		t.Fatal("plus omitted descendants")
	}
	results = append(results, "plus includes descendants")
	putDNS(policy, true)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "proxies:\n  - name: Remote\n    type: socks5\n    server: one.example.test\n    port: %d\ndns:\n  nameserver: [127.0.0.1:1]\n", numericPort)
	}))
	defer remote.Close()
	_, remotePort, _ := net.SplitHostPort(strings.TrimPrefix(remote.URL, "http://"))
	remoteProvider, remoteErr := application.AddProvider(gateway.Provider{Name: "Remote fixture", Type: "http", URL: "http://download.unrelated.test:" + remotePort + "/subscription", Enabled: true, RefreshSeconds: 3600})
	if remoteErr != nil {
		t.Fatal(remoteErr)
	}
	if count("system", "download.unrelated.test") == 0 || count("default", "download.unrelated.test") != 0 {
		t.Fatal("subscription download hijacked")
	}
	probe("Remote", true)
	if err := application.DeleteProvider(remoteProvider.StableID); err != nil {
		t.Fatal(err)
	}
	results = append(results, "download DNS scoped and remote DNS ignored")
	for _, format := range []string{"yaml", "uri", "base64"} {
		payload := fmt.Sprintf("socks5://one.example.test:%d#File-%s", numericPort, format)
		if format == "yaml" {
			payload = fmt.Sprintf("proxies:\n  - name: File-%s\n    type: socks5\n    server: one.example.test\n    port: %d\n", format, numericPort)
		}
		if format == "base64" {
			payload = base64.StdEncoding.EncodeToString([]byte(payload))
		}
		path, err := application.StoreProviderUpload(strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		provider, err := application.AddProvider(gateway.Provider{Name: "File-" + format, Type: "file", FilePath: path, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		probe("File-"+format, true)
		if err := application.DeleteProvider(provider.StableID); err != nil {
			t.Fatal(err)
		}
		results = append(results, "file "+format+" node inventory")
	}

	for _, invalid := range []map[string]any{
		{"rules": []any{rule("ONE.example.test.", exactNS), rule("one.example.test", suffixNS)}},
		{"rules": []any{rule("a.*.test", exactNS)}}, {"rules": []any{map[string]any{"domain": "*.test", "nameservers": []string{}}}},
		{"nameservers": []string{"https://user:pass@127.0.0.1/dns-query"}},
		{"nameservers": []string{"udp://127.0.0.1:0"}}, {"nameservers": []string{"https://127.0.0.1/dns-query#skip-cert-verify"}},
		{"bootstrap_nameservers": []string{"tls://recursive.test"}},
	} {
		putDNS(invalid, false)
	}
	probe("deep", true)
	results = append(results, "invalid configuration rejected without runtime change")
	// Old clients omit the new field: preserve the accepted policy.
	oldBody := settings()
	delete(oldBody, "node_dns")
	code, data := request("PUT", "/api/settings", oldBody)
	if code != 200 {
		t.Fatalf("old client %d %s", code, data)
	}
	probe("deep", true)
	results = append(results, "legacy API omission")
	if err := application.RefreshProvider(providers["deep"].StableID); err != nil {
		t.Fatal(err)
	}
	probe("deep", true)
	results = append(results, "refresh retains policy")
	// Force durable publication to fail after the candidate has been prepared.
	configPath := filepath.Join(application.DataDir(), "gateway.json")
	savedPath := configPath + ".e2e-backup"
	if err := os.Rename(configPath, savedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(configPath, 0700); err != nil {
		t.Fatal(err)
	}
	putDNS(map[string]any{"nameservers": []string{failNS}}, false)
	_ = os.Remove(configPath)
	if err := os.Rename(savedPath, configPath); err != nil {
		t.Fatal(err)
	}
	probe("deep", true)
	results = append(results, "persistence rollback")
	// A dead transport must also fail closed, including when system could answer.
	putDNS(map[string]any{"rules": []any{rule(nodes["failed"], "tcp://127.0.0.1:1")}}, true)
	probe("failed", false)
	if count("system", nodes["failed"]) != 0 {
		t.Fatal("transport failure leaked to system")
	}
	results = append(results, "transport failure no fallback")
	putDNS(map[string]any{}, true)
	probe("system", true)
	if count("system", nodes["system"]) == 0 {
		t.Fatal("unconfigured default did not use system")
	}
	results = append(results, "empty default system compatibility")
	putDNS(map[string]any{"nameservers": []string{tcpNS}}, true)
	probe("default", true)
	results = append(results, "TCP DNS transport")

	// Private CA only inside this test process; TLS verification remains enabled.
	certPEM, keyPEM := nodeDNSCertificate(t)
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if err := ca.AddCertificate(string(certPEM)); err != nil {
		t.Fatal(err)
	}
	defer ca.ResetCertificate()
	tlsHandler := func(req *D.Msg, label string) *D.Msg {
		q := req.Question[0]
		record(label, q.Name, q.Qtype)
		reply := new(D.Msg)
		reply.SetReply(req)
		if strings.HasPrefix(q.Name, "failed.") {
			reply.Rcode = D.RcodeNameError
		} else if q.Qtype == D.TypeA {
			reply.Answer = []D.RR{&D.A{Hdr: D.RR_Header{Name: q.Name, Rrtype: D.TypeA, Class: D.ClassINET, Ttl: 60}, A: net.ParseIP("127.0.0.1")}}
		}
		return reply
	}
	dotListener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	dot := &D.Server{Listener: dotListener, Handler: D.HandlerFunc(func(w D.ResponseWriter, r *D.Msg) { _ = w.WriteMsg(tlsHandler(r, "dot")) })}
	go dot.ActivateAndServe()
	defer dot.Shutdown()
	doh := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if r.Method == http.MethodGet {
			raw, _ = base64.RawURLEncoding.DecodeString(r.URL.Query().Get("dns"))
		}
		msg := new(D.Msg)
		if err := msg.Unpack(raw); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		answer, _ := tlsHandler(msg, "doh").Pack()
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(answer)
	}))
	doh.TLS = &tls.Config{Certificates: []tls.Certificate{pair}}
	doh.EnableHTTP2 = true
	doh.StartTLS()
	defer doh.Close()
	_, dotPort, _ := net.SplitHostPort(dotListener.Addr().String())
	_, dohPort, _ := net.SplitHostPort(strings.TrimPrefix(doh.URL, "https://"))
	for _, tc := range []struct{ label, ns string }{{"dot", "tls://resolver.fixture.test:" + dotPort}, {"doh", "https://resolver.fixture.test:" + dohPort + "/dns-query"}} {
		// Explicit bootstrap remains the only upstream-host resolver, even on failure.
		for _, bootstrap := range []string{bootstrapNS, bootstrapTCP, failNS, "tcp://127.0.0.1:1"} {
			beforeSystem := count("system", "resolver.fixture.test")
			beforeBootstrap := count("bootstrap", "resolver.fixture.test")
			putDNS(map[string]any{"nameservers": []string{tc.ns}, "bootstrap_nameservers": []string{bootstrap}}, true)
			success := bootstrap == bootstrapNS || bootstrap == bootstrapTCP
			probe("default", success)
			if count("system", "resolver.fixture.test") != beforeSystem || count("system", nodes["default"]) != 0 {
				t.Fatalf("%s explicit bootstrap leaked to system", tc.label)
			}
			if success && count("bootstrap", "resolver.fixture.test") <= beforeBootstrap {
				t.Fatalf("%s explicit bootstrap not used", tc.label)
			}
		}
		results = append(results, tc.label+" explicit UDP/TCP bootstrap and failure isolation")
		// The upstream hostname is itself a loaded node matching the encrypted rule.
		// Resolving it as an upstream must bypass that rule, avoiding recursion.
		overlap := addNode("upstream-overlap", "resolver.fixture.test", nil)
		beforeSystem := count("system", "resolver.fixture.test")
		beforeEncrypted := count(tc.label, nodes["default"])
		beforeBootstrap := count("bootstrap", "resolver.fixture.test")
		putDNS(map[string]any{"nameservers": []string{tc.ns}, "rules": []any{rule("+.fixture.test", tc.ns)}}, true)
		probe("default", true)
		probe("upstream-overlap", true)
		probe("failed", false)
		if count("system", "resolver.fixture.test") <= beforeSystem || count(tc.label, nodes["default"]) <= beforeEncrypted {
			t.Fatalf("%s empty bootstrap system resolution or encrypted node query missing", tc.label)
		}
		if count("bootstrap", "resolver.fixture.test") != beforeBootstrap || count("system", nodes["default"]) != 0 || count("system", nodes["failed"]) != 0 {
			t.Fatalf("%s empty bootstrap leaked node queries or used stale bootstrap", tc.label)
		}
		if err := application.DeleteProvider(overlap.StableID); err != nil {
			t.Fatal(err)
		}
		// Clear fixture system cache to prove a new resolver performs a fresh lookup.
		resolver.SystemResolver.(*MDNS.Resolver).ClearCache()
		results = append(results, tc.label+" empty bootstrap, overlap no recursion, node NXDOMAIN no fallback")
	}
	// Persist the user's rule shape using a private DoH endpoint and synthetic node.
	userPolicy := map[string]any{"nameservers": []string{}, "bootstrap_nameservers": []string{}, "rules": []any{rule("+.rfsconfig.com", "https://resolver.fixture.test:"+dohPort+"/dns-query")}}
	putDNS(userPolicy, true)
	gotDNS := settings()["node_dns"].(map[string]any)
	if (gotDNS["bootstrap_nameservers"] != nil && len(gotDNS["bootstrap_nameservers"].([]any)) != 0) || gotDNS["rules"].([]any)[0].(map[string]any)["domain"] != "+.rfsconfig.com" {
		t.Fatal("GET lost empty-bootstrap user rule")
	}
	addNode("user-rule", "node.rfsconfig.com", nil)
	probe("user-rule", true)
	if count("doh", "node.rfsconfig.com") == 0 || count("system", "node.rfsconfig.com") != 0 {
		t.Fatal("user rule bypassed private DoH")
	}
	results = append(results, "user +.rfsconfig.com empty bootstrap GET/PUT private fixture")
	// Restore encrypted default for the node TLS/SNI scenario.
	putDNS(map[string]any{"nameservers": []string{"https://resolver.fixture.test:" + dohPort + "/dns-query"}}, true)
	tlsNodeListener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer tlsNodeListener.Close()
	sniSeen := make(chan string, 1)
	go func() {
		conn, err := tlsNodeListener.Accept()
		if err != nil {
			return
		}
		tc := conn.(*tls.Conn)
		if err := tc.Handshake(); err != nil {
			conn.Close()
			return
		}
		sniSeen <- tc.ConnectionState().ServerName
		nodeDNSSOCKSFixture(conn, targetAddr)
	}()
	_, tlsNodePort, _ := net.SplitHostPort(tlsNodeListener.Addr().String())
	var tlsPort int
	_, _ = fmt.Sscan(tlsNodePort, &tlsPort)
	_, err = application.AddProvider(gateway.Provider{Name: "tls-node", Type: "inline", Enabled: true, Payload: gateway.InlinePayload{{"name": "tls-node", "type": "socks5", "server": "secure.example.test", "port": tlsPort, "tls": true}}})
	if err != nil {
		t.Fatal(err)
	}
	probe("tls-node", true)
	select {
	case sni := <-sniSeen:
		if sni != "secure.example.test" {
			t.Fatalf("TLS SNI changed: %s", sni)
		}
	case <-time.After(time.Second):
		t.Fatal("no node TLS handshake")
	}
	results = append(results, "node TLS SNI preserved")
	// Restart loads accepted policy; migration from schema 5 defaults to empty DNS.
	putDNS(userPolicy, true)
	dir := application.DataDir()
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	application, err = gateway.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	probe("user-rule", true)
	if len(application.Config().NodeDNS.BootstrapNameservers) != 0 {
		t.Fatal("restart changed bootstrap")
	}
	results = append(results, "restart persistence of empty-bootstrap user rule")
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	_ = json.Unmarshal(encoded, &legacy)
	legacy["schema_version"] = 5
	delete(legacy, "node_dns")
	encoded, _ = json.Marshal(legacy)
	if err := os.WriteFile(configPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := gateway.NewStore(dir).Load()
	if err != nil || loaded.SchemaVersion != gateway.SchemaVersion {
		t.Fatalf("migration: %v", err)
	}
	results = append(results, "schema 5 migration")
}

func nodeDNSSOCKSFixture(conn net.Conn, target string) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	head := make([]byte, 2)
	if _, err := io.ReadFull(conn, head); err != nil {
		return
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}
	_, _ = conn.Write([]byte{5, 0})
	req := make([]byte, 4)
	if _, err := io.ReadFull(conn, req); err != nil {
		return
	}
	size := 0
	switch req[3] {
	case 1:
		size = 4
	case 4:
		size = 16
	case 3:
		b := []byte{0}
		if _, err := io.ReadFull(conn, b); err != nil {
			return
		}
		size = int(b[0])
	default:
		return
	}
	address := make([]byte, size+2)
	if _, err := io.ReadFull(conn, address); err != nil {
		return
	}
	dest, err := net.DialTimeout("tcp", target, time.Second)
	if err != nil {
		return
	}
	defer dest.Close()
	_, _ = conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
	go io.Copy(dest, conn)
	_, _ = io.Copy(conn, dest)
}
func nodeDNSCertificate(t *testing.T) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(int64(binary.BigEndian.Uint32([]byte{1, 2, 3, 4}))), Subject: pkix.Name{CommonName: "Node DNS E2E"}, DNSNames: []string{"resolver.fixture.test", "secure.example.test"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	cert, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: raw})
}
