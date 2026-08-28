package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// UnmarshalJSON accepts raw Mihomo Provider YAML from management clients while
// keeping the durable representation normalized as a proxy array plus the
// source's top-level hosts mappings.
func (p *Provider) UnmarshalJSON(data []byte) error {
	type providerAlias Provider
	var wire struct {
		providerAlias
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*p = Provider(wire.providerAlias)
	trimmed := bytes.TrimSpace(wire.Payload)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		p.Payload = nil
		return nil
	}
	if trimmed[0] != '"' {
		var legacy InlinePayload
		if err := json.Unmarshal(trimmed, &legacy); err != nil {
			return fmt.Errorf("inline payload must be Mihomo Provider YAML: %w", err)
		}
		p.Payload = legacy
		return nil
	}
	var source string
	if err := json.Unmarshal(trimmed, &source); err != nil {
		return fmt.Errorf("decode inline Mihomo Provider YAML: %w", err)
	}
	payload, hosts, err := parseInlineProviderYAML(source)
	if err != nil {
		return err
	}
	p.Payload = payload
	p.Hosts = hosts
	return nil
}
