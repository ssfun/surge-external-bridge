package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"

	"github.com/sagernet/sing-box/include"
	boxoption "github.com/sagernet/sing-box/option"
	"github.com/ssfun/vless2surge/internal/domain"
)

var CoreVersion = linkedCoreVersion()

type Compiler struct{}

func linkedCoreVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, dependency := range info.Deps {
		if dependency.Path != "github.com/sagernet/sing-box" {
			continue
		}
		version := dependency.Version
		if dependency.Replace != nil && dependency.Replace.Version != "" {
			version = dependency.Replace.Version
		}
		return strings.TrimPrefix(version, "v")
	}
	return "unknown"
}

func NewCompiler() *Compiler { return &Compiler{} }

func (c *Compiler) Compile(config domain.Config, revision *domain.Revision) ([]byte, error) {
	if revision == nil || len(revision.Nodes) == 0 {
		return nil, errors.New("cannot compile an empty revision")
	}
	socksBind, socksPort, socksAdvertise := revisionEndpoint(config, revision)
	users := make([]map[string]any, 0, len(revision.Nodes))
	outbounds := make([]map[string]any, 0, len(revision.Nodes))
	rules := make([]map[string]any, 0, len(revision.Nodes)+1)
	seenUsers := make(map[string]bool, len(revision.Nodes))
	seenTags := make(map[string]bool, len(revision.Nodes))
	for _, node := range revision.Nodes {
		if !strings.EqualFold(node.Type, "vless") {
			return nil, fmt.Errorf("compile node %q: protocol %q is not supported by the Embedded VLESS gateway", node.DisplayName, node.Type)
		}
		if node.AuthUser == "" || node.Password == "" || node.OutboundTag == "" {
			return nil, fmt.Errorf("compile node %q: SOCKS identity and outbound tag are required", node.DisplayName)
		}
		if seenUsers[node.AuthUser] || seenTags[node.OutboundTag] {
			return nil, fmt.Errorf("compile node %q: duplicate SOCKS identity or outbound tag", node.DisplayName)
		}
		seenUsers[node.AuthUser] = true
		seenTags[node.OutboundTag] = true
		users = append(users, map[string]any{"username": node.AuthUser, "password": node.Password})
		outbound, err := compileVLESS(node)
		if err != nil {
			return nil, fmt.Errorf("compile node %q: %w", node.DisplayName, err)
		}
		outbounds = append(outbounds, outbound)
		rules = append(rules, map[string]any{
			"inbound":   []string{"surge-in"},
			"auth_user": []string{node.AuthUser},
			"action":    "route",
			"outbound":  node.OutboundTag,
		})
	}
	rules = append(rules, map[string]any{
		"inbound": []string{"surge-in"},
		"action":  "reject",
		"method":  "default",
	})
	compiled := map[string]any{
		"log": map[string]any{
			"disabled":  true,
			"level":     "warn",
			"timestamp": true,
		},
		"inbounds": []map[string]any{{
			"type":        "socks",
			"tag":         "surge-in",
			"listen":      socksBind,
			"listen_port": socksPort,
			"users":       users,
		}},
		"outbounds": outbounds,
		"route": map[string]any{
			"rules":                 rules,
			"auto_detect_interface": true,
		},
	}
	data, err := json.MarshalIndent(compiled, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if err := ValidateOptions(data); err != nil {
		return nil, err
	}
	effective := append([]byte(nil), data...)
	effective = append(effective, []byte(socksAdvertise)...)
	for _, node := range revision.Nodes {
		effective = append(effective, []byte(node.NodeID)...)
		effective = append(effective, []byte(node.DisplayName)...)
	}
	hash := sha256.Sum256(effective)
	revision.ConfigHash = hex.EncodeToString(hash[:])
	revision.ID = hex.EncodeToString(hash[:8])
	return data, nil
}

func revisionEndpoint(config domain.Config, revision *domain.Revision) (string, uint16, string) {
	bind, port, advertise := revision.SocksBind, revision.SocksPort, revision.SocksAdvertise
	// Schema 1 revisions created before endpoint snapshots used the live config.
	if bind == "" {
		bind = config.SocksBind
	}
	if port == 0 {
		port = config.SocksPort
	}
	if advertise == "" {
		advertise = config.SocksAdvertise
	}
	return bind, port, advertise
}

func compileVLESS(node domain.RuntimeNode) (map[string]any, error) {
	if node.Server == "" || node.Port == 0 || node.UUID == "" {
		return nil, errors.New("missing server, port, or UUID")
	}
	if node.Flow == "xtls-rprx-vision" {
		if node.Network != "" && !strings.EqualFold(node.Network, "tcp") {
			return nil, errors.New("xtls-rprx-vision requires the TCP transport")
		}
		if !strings.EqualFold(node.Security, "tls") && !strings.EqualFold(node.Security, "reality") {
			return nil, errors.New("xtls-rprx-vision requires TLS or Reality")
		}
	}
	outbound := map[string]any{
		"type":        "vless",
		"tag":         node.OutboundTag,
		"server":      node.Server,
		"server_port": node.Port,
		"uuid":        node.UUID,
	}
	if node.Flow != "" {
		outbound["flow"] = node.Flow
	}
	if node.PacketEncoding != "" {
		outbound["packet_encoding"] = node.PacketEncoding
	}
	security := strings.ToLower(node.Security)
	if security == "tls" || security == "reality" {
		tls := map[string]any{
			"enabled":     true,
			"server_name": firstNonEmpty(node.ServerName, node.Server),
			"insecure":    node.Insecure,
		}
		fingerprint := node.Fingerprint
		if security == "reality" && fingerprint == "" {
			fingerprint = "chrome"
		}
		if fingerprint != "" {
			tls["utls"] = map[string]any{"enabled": true, "fingerprint": fingerprint}
		}
		if len(node.ALPN) > 0 {
			tls["alpn"] = node.ALPN
		}
		if security == "reality" {
			if node.RealityPublicKey == "" {
				return nil, errors.New("Reality public key is missing")
			}
			tls["reality"] = map[string]any{
				"enabled":    true,
				"public_key": node.RealityPublicKey,
				"short_id":   node.RealityShortID,
			}
		}
		outbound["tls"] = tls
	}

	switch strings.ToLower(node.Network) {
	case "", "tcp":
	case "ws":
		transport := map[string]any{"type": "ws", "path": node.Path}
		if node.Host != "" {
			transport["headers"] = map[string][]string{"Host": {node.Host}}
		}
		outbound["transport"] = transport
	case "grpc":
		outbound["transport"] = map[string]any{"type": "grpc", "service_name": node.ServiceName}
	case "http":
		transport := map[string]any{"type": "http", "path": node.Path}
		if node.Host != "" {
			transport["host"] = []string{node.Host}
		}
		outbound["transport"] = transport
	case "httpupgrade":
		outbound["transport"] = map[string]any{"type": "httpupgrade", "host": node.Host, "path": node.Path}
	default:
		return nil, fmt.Errorf("unsupported transport %q", node.Network)
	}
	return outbound, nil
}

func ValidateOptions(data []byte) error {
	ctx := include.Context(context.Background())
	var options boxoption.Options
	if err := options.UnmarshalJSONContext(ctx, data); err != nil {
		return fmt.Errorf("sing-box %s config validation: %w", CoreVersion, err)
	}
	return nil
}

func Redacted(data []byte) []byte {
	var value any
	if json.Unmarshal(data, &value) != nil {
		return []byte("{}\n")
	}
	redact(value)
	result, _ := json.MarshalIndent(value, "", "  ")
	return append(result, '\n')
}

func redact(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			switch strings.ToLower(key) {
			case "uuid", "password", "public_key", "private_key", "short_id":
				typed[key] = "***"
			default:
				redact(child)
			}
		}
	case []any:
		for _, child := range typed {
			redact(child)
		}
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func Compact(data []byte) string {
	var buffer bytes.Buffer
	if json.Compact(&buffer, data) != nil {
		return string(data)
	}
	return buffer.String()
}
