package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// InlinePayload keeps the durable gateway configuration compatible with the
// normalized array Mihomo needs, while also accepting a Mihomo Provider YAML
// document from management API clients.
type InlinePayload []map[string]any

func (p *InlinePayload) UnmarshalJSON(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		*p = nil
		return nil
	}
	if bytes.TrimSpace(data)[0] != '"' {
		var legacy []map[string]any
		if err := json.Unmarshal(data, &legacy); err != nil {
			return fmt.Errorf("inline payload must be Mihomo Provider YAML: %w", err)
		}
		*p = legacy
		return nil
	}
	var source string
	if err := json.Unmarshal(data, &source); err != nil {
		return fmt.Errorf("decode inline Mihomo Provider YAML: %w", err)
	}
	payload, err := parseInlineProviderYAML(source)
	if err != nil {
		return err
	}
	*p = payload
	return nil
}

func parseInlineProviderYAML(source string) (InlinePayload, error) {
	if strings.TrimSpace(source) == "" {
		return nil, errors.New("inline Mihomo Provider YAML is empty")
	}
	var document struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	decoder := yaml.NewDecoder(strings.NewReader(source))
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("invalid inline Mihomo Provider YAML: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("inline Mihomo Provider YAML must contain exactly one document")
		}
		return nil, fmt.Errorf("invalid inline Mihomo Provider YAML: %w", err)
	}
	if len(document.Proxies) == 0 {
		return nil, errors.New("inline Mihomo Provider YAML must contain a non-empty proxies list")
	}
	return InlinePayload(document.Proxies), nil
}
