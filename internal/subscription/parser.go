package subscription

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ssfun/vless2surge/internal/domain"
	"gopkg.in/yaml.v3"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var shareLinkPattern = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.-]*)://`)

type ParseResult struct {
	Nodes    []domain.Node
	Dropped  []domain.DroppedNode
	RawCount int
}

func Parse(content []byte) (ParseResult, error) {
	return parse(content, 0)
}

func parse(content []byte, depth int) (ParseResult, error) {
	if depth > 2 {
		return ParseResult{}, errors.New("subscription encoding is nested too deeply")
	}
	text := strings.TrimSpace(string(content))
	if text == "" {
		return ParseResult{}, errors.New("subscription is empty")
	}

	if strings.Contains(text, "proxies:") || strings.HasPrefix(text, "proxy-providers:") {
		if result, err := parseClash([]byte(text)); err == nil && result.RawCount > 0 {
			return result, nil
		}
	}

	result := ParseResult{}
	for _, line := range strings.Fields(text) {
		line = strings.TrimSpace(line)
		protocol, isShareLink := shareProtocol(line)
		if !isShareLink {
			continue
		}
		result.RawCount++
		if protocol != "vless" {
			result.Dropped = append(result.Dropped, domain.DroppedNode{
				Name: linkName(line), Type: protocol, Reason: unsupportedProtocolReason(protocol),
			})
			continue
		}
		node, err := parseVLESSURL(line)
		if err != nil {
			result.Dropped = append(result.Dropped, domain.DroppedNode{Name: linkName(line), Type: "vless", Reason: err.Error()})
			continue
		}
		result.Nodes = append(result.Nodes, node)
	}
	if result.RawCount > 0 {
		return result, nil
	}

	if decoded, ok := decodeBase64(text); ok {
		return parse(decoded, depth+1)
	}
	return ParseResult{}, errors.New("unsupported subscription format: expected VLESS links, Base64, or Clash YAML")
}

func parseVLESSURL(raw string) (domain.Node, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return domain.Node{}, fmt.Errorf("invalid VLESS URL: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "vless") {
		return domain.Node{}, errors.New("not a VLESS URL")
	}
	uuid := ""
	if u.User != nil {
		uuid = u.User.Username()
	}
	if !uuidPattern.MatchString(uuid) {
		return domain.Node{}, errors.New("invalid or missing VLESS UUID")
	}
	host := u.Hostname()
	if host == "" {
		return domain.Node{}, errors.New("missing VLESS server")
	}
	portNumber, err := strconv.Atoi(u.Port())
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return domain.Node{}, errors.New("invalid VLESS port")
	}
	q := u.Query()
	if encryption := strings.ToLower(strings.TrimSpace(q.Get("encryption"))); encryption != "" && encryption != "none" {
		return domain.Node{}, fmt.Errorf("unsupported VLESS encryption %q", encryption)
	}
	network := first(q, "type", "network")
	if network == "" {
		network = "tcp"
	}
	network = normalizeNetwork(network)
	if !supportedNetwork(network) {
		return domain.Node{}, fmt.Errorf("unsupported VLESS transport %q", network)
	}
	security := strings.ToLower(q.Get("security"))
	if security == "" && (q.Get("pbk") != "" || q.Get("publicKey") != "") {
		security = "reality"
	}
	if !supportedSecurity(security) {
		return domain.Node{}, fmt.Errorf("unsupported VLESS security %q", security)
	}
	name, _ := url.PathUnescape(u.Fragment)
	if strings.TrimSpace(name) == "" {
		name = net.JoinHostPort(host, strconv.Itoa(portNumber))
	}
	flow := strings.ToLower(strings.TrimSpace(q.Get("flow")))
	if !supportedFlow(flow) {
		return domain.Node{}, fmt.Errorf("unsupported VLESS flow %q", flow)
	}
	packetEncoding := strings.ToLower(strings.TrimSpace(first(q, "packetEncoding", "packet_encoding")))
	if !supportedPacketEncoding(packetEncoding) {
		return domain.Node{}, fmt.Errorf("unsupported VLESS packet encoding %q", packetEncoding)
	}
	node := domain.Node{
		Type:             "vless",
		Name:             name,
		Server:           host,
		Port:             uint16(portNumber),
		UUID:             uuid,
		Flow:             flow,
		Network:          network,
		Security:         security,
		ServerName:       first(q, "sni", "serverName"),
		Fingerprint:      first(q, "fp", "fingerprint"),
		ALPN:             splitCommaList(q.Get("alpn")),
		RealityPublicKey: first(q, "pbk", "publicKey"),
		RealityShortID:   first(q, "sid", "shortId"),
		Path:             q.Get("path"),
		Host:             q.Get("host"),
		ServiceName:      first(q, "serviceName", "service_name"),
		Insecure:         parseBool(first(q, "allowInsecure", "insecure", "skip-cert-verify")),
		PacketEncoding:   packetEncoding,
	}
	if node.Security == "reality" && node.RealityPublicKey == "" {
		return domain.Node{}, errors.New("Reality node is missing public key")
	}
	return node, nil
}

func parseClash(content []byte) (ParseResult, error) {
	var root struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(content, &root); err != nil {
		return ParseResult{}, fmt.Errorf("parse Clash YAML: %w", err)
	}
	result := ParseResult{RawCount: len(root.Proxies)}
	for _, proxy := range root.Proxies {
		typeName := strings.ToLower(asString(proxy["type"]))
		name := asString(proxy["name"])
		if typeName != "vless" {
			result.Dropped = append(result.Dropped, domain.DroppedNode{Name: name, Type: typeName, Reason: unsupportedProtocolReason(typeName)})
			continue
		}
		node, err := clashVLESS(proxy)
		if err != nil {
			result.Dropped = append(result.Dropped, domain.DroppedNode{Name: name, Type: typeName, Reason: err.Error()})
			continue
		}
		result.Nodes = append(result.Nodes, node)
	}
	return result, nil
}

func clashVLESS(proxy map[string]any) (domain.Node, error) {
	name := asString(proxy["name"])
	server := asString(proxy["server"])
	port := asInt(proxy["port"])
	uuid := asString(proxy["uuid"])
	if name == "" || server == "" || port < 1 || port > 65535 || !uuidPattern.MatchString(uuid) {
		return domain.Node{}, errors.New("missing or invalid VLESS name/server/port/uuid")
	}
	network := normalizeNetwork(asString(proxy["network"]))
	if network == "" {
		network = "tcp"
	}
	if !supportedNetwork(network) {
		return domain.Node{}, fmt.Errorf("unsupported VLESS transport %q", network)
	}
	reality := asMap(proxy["reality-opts"])
	if encryption := strings.ToLower(strings.TrimSpace(asString(proxy["encryption"]))); encryption != "" && encryption != "none" {
		return domain.Node{}, fmt.Errorf("unsupported VLESS encryption %q", encryption)
	}
	security := strings.ToLower(asString(proxy["security"]))
	if len(reality) > 0 {
		security = "reality"
	} else if asBool(proxy["tls"]) && security == "" {
		security = "tls"
	}
	if !supportedSecurity(security) {
		return domain.Node{}, fmt.Errorf("unsupported VLESS security %q", security)
	}
	ws := asMap(proxy["ws-opts"])
	grpc := asMap(proxy["grpc-opts"])
	httpOpts := asMap(proxy["http-opts"])
	httpUpgrade := asMap(proxy["http-upgrade-opts"])
	host := asString(proxy["host"])
	path := asString(proxy["path"])
	if network == "ws" {
		path = firstString(asString(ws["path"]), path)
		headers := asMap(ws["headers"])
		host = firstString(asString(headers["Host"]), asString(headers["host"]), host)
	}
	if network == "http" {
		path = firstString(asString(httpOpts["path"]), path)
	}
	if network == "httpupgrade" {
		path = firstString(asString(httpUpgrade["path"]), path)
		host = firstString(asString(httpUpgrade["host"]), host)
	}
	flow := strings.ToLower(strings.TrimSpace(asString(proxy["flow"])))
	if !supportedFlow(flow) {
		return domain.Node{}, fmt.Errorf("unsupported VLESS flow %q", flow)
	}
	packetEncoding := strings.ToLower(strings.TrimSpace(firstString(asString(proxy["packet-encoding"]), asString(proxy["packet_encoding"]))))
	if !supportedPacketEncoding(packetEncoding) {
		return domain.Node{}, fmt.Errorf("unsupported VLESS packet encoding %q", packetEncoding)
	}
	node := domain.Node{
		Type:             "vless",
		Name:             name,
		Server:           server,
		Port:             uint16(port),
		UUID:             uuid,
		Flow:             flow,
		Network:          network,
		Security:         security,
		ServerName:       firstString(asString(proxy["servername"]), asString(proxy["sni"])),
		Fingerprint:      firstString(asString(proxy["client-fingerprint"]), asString(proxy["fingerprint"]), "chrome"),
		ALPN:             asStringSlice(proxy["alpn"]),
		RealityPublicKey: firstString(asString(reality["public-key"]), asString(reality["public_key"])),
		RealityShortID:   firstString(asString(reality["short-id"]), asString(reality["short_id"])),
		Path:             path,
		Host:             host,
		ServiceName:      firstString(asString(grpc["grpc-service-name"]), asString(grpc["service-name"]), asString(proxy["serviceName"])),
		Insecure:         asBool(proxy["skip-cert-verify"]),
		PacketEncoding:   packetEncoding,
	}
	if node.Security == "reality" && node.RealityPublicKey == "" {
		return domain.Node{}, errors.New("Reality node is missing public key")
	}
	return node, nil
}

func ParseClashProviders(content []byte) ([]domain.Subscription, error) {
	var root struct {
		Providers map[string]map[string]any `yaml:"proxy-providers"`
	}
	if err := yaml.Unmarshal(content, &root); err != nil {
		return nil, fmt.Errorf("parse Clash providers: %w", err)
	}
	if len(root.Providers) == 0 {
		return nil, errors.New("no proxy-providers found")
	}
	result := make([]domain.Subscription, 0, len(root.Providers))
	for name, provider := range root.Providers {
		rawURL := asString(provider["url"])
		parsedURL, err := url.Parse(rawURL)
		if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
			continue
		}
		result = append(result, domain.Subscription{
			Name:           name,
			SourceType:     "url",
			URL:            rawURL,
			Filter:         asString(provider["filter"]),
			Enabled:        true,
			Headers:        firstHeaderMap(provider["headers"], provider["header"]),
			RefreshSeconds: asInt(provider["interval"]),
		})
	}
	if len(result) == 0 {
		return nil, errors.New("proxy-providers contain no HTTP URLs")
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func decodeBase64(value string) ([]byte, bool) {
	compact := strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, value)
	encodings := []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding}
	for _, encoding := range encodings {
		decoded, err := encoding.DecodeString(compact)
		if err == nil && len(decoded) > 0 {
			return decoded, true
		}
	}
	return nil, false
}

func first(values url.Values, keys ...string) string {
	for _, key := range keys {
		if value := values.Get(key); value != "" {
			return value
		}
	}
	return ""
}

func firstString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func parseBool(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "1" || value == "true" || value == "yes"
}

func normalizeNetwork(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "tcp", "raw":
		return "tcp"
	case "websocket", "ws":
		return "ws"
	case "grpc":
		return "grpc"
	case "http", "h2":
		return "http"
	case "httpupgrade", "http-upgrade":
		return "httpupgrade"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func supportedNetwork(value string) bool {
	switch value {
	case "tcp", "ws", "grpc", "http", "httpupgrade":
		return true
	default:
		return false
	}
}

func supportedSecurity(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "none", "tls", "reality":
		return true
	default:
		return false
	}
}

func supportedFlow(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "xtls-rprx-vision":
		return true
	default:
		return false
	}
}

func supportedPacketEncoding(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "xudp", "packetaddr":
		return true
	default:
		return false
	}
}

func splitCommaList(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func shareProtocol(raw string) (string, bool) {
	match := shareLinkPattern.FindStringSubmatch(strings.TrimSpace(raw))
	if len(match) != 2 {
		return "", false
	}
	protocol := strings.ToLower(match[1])
	switch protocol {
	case "vless", "ss", "shadowsocks", "trojan", "vmess", "hysteria2", "hy2", "tuic", "anytls", "socks", "socks5", "http", "https":
		return protocol, true
	default:
		return protocol, true
	}
}

func unsupportedProtocolReason(protocol string) string {
	switch strings.ToLower(protocol) {
	case "ss", "shadowsocks", "trojan", "vmess", "hysteria2", "hy2", "tuic", "anytls", "socks", "socks5", "http", "https":
		return "Surge 原生支持"
	default:
		return "产品暂不支持的协议"
	}
}

func linkName(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "invalid VLESS link"
	}
	name, _ := url.PathUnescape(u.Fragment)
	if strings.TrimSpace(name) == "" {
		name = strings.ToUpper(u.Scheme) + " 节点"
	}
	return name
}

func firstHeaderMap(values ...any) map[string]string {
	for _, value := range values {
		mapping := asMap(value)
		if len(mapping) == 0 {
			continue
		}
		result := make(map[string]string, len(mapping))
		for key, raw := range mapping {
			switch typed := raw.(type) {
			case string:
				result[key] = typed
			case []string:
				if len(typed) > 0 {
					result[key] = typed[0]
				}
			case []any:
				if len(typed) > 0 {
					result[key] = asString(typed[0])
				}
			}
		}
		if len(result) > 0 {
			return result
		}
	}
	return nil
}

func asString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return ""
	}
}

func asInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case uint64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		result, _ := strconv.Atoi(typed)
		return result
	default:
		return 0
	}
}

func asBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return parseBool(typed)
	default:
		return false
	}
}

func asStringSlice(value any) []string {
	switch typed := value.(type) {
	case string:
		return splitCommaList(typed)
	case []string:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if item = strings.TrimSpace(item); item != "" {
				result = append(result, item)
			}
		}
		return result
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(asString(item)); text != "" {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}

func asMap(value any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	return map[string]any{}
}
