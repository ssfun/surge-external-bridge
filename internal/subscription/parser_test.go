package subscription

import (
	"encoding/base64"
	"strings"
	"testing"
)

const realityLink = "vless://11111111-1111-4111-8111-111111111111@example.com:443?type=ws&security=reality&sni=www.example.com&fp=chrome&alpn=h2%2Chttp%2F1.1&pbk=public-key&sid=abcd&path=%2Fws&host=cdn.example.com#Hong%20Kong"

func TestParseVLESSReality(t *testing.T) {
	result, err := Parse([]byte(realityLink))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 1 || result.RawCount != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	node := result.Nodes[0]
	if node.Name != "Hong Kong" || node.Network != "ws" || node.Security != "reality" || node.RealityPublicKey != "public-key" || node.Path != "/ws" || node.Host != "cdn.example.com" || len(node.ALPN) != 2 || node.ALPN[0] != "h2" || node.ALPN[1] != "http/1.1" {
		t.Fatalf("unexpected node: %+v", node)
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
      public-key: public-key
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
	providers, err := ParseClashProviders(content)
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
	valid := "vless://22222222-2222-4222-8222-222222222222@example.com:443?encryption=none&flow=xtls-rprx-vision&packetEncoding=xudp#valid"
	invalid := []string{
		"vless://11111111-1111-4111-8111-111111111111@example.com:443?encryption=aes-128-gcm#encryption",
		"vless://33333333-3333-4333-8333-333333333333@example.com:443?flow=unknown#flow",
		"vless://44444444-4444-4444-8444-444444444444@example.com:443?packetEncoding=unknown#packet",
	}
	result, err := Parse([]byte(valid + "\n" + strings.Join(invalid, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	if result.RawCount != 4 || len(result.Nodes) != 1 || len(result.Dropped) != 3 {
		t.Fatalf("invalid nodes affected valid peers: %+v", result)
	}
	for _, dropped := range result.Dropped {
		if !strings.Contains(dropped.Reason, "unsupported VLESS") {
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
