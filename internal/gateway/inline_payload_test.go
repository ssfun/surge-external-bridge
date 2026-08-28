package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInlinePayloadAcceptsMihomoProviderYAML(t *testing.T) {
	encoded, err := json.Marshal(map[string]any{
		"name":    "Inline",
		"type":    "inline",
		"enabled": true,
		"payload": "proxies:\n  - name: YAML Node\n    type: vless\n    server: edge.example.com\n    port: 443\n    uuid: 11111111-1111-4111-8111-111111111111\n    network: ws\n    ws-opts:\n      headers:\n        Host: example.com\nhosts:\n  edge.example.com: origin.example.com\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	var provider Provider
	if err := json.Unmarshal(encoded, &provider); err != nil {
		t.Fatal(err)
	}
	if len(provider.Payload) != 1 || provider.Payload[0]["name"] != "YAML Node" {
		t.Fatalf("Mihomo YAML was not normalized: %#v", provider.Payload)
	}
	wsOptions, ok := provider.Payload[0]["ws-opts"].(map[string]any)
	if !ok || wsOptions["headers"] == nil {
		t.Fatalf("nested Mihomo options were not retained: %#v", provider.Payload[0])
	}
	if provider.Hosts["edge.example.com"] != "origin.example.com" {
		t.Fatalf("Mihomo hosts were not retained: %#v", provider.Hosts)
	}
}

func TestInlinePayloadKeepsLegacyStoredArrayCompatible(t *testing.T) {
	var provider Provider
	if err := json.Unmarshal([]byte(`{"name":"Legacy","type":"inline","enabled":true,"payload":[{"name":"Legacy Node","type":"vless"}]}`), &provider); err != nil {
		t.Fatal(err)
	}
	if len(provider.Payload) != 1 || provider.Payload[0]["name"] != "Legacy Node" {
		t.Fatalf("legacy payload was not retained: %#v", provider.Payload)
	}
	encoded, err := json.Marshal(provider)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"payload":"`) {
		t.Fatalf("durable payload changed from normalized array to source text: %s", encoded)
	}
}

func TestInlinePayloadRejectsInvalidMihomoProviderYAML(t *testing.T) {
	for name, source := range map[string]string{
		"missing proxies": "proxy-groups: []\n",
		"empty proxies":   "proxies: []\n",
		"invalid yaml":    "proxies: [\n",
		"multiple docs":   "proxies:\n  - name: One\n---\nproxies:\n  - name: Two\n",
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(map[string]any{"payload": source})
			if err != nil {
				t.Fatal(err)
			}
			var provider Provider
			if err := json.Unmarshal(encoded, &provider); err == nil {
				t.Fatalf("invalid Mihomo Provider YAML was accepted: %q", source)
			}
		})
	}
}
