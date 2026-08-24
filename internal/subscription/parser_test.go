package subscription

import (
	"encoding/base64"
	"strings"
	"testing"
)

const realityPublicKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
const realityLink = "vless://11111111-1111-4111-8111-111111111111@example.com:443?type=ws&security=reality&sni=www.example.com&fp=chrome&alpn=h2%2Chttp%2F1.1&pbk=" + realityPublicKey + "&sid=abcd&path=%2Fws&host=cdn.example.com#Hong%20Kong"

func TestParseVLESSReality(t *testing.T) {
	result, err := Parse([]byte(realityLink))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 1 || result.RawCount != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	node := result.Nodes[0]
	if node.Name != "Hong Kong" || node.Network != "ws" || node.Security != "reality" || node.RealityPublicKey != realityPublicKey || node.Path != "/ws" || node.Host != "cdn.example.com" || len(node.ALPN) != 2 || node.ALPN[0] != "h2" || node.ALPN[1] != "http/1.1" {
		t.Fatalf("unexpected node: %+v", node)
	}
}

func TestRealityDefaultsToRequiredChromeUTLSFingerprint(t *testing.T) {
	link := "vless://99999999-9999-4999-8999-999999999999@example.com:443?security=reality&pbk=" + realityPublicKey + "#Reality"
	result, err := Parse([]byte(link))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 1 || result.Nodes[0].Fingerprint != "chrome" {
		t.Fatalf("Reality node did not receive the required uTLS default: %+v", result)
	}
}

func TestParseBase64Subscription(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte(realityLink + "\n"))
	result, err := Parse([]byte(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 1 {
		t.Fatalf("expected one node, got %d", len(result.Nodes))
	}
}

func TestParseClashAndProviders(t *testing.T) {
	content := []byte(`proxy-providers:
  z-invalid:
    type: http
    url: ftp://invalid.example/sub
  beup:
    type: http
    url: https://example.com/sub/token
    filter: 香港|HK
    interval: 7200
    header:
      User-Agent: [Clash.Meta]
      Authorization: Bearer secret
proxies:
  - name: HK Reality
    type: vless
    server: hk.example.com
    port: 443
    uuid: 11111111-1111-4111-8111-111111111111
    network: tcp
    tls: true
    flow: xtls-rprx-vision
    client-fingerprint: chrome
    alpn: [h2, http/1.1]
    reality-opts:
      public-key: AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA
      short-id: abcd
  - name: Native SS
    type: ss
    server: ss.example.com
    port: 443
`)
	result, err := Parse(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 1 || len(result.Dropped) != 1 {
		t.Fatalf("unexpected parse counts: nodes=%d dropped=%d", len(result.Nodes), len(result.Dropped))
	}
	if result.Dropped[0].Reason != "Surge 原生支持" {
		t.Fatalf("unexpected drop reason: %s", result.Dropped[0].Reason)
	}
	if providers, err := ParseClashProviders(content); err == nil || providers != nil || !strings.Contains(err.Error(), "z-invalid") {
		t.Fatalf("mixed valid/invalid providers were partially accepted: providers=%+v err=%v", providers, err)
	}
	providers, err := ParseClashProviders([]byte(`proxy-providers:
  beup:
    type: http
    url: https://example.com/sub/token
    filter: 香港|HK
    interval: 7200
    header:
      User-Agent: [Clash.Meta]
      Authorization: Bearer secret
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 1 || providers[0].Name != "beup" || providers[0].SourceType != "url" || providers[0].Filter != "香港|HK" || providers[0].RefreshSeconds != 7200 {
		t.Fatalf("unexpected providers: %+v", providers)
	}
	if providers[0].Headers["User-Agent"] != "Clash.Meta" || providers[0].Headers["Authorization"] != "Bearer secret" {
		t.Fatalf("provider headers were not preserved: %+v", providers[0].Headers)
	}
}

func TestParseClashHTTPTransportListsAndHostHeader(t *testing.T) {
	result, err := Parse([]byte(`proxies:
  - name: HTTP transport
    type: vless
    server: edge.example.com
    port: 443
    uuid: 11111111-1111-4111-8111-111111111111
    network: h2
    tls: true
    http-opts:
      path: [/first, /second]
      headers:
        HOST: [cdn.example.com]
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 1 || result.Nodes[0].Network != "http" || result.Nodes[0].Path != "/first" || result.Nodes[0].Host != "cdn.example.com" {
		t.Fatalf("Clash HTTP transport options were not normalized: %+v", result)
	}
}

func TestRejectUnsupportedSecurity(t *testing.T) {
	link := "vless://11111111-1111-4111-8111-111111111111@example.com:443?security=unknown#bad"
	result, err := Parse([]byte(link))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 0 || len(result.Dropped) != 1 || !strings.Contains(result.Dropped[0].Reason, "unsupported VLESS security") {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestRejectUnsupportedVLESSSemanticsPerNode(t *testing.T) {
	valid := "vless://22222222-2222-4222-8222-222222222222@example.com:443?encryption=none&security=tls&sni=example.com&flow=xtls-rprx-vision&packetEncoding=xudp#valid"
	invalid := []string{
		"vless://11111111-1111-4111-8111-111111111111@example.com:443?encryption=aes-128-gcm#encryption",
		"vless://33333333-3333-4333-8333-333333333333@example.com:443?flow=unknown#flow",
		"vless://44444444-4444-4444-8444-444444444444@example.com:443?packetEncoding=unknown#packet",
		"vless://55555555-5555-4555-8555-555555555555@example.com:443?type=ws&security=tls&flow=xtls-rprx-vision#vision-transport",
		"vless://66666666-6666-4666-8666-666666666666@example.com:443?type=tcp&flow=xtls-rprx-vision#vision-security",
	}
	result, err := Parse([]byte(valid + "\n" + strings.Join(invalid, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	if result.RawCount != 6 || len(result.Nodes) != 1 || len(result.Dropped) != 5 {
		t.Fatalf("invalid nodes affected valid peers: %+v", result)
	}
	for _, dropped := range result.Dropped {
		if dropped.Reason == "" {
			t.Fatalf("invalid node has no explicit reason: %+v", dropped)
		}
	}
}

func TestRejectInvalidRealityAndUTLSSemanticsPerNode(t *testing.T) {
	valid := "vless://55555555-5555-4555-8555-555555555555@example.com:443?security=reality&fp=chrome&pbk=" + realityPublicKey + "&sid=abcd#valid"
	invalid := []string{
		"vless://66666666-6666-4666-8666-666666666666@example.com:443?security=tls&fp=not-a-browser#fingerprint",
		"vless://77777777-7777-4777-8777-777777777777@example.com:443?security=reality&fp=chrome&pbk=too-short#public-key",
		"vless://88888888-8888-4888-8888-888888888888@example.com:443?security=reality&fp=chrome&pbk=" + realityPublicKey + "&sid=xyz#short-id",
	}
	result, err := Parse([]byte(valid + "\n" + strings.Join(invalid, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	if result.RawCount != 4 || len(result.Nodes) != 1 || len(result.Dropped) != 3 {
		t.Fatalf("invalid crypto semantics affected valid peers: %+v", result)
	}
	for _, dropped := range result.Dropped {
		if dropped.Reason == "" {
			t.Fatalf("invalid node has no explicit reason: %+v", dropped)
		}
	}
}

func TestMixedShareLinksAreNeverSilentlyDiscarded(t *testing.T) {
	content := strings.Join([]string{
		realityLink,
		"ss://cipher@example.com:443#Native%20SS",
		"vmess://opaque-payload",
		"foo://opaque#Unknown",
	}, "\n")
	result, err := Parse([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if result.RawCount != 4 || len(result.Nodes) != 1 || len(result.Dropped) != 3 {
		t.Fatalf("unexpected mixed parse result: %+v", result)
	}
	if result.Dropped[0].Reason != "Surge 原生支持" || result.Dropped[1].Reason != "Surge 原生支持" || result.Dropped[2].Reason != "产品暂不支持的协议" {
		t.Fatalf("drop reasons are not explicit: %+v", result.Dropped)
	}
}

func TestRejectUnsupportedTransport(t *testing.T) {
	link := "vless://11111111-1111-4111-8111-111111111111@example.com:443?type=xhttp#bad"
	result, err := Parse([]byte(link))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 0 || len(result.Dropped) != 1 || !strings.Contains(result.Dropped[0].Reason, "unsupported") {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestProvidersAreDeterministicallySorted(t *testing.T) {
	providers, err := ParseClashProviders([]byte(`proxy-providers:
  zeta: {type: http, url: https://z.example/sub}
  alpha: {type: http, url: https://a.example/sub}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 2 || providers[0].Name != "alpha" || providers[1].Name != "zeta" {
		t.Fatalf("providers are not sorted: %+v", providers)
	}
}

func TestProvidersRejectPartialInvalidInputWithNames(t *testing.T) {
	providers, err := ParseClashProviders([]byte(`proxy-providers:
  valid: {type: http, url: https://valid.example/sub}
  missing-url: {type: http}
  bad-scheme: {type: http, url: file:///tmp/sub}
`))
	if err == nil || providers != nil || !strings.Contains(err.Error(), "missing-url") || !strings.Contains(err.Error(), "bad-scheme") {
		t.Fatalf("invalid providers were silently skipped: providers=%+v err=%v", providers, err)
	}
}
