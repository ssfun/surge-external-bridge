package mihomo

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	MConfig "github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	_ "github.com/metacubex/mihomo/hub/executor"
	"gopkg.in/yaml.v3"
)

var stableIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

type ProviderDefinition struct {
	StableID           string
	Name               string
	Type               string
	URL                string
	FilePath           string
	Payload            []map[string]any
	Headers            map[string][]string
	RefreshSeconds     int
	DownloadProxy      string
	SizeLimit          int64
	HealthCheck        bool
	HealthCheckURL     string
	HealthCheckSeconds int
	HealthCheckTimeout int
	HealthCheckLazy    bool
	ExpectedStatus     string
	IncludeName        string
	ExcludeName        string
	IncludeTypes       []C.AdapterType
}

func ProviderKey(stableID string) (string, error) {
	if !stableIDPattern.MatchString(stableID) {
		return "", errors.New("provider stable ID must use 1-80 ASCII letters, digits, underscore, or hyphen")
	}
	return "surgeeb-provider-" + stableID, nil
}

func BuildControlledConfig(homeDir, controllerSocket, controllerSecret string, providers []ProviderDefinition, store *SnapshotStore) (*MConfig.Config, error) {
	if store == nil {
		return nil, errors.New("projection Snapshot store is required")
	}
	homeDir, err := filepath.Abs(homeDir)
	if err != nil || homeDir == "" {
		return nil, errors.New("valid Mihomo HomeDir is required")
	}
	if err := EnsurePrivateDir(homeDir); err != nil {
		return nil, err
	}
	controllerSocket, err = filepath.Abs(controllerSocket)
	if err != nil || !pathWithin(homeDir, controllerSocket) {
		return nil, errors.New("Controller socket must be inside Mihomo HomeDir")
	}

	providerMappings := make(map[string]any, len(providers))
	seen := make(map[string]struct{}, len(providers))
	for _, definition := range providers {
		key, err := ProviderKey(definition.StableID)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", definition.Name, err)
		}
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("duplicate provider stable ID %q", definition.StableID)
		}
		seen[key] = struct{}{}
		mapping, err := providerMapping(homeDir, definition)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", definition.Name, err)
		}
		providerMappings[key] = mapping
	}
	raw := map[string]any{
		"port": 0, "socks-port": 0, "mixed-port": 0, "redir-port": 0, "tproxy-port": 0,
		"allow-lan": false, "bind-address": "127.0.0.1", "mode": "rule", "log-level": "info", "ipv6": true,
		"external-controller": "", "external-controller-tls": "", "external-controller-unix": controllerSocket,
		"external-controller-pipe": "", "secret": controllerSecret,
		"tun":             map[string]any{"enable": false, "auto-route": false, "auto-redirect": false},
		"dns":             map[string]any{"enable": false, "listen": ""},
		"iptables":        map[string]any{"enable": false, "dns-redirect": false},
		"ntp":             map[string]any{"enable": false, "write-to-system": false},
		"sniffer":         map[string]any{"enable": false},
		"profile":         map[string]any{"store-selected": false, "store-fake-ip": false},
		"geo-auto-update": false,
		"listeners":       []any{},
		"tunnels":         []any{},
		"proxies":         []any{},
		"proxy-providers": providerMappings,
		"rules":           []string{},
	}
	encoded, err := yaml.Marshal(raw)
	if err != nil {
		return nil, err
	}
	C.SetHomeDir(homeDir)
	cfg, err := MConfig.Parse(encoded)
	if err != nil {
		return nil, fmt.Errorf("parse controlled Mihomo configuration: %w", err)
	}
	if cfg.Proxies == nil {
		cfg.Proxies = make(map[string]C.Proxy)
	}
	cfg.Proxies[RouterName] = WrappedRouter(store)
	if err := ValidateControlledConfig(cfg, homeDir); err != nil {
		return nil, err
	}
	return cfg, nil
}

func providerMapping(homeDir string, definition ProviderDefinition) (map[string]any, error) {
	providerType := strings.ToLower(strings.TrimSpace(definition.Type))
	if providerType == "" {
		providerType = "http"
	}
	mapping := map[string]any{"type": providerType}
	switch providerType {
	case "http":
		parsed, err := url.Parse(definition.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return nil, errors.New("HTTP Provider URL must use http or https")
		}
		mapping["url"] = definition.URL
	case "file":
		if definition.FilePath == "" {
			return nil, errors.New("file Provider path is required")
		}
		path := definition.FilePath
		if !filepath.IsAbs(path) {
			path = filepath.Join(homeDir, path)
		}
		if !pathWithin(homeDir, path) {
			return nil, errors.New("file Provider path must stay inside Mihomo HomeDir")
		}
		pathInfo, err := os.Lstat(path)
		if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() {
			return nil, errors.New("file Provider path must be a regular file, not a symbolic link")
		}
		resolvedHome, err := filepath.EvalSymlinks(homeDir)
		if err != nil {
			return nil, fmt.Errorf("resolve Mihomo HomeDir: %w", err)
		}
		resolvedPath, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, fmt.Errorf("resolve file Provider path: %w", err)
		}
		info, err := os.Stat(resolvedPath)
		if err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("file Provider path must be a regular file")
		}
		if !pathWithin(resolvedHome, resolvedPath) {
			return nil, errors.New("file Provider symbolic link must stay inside Mihomo HomeDir")
		}
		// Keep Mihomo's lexical path under its configured HomeDir. On macOS
		// /tmp resolves to /private/tmp, and passing the resolved spelling would
		// be rejected by Mihomo's own safe-path check even though both identify
		// the same private file. The resolved path above is used only to prove
		// that no symbolic link escapes the private HomeDir.
		mapping["path"] = path
	case "inline":
		if len(definition.Payload) == 0 {
			return nil, errors.New("inline Provider payload is empty")
		}
		mapping["payload"] = definition.Payload
	default:
		return nil, fmt.Errorf("unsupported Provider type %q", definition.Type)
	}
	// Mihomo v1.19.30's Fetcher pull loop reads updatedAt without the mutex
	// used by its public Update method. Manager serializes both scheduled and
	// manual native Update calls instead, so do not start the upstream ticker.
	if len(definition.Headers) > 0 {
		mapping["header"] = definition.Headers
	}
	if definition.DownloadProxy != "" {
		mapping["proxy"] = definition.DownloadProxy
	}
	if definition.SizeLimit > 0 {
		mapping["size-limit"] = definition.SizeLimit
	}
	if definition.HealthCheck {
		mapping["health-check"] = map[string]any{
			"enable": true, "url": definition.HealthCheckURL, "interval": definition.HealthCheckSeconds,
			"timeout": definition.HealthCheckTimeout, "lazy": definition.HealthCheckLazy,
			"expected-status": definition.ExpectedStatus,
		}
	}
	return mapping, nil
}

func ProviderViews(cfg *MConfig.Config, definitions []ProviderDefinition) ([]ProviderView, error) {
	views := make([]ProviderView, 0, len(definitions))
	for _, definition := range definitions {
		key, err := ProviderKey(definition.StableID)
		if err != nil {
			return nil, err
		}
		provider := cfg.Providers[key]
		if provider == nil {
			return nil, fmt.Errorf("Mihomo Provider %q is missing", key)
		}
		views = append(views, ProviderView{
			StableID: definition.StableID, Name: definition.Name, Proxies: provider.Proxies(),
			IncludeName: definition.IncludeName, ExcludeName: definition.ExcludeName, IncludeTypes: definition.IncludeTypes,
		})
	}
	return views, nil
}
