package gateway

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	M "github.com/ssfun/surge-external-bridge/internal/mihomo"
)

var Version = "0.2.0-dev"
var BuildVersionMarker = "surgeeb-version:0.2.0-dev"

type App struct {
	mu               sync.RWMutex
	applyMu          sync.Mutex
	store            *Store
	masterKey        []byte
	config           Config
	manager          *M.Manager
	events           []Event
	controllerSocket string
	controllerSecret string
}

func New(dataDir string) (*App, error) {
	if BuildVersionMarker != "surgeeb-version:"+Version {
		return nil, errors.New("binary build metadata is inconsistent; rebuild with the official Makefile")
	}
	store := NewStore(dataDir)
	config, err := store.Load()
	if err != nil {
		return nil, err
	}
	if err := ValidateConfig(config); err != nil {
		return nil, fmt.Errorf("invalid gateway configuration: %w", err)
	}
	masterKey := projectionMasterKey(config.ProjectionKey)
	controllerKey, err := M.LoadOrCreatePrivateKey(filepath.Join(dataDir, "controller.key"))
	if err != nil {
		return nil, err
	}
	controllerSocket := filepath.Join(dataDir, "mihomo", "controller.sock")
	controllerSecret := base64.RawURLEncoding.EncodeToString(controllerKey)
	application := &App{store: store, config: config, masterKey: append([]byte(nil), masterKey...), controllerSocket: controllerSocket, controllerSecret: controllerSecret}
	for _, notice := range store.Notices() {
		application.addEvent("warn", notice)
	}
	if err := application.reconcileProviderUploads(config); err != nil {
		application.addEvent("warn", "Provider 上传目录对账失败，将在下次启动重试: "+err.Error())
	}
	manager, err := M.NewManager(M.ManagerOptions{
		HomeDir: filepath.Join(dataDir, "mihomo"), ControllerSocket: controllerSocket,
		ControllerSecret: controllerSecret,
		SocksBind:        config.SocksBind, SocksAdvertise: config.SocksHost, SocksPort: config.SocksPort,
		MasterKey: masterKey, PrefixProvider: config.PrefixProvider, Providers: definitions(config),
		OnEvent: application.addEvent,
	})
	if err != nil {
		return nil, err
	}
	application.manager = manager
	if err := manager.Start(); err != nil {
		application.addEvent("error", "Mihomo 启动失败: "+err.Error())
		// Keep the authenticated management plane available so deployment or
		// Provider configuration can be repaired. The data plane remains closed.
		return application, nil
	}
	return application, nil
}

func (a *App) Close() error    { return a.manager.Stop() }
func (a *App) DataDir() string { return a.store.Dir() }

// ControllerAccess is intentionally consumed only by the authenticated product
// facade. Neither value is serialized into public configuration or APIs.
func (a *App) ControllerAccess() (socket, secret string) {
	return a.controllerSocket, a.controllerSecret
}

func (a *App) Config() Config {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return cloneConfig(a.config)
}

func (a *App) Status() M.ManagerStatus {
	status := a.manager.Status()
	a.mu.RLock()
	status.LastError = a.redactEventLocked(status.LastError)
	a.mu.RUnlock()
	return status
}

type SecurityStatus struct {
	DataDirectoryProtected bool `json:"data_directory_protected"`
	ConfigurationProtected bool `json:"configuration_protected"`
	ControllerKeyProtected bool `json:"controller_key_protected"`
}

// SecurityStatus intentionally reports only protection booleans. Local paths
// remain private even when the management UI is accessed from a trusted LAN.
func (a *App) SecurityStatus() SecurityStatus {
	dir := a.store.Dir()
	return SecurityStatus{
		DataDirectoryProtected: privatePathMode(dir, true, 0o700) && M.PrivateTreeProtected(filepath.Join(dir, "mihomo")),
		ConfigurationProtected: privatePathMode(filepath.Join(dir, "gateway.json"), false, 0o600),
		ControllerKeyProtected: privatePathMode(filepath.Join(dir, "controller.key"), false, 0o600),
	}
}

func privatePathMode(path string, directory bool, permission os.FileMode) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != permission {
		return false
	}
	return info.IsDir() == directory
}
func (a *App) Snapshot() *M.Snapshot { return a.manager.Snapshot() }

func (a *App) Events() []Event {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]Event(nil), a.events...)
}

func (a *App) Providers() []Provider { return a.Config().Providers }

func (a *App) Provider(id string) (Provider, bool) {
	for _, provider := range a.Providers() {
		if provider.StableID == id {
			return provider, true
		}
	}
	return Provider{}, false
}

func (a *App) Node(id string) (M.Entry, bool) { return a.manager.Snapshot().EntryByID(id) }

func (a *App) SurgeLine(id string) (string, error) {
	entry, ok := a.Node(id)
	if !ok {
		return "", errors.New("Node not found")
	}
	return formatSurgeLine(entry), nil
}

func (a *App) AddProvider(provider Provider) (Provider, error) {
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	assignProviderID(&provider)
	if provider.Type == "" {
		provider.Type = "http"
	}
	if provider.Type == "http" {
		if provider.RefreshSeconds == 0 {
			provider.RefreshSeconds = 21600
		}
		if provider.SizeLimit == 0 {
			provider.SizeLimit = 16 << 20
		}
	}
	normalizeProviderSource(&provider)
	if provider.HealthCheck {
		if provider.HealthCheckURL == "" {
			provider.HealthCheckURL = "https://www.gstatic.com/generate_204"
		}
		if provider.HealthCheckSeconds == 0 {
			provider.HealthCheckSeconds = 300
		}
		if provider.HealthCheckTimeout == 0 {
			provider.HealthCheckTimeout = 5000
		}
		if provider.ExpectedStatus == "" {
			provider.ExpectedStatus = "200-399"
		}
	}
	if err := validateProvider(provider); err != nil {
		return Provider{}, err
	}
	a.mu.RLock()
	candidate := cloneConfig(a.config)
	a.mu.RUnlock()
	for _, existing := range candidate.Providers {
		if existing.StableID == provider.StableID || strings.EqualFold(existing.Name, provider.Name) {
			return Provider{}, errors.New("Provider ID or name already exists")
		}
	}
	candidate.Providers = append(candidate.Providers, provider)
	if err := a.applyConfig(candidate); err != nil {
		return Provider{}, err
	}
	return provider, nil
}

func (a *App) UpdateProvider(id string, provider Provider) (Provider, error) {
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	a.mu.RLock()
	candidate := cloneConfig(a.config)
	a.mu.RUnlock()
	found := false
	previousFilePath := ""
	for index := range candidate.Providers {
		if candidate.Providers[index].StableID != id {
			continue
		}
		existing := candidate.Providers[index]
		previousFilePath = existing.FilePath
		assignProviderID(&provider)
		sameType := provider.Type == existing.Type
		if provider.Type == "http" && !sameType {
			if provider.RefreshSeconds == 0 {
				provider.RefreshSeconds = 21600
			}
			if provider.SizeLimit == 0 {
				provider.SizeLimit = 16 << 20
			}
		}
		if provider.Type == "http" && sameType && provider.URL == "" {
			provider.URL = existing.URL
		}
		if provider.Type == "http" && sameType && provider.Headers == nil {
			provider.Headers = existing.Headers
		}
		if provider.Type == "http" && sameType && provider.SizeLimit == 0 {
			provider.SizeLimit = existing.SizeLimit
		}
		if provider.Type == "file" && sameType && provider.FilePath == "" {
			provider.FilePath = existing.FilePath
		}
		if provider.Type == "inline" && sameType && provider.Payload == nil {
			provider.Payload = existing.Payload
		}
		if provider.HealthCheckURL == "" {
			provider.HealthCheckURL = existing.HealthCheckURL
		}
		if provider.HealthCheck && provider.HealthCheckSeconds == 0 {
			provider.HealthCheckSeconds = existing.HealthCheckSeconds
		}
		if provider.HealthCheck && provider.HealthCheckTimeout == 0 {
			provider.HealthCheckTimeout = existing.HealthCheckTimeout
		}
		if provider.HealthCheck && provider.ExpectedStatus == "" {
			provider.ExpectedStatus = existing.ExpectedStatus
		}
		normalizeProviderSource(&provider)
		if err := validateProvider(provider); err != nil {
			return Provider{}, err
		}
		candidate.Providers[index] = provider
		found = true
		break
	}
	if !found {
		return Provider{}, errors.New("Provider not found")
	}
	if err := a.applyConfig(candidate); err != nil {
		return Provider{}, err
	}
	if previousFilePath != provider.FilePath {
		_ = a.DiscardProviderUpload(previousFilePath)
	}
	return provider, nil
}

func normalizeProviderSource(provider *Provider) bool {
	changed := false
	switch provider.Type {
	case "http":
		changed = provider.FilePath != "" || provider.Payload != nil
		provider.FilePath = ""
		provider.Payload = nil
	case "file":
		changed = provider.URL != "" || provider.Payload != nil || provider.Headers != nil || provider.RefreshSeconds != 0 || provider.DownloadProxy != "" || provider.SizeLimit != 0
		provider.URL = ""
		provider.Payload = nil
		provider.Headers = nil
		provider.RefreshSeconds = 0
		provider.DownloadProxy = ""
		provider.SizeLimit = 0
	case "inline":
		changed = provider.URL != "" || provider.FilePath != "" || provider.Headers != nil || provider.RefreshSeconds != 0 || provider.DownloadProxy != "" || provider.SizeLimit != 0
		provider.URL = ""
		provider.FilePath = ""
		provider.Headers = nil
		provider.RefreshSeconds = 0
		provider.DownloadProxy = ""
		provider.SizeLimit = 0
	}
	return changed
}

func (a *App) DeleteProvider(id string) error {
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	a.mu.RLock()
	candidate := cloneConfig(a.config)
	a.mu.RUnlock()
	index := -1
	filePath := ""
	for current := range candidate.Providers {
		if candidate.Providers[current].StableID == id {
			index = current
			filePath = candidate.Providers[current].FilePath
			break
		}
	}
	if index < 0 {
		return errors.New("Provider not found")
	}
	candidate.Providers = append(candidate.Providers[:index], candidate.Providers[index+1:]...)
	if err := a.applyConfig(candidate); err != nil {
		return err
	}
	_ = a.DiscardProviderUpload(filePath)
	return nil
}

func (a *App) RefreshProvider(id string) error {
	provider, _ := a.Provider(id)
	err := a.manager.RefreshProvider(id)
	if err != nil {
		a.addEvent("warn", fmt.Sprintf("Provider %s 刷新失败: %v", provider.Name, err))
		return err
	}
	a.addEvent("info", fmt.Sprintf("Provider %s 已刷新", provider.Name))
	return nil
}

func (a *App) HealthCheckProvider(id string) error {
	provider, _ := a.Provider(id)
	if err := a.manager.HealthCheckProvider(id); err != nil {
		a.addEvent("warn", fmt.Sprintf("Provider %s 节点检查启动失败: %v", provider.Name, err))
		return err
	}
	a.addEvent("info", fmt.Sprintf("Provider %s 已开始检查节点可用性", provider.Name))
	return nil
}

func (a *App) ProviderRuntimeState(id string) (time.Time, string) {
	nextRefresh, lastError := a.manager.ProviderState(id)
	a.mu.RLock()
	defer a.mu.RUnlock()
	return nextRefresh, a.redactEventLocked(lastError)
}

func (a *App) ProviderFilterState(id string) M.ProviderFilterReport {
	return a.manager.ProviderFilterState(id)
}

func (a *App) UpdateSettings(settings Settings) error {
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	a.mu.RLock()
	previous := cloneConfig(a.config)
	previousKey := append([]byte(nil), a.masterKey...)
	a.mu.RUnlock()
	candidate := cloneConfig(previous)
	candidate.SetSettings(settings)
	if err := ValidateConfig(candidate); err != nil {
		return err
	}
	nextKey := projectionMasterKey(candidate.ProjectionKey)
	wasRunning := a.manager.Status().State == "running"
	if wasRunning {
		if err := a.manager.ApplyProjectionSettings(candidate.SocksBind, candidate.SocksHost, candidate.SocksPort, candidate.PrefixProvider, nextKey); err != nil {
			return err
		}
	} else {
		if err := a.manager.ConfigureWhenStopped(candidate.SocksBind, candidate.SocksHost, candidate.SocksPort, candidate.PrefixProvider, nextKey, definitions(candidate)); err != nil {
			return err
		}
		if err := a.manager.Start(); err != nil {
			if restoreErr := a.restoreStoppedSettings(previous, previousKey); restoreErr != nil {
				return errors.Join(err, fmt.Errorf("restore persisted stopped configuration: %w", restoreErr))
			}
			return err
		}
	}
	if err := a.store.Save(candidate); err != nil {
		var rollbackErr error
		if wasRunning {
			rollbackErr = a.manager.ApplyProjectionSettings(previous.SocksBind, previous.SocksHost, previous.SocksPort, previous.PrefixProvider, previousKey)
		} else {
			rollbackErr = a.restoreStoppedSettings(previous, previousKey)
		}
		return rollbackAfterPersistenceFailure(err, rollbackErr, a.manager.Stop)
	}
	a.mu.Lock()
	a.config = candidate
	a.masterKey = append([]byte(nil), nextKey...)
	a.mu.Unlock()
	a.addEvent("info", "使用范围与节点凭据设置已更新")
	return nil
}

func (a *App) applyConfig(candidate Config) error {
	if err := ValidateConfig(candidate); err != nil {
		return err
	}
	a.mu.RLock()
	previous := cloneConfig(a.config)
	previousKey := append([]byte(nil), a.masterKey...)
	a.mu.RUnlock()
	wasRunning := a.manager.Status().State == "running"
	var applyErr error
	if wasRunning {
		applyErr = a.manager.ApplyProviders(definitions(candidate))
	} else {
		applyErr = a.manager.StartWithProviders(definitions(candidate))
	}
	if applyErr != nil {
		if !wasRunning {
			if restoreErr := a.restoreStoppedSettings(previous, previousKey); restoreErr != nil {
				return errors.Join(applyErr, fmt.Errorf("restore persisted stopped configuration: %w", restoreErr))
			}
		}
		return applyErr
	}
	if err := a.store.Save(candidate); err != nil {
		var rollbackErr error
		if wasRunning {
			rollbackErr = a.manager.ApplyProviders(definitions(previous))
		} else {
			rollbackErr = a.restoreStoppedSettings(previous, previousKey)
		}
		return rollbackAfterPersistenceFailure(err, rollbackErr, a.manager.Stop)
	}
	a.mu.Lock()
	a.config = cloneConfig(candidate)
	a.mu.Unlock()
	a.addEvent("info", "Provider 配置已更新")
	return nil
}

func (a *App) Proxies() (string, string, error) {
	if status := a.Status(); status.State != "running" {
		return "", "", errors.New("Mihomo 身份数据面当前不可用")
	}
	snapshot := a.manager.Snapshot()
	entries := snapshot.Entries()
	if len(entries) == 0 {
		return "", snapshot.Revision(), nil
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		lines = append(lines, formatSurgeLine(entry))
	}
	return strings.Join(lines, "\n") + "\n", snapshot.Revision(), nil
}

func formatSurgeLine(entry M.Entry) string {
	return fmt.Sprintf("%s = socks5, %s, %d, username=%s, password=%s, udp-relay=%t", entry.DisplayName, entry.SocksHost, entry.SocksPort, entry.Username, entry.Password, entry.SupportUDP)
}

func (a *App) restoreStoppedSettings(previous Config, previousKey []byte) error {
	if err := a.manager.Stop(); err != nil {
		return fmt.Errorf("stop unpersisted runtime: %w", err)
	}
	if err := a.manager.ConfigureWhenStopped(previous.SocksBind, previous.SocksHost, previous.SocksPort, previous.PrefixProvider, previousKey, definitions(previous)); err != nil {
		return fmt.Errorf("restore previous stopped configuration: %w", err)
	}
	return nil
}

func rollbackAfterPersistenceFailure(persistErr, rollbackErr error, stop func() error) error {
	if rollbackErr == nil {
		return persistErr
	}
	stopErr := stop()
	return errors.Join(
		persistErr,
		fmt.Errorf("runtime rollback failed; data plane stopped: %w", rollbackErr),
		wrapStopAfterRollbackError(stopErr),
	)
}

func wrapStopAfterRollbackError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("stop runtime after rollback failure: %w", err)
}

func (a *App) addEvent(level, message string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	message = a.redactEventLocked(message)
	a.events = append(a.events, Event{Time: time.Now().UTC(), Level: level, Message: message})
	if len(a.events) > 200 {
		a.events = append([]Event(nil), a.events[len(a.events)-200:]...)
	}
}

var eventUUIDPattern = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\b`)
var eventSecretPattern = regexp.MustCompile(`(?i)\b(token|password|passwd|secret|authorization|cookie)\s*[:=]\s*([^\s,;]+)`)
var eventURLPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)

func (a *App) redactEventLocked(message string) string {
	secrets := []string{a.config.ManagementToken, a.config.PolicyToken, a.config.ProjectionKey, a.store.Dir()}
	for _, provider := range a.config.Providers {
		secrets = append(secrets, provider.URL, provider.FilePath)
		for _, values := range provider.Headers {
			secrets = append(secrets, values...)
		}
	}
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "<redacted>")
		}
	}
	message = eventUUIDPattern.ReplaceAllString(message, "<uuid>")
	message = eventURLPattern.ReplaceAllString(message, "<url>")
	return eventSecretPattern.ReplaceAllString(message, "$1=<redacted>")
}

func definitions(config Config) []M.ProviderDefinition {
	result := make([]M.ProviderDefinition, 0, len(config.Providers))
	for _, provider := range config.Providers {
		if !provider.Enabled {
			continue
		}
		result = append(result, M.ProviderDefinition{
			StableID: provider.StableID, Name: provider.Name, Type: provider.Type, URL: provider.URL, FilePath: provider.FilePath, Payload: provider.Payload,
			Headers: provider.Headers, RefreshSeconds: provider.RefreshSeconds, DownloadProxy: provider.DownloadProxy, SizeLimit: provider.SizeLimit,
			HealthCheck: provider.HealthCheck, HealthCheckURL: provider.HealthCheckURL, HealthCheckSeconds: provider.HealthCheckSeconds,
			HealthCheckTimeout: provider.HealthCheckTimeout, HealthCheckLazy: provider.HealthCheckLazy, ExpectedStatus: provider.ExpectedStatus,
			IncludeName: provider.IncludeName, ExcludeName: provider.ExcludeName,
		})
	}
	return result
}

func ValidateConfig(config Config) error {
	if config.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema version must be %d", SchemaVersion)
	}
	if config.Mode != ModeLocal && config.Mode != ModeGateway {
		return errors.New("mode must be local or gateway")
	}
	httpHost, err := hostWithValidPort(config.HTTPBind)
	if err != nil || net.ParseIP(strings.Trim(httpHost, "[]")) == nil {
		return errors.New("invalid HTTP bind address")
	}
	if net.ParseIP(config.SocksBind) == nil || config.SocksPort == 0 {
		return errors.New("invalid SOCKS bind or port")
	}
	if !isValidPublishedHost(config.SocksHost) || isUnspecifiedHost(config.SocksHost) {
		return errors.New("SOCKS host must be a hostname or non-unspecified IP address without a port")
	}
	if !isValidPublishedHost(config.PolicyHost) || isUnspecifiedHost(config.PolicyHost) {
		return errors.New("Policy host must be a hostname or non-unspecified IP address without a port")
	}
	if len(config.ProjectionKey) < 16 || len(config.ProjectionKey) > 256 || config.ProjectionKey != strings.TrimSpace(config.ProjectionKey) || strings.ContainsAny(config.ProjectionKey, "\x00\r\n") {
		return errors.New("projection key must contain 16-256 non-control characters without surrounding whitespace")
	}
	socksIP, policyIP := net.ParseIP(config.SocksHost), net.ParseIP(config.PolicyHost)
	if config.ManagementToken != "" && len(config.ManagementToken) < 16 || !validPolicyToken(config.PolicyToken, false) || config.ManagementToken != "" && config.ManagementToken == config.PolicyToken {
		return errors.New("Management Token and Policy Token must each contain at least 16 characters when configured, and the two values must differ")
	}
	if config.Mode == ModeLocal && (!isLoopbackHost(httpHost) || !isLoopbackHost(config.SocksBind)) {
		return errors.New("local mode requires loopback HTTP and SOCKS bind addresses")
	}
	if config.Mode == ModeLocal && (socksIP != nil && !socksIP.IsLoopback() || policyIP != nil && !policyIP.IsLoopback()) {
		return errors.New("local mode SOCKS and Policy host IPs must be loopback")
	}
	if config.Mode == ModeGateway && (len(config.ManagementToken) < 16 || !validPolicyToken(config.PolicyToken, true) || config.ManagementToken == config.PolicyToken) {
		return errors.New("gateway mode requires distinct Management and Policy Tokens of at least 16 characters")
	}
	if config.Mode == ModeGateway && (socksIP != nil && !isPrivateOrTrustedHost(config.SocksHost) || policyIP != nil && !isPrivateOrTrustedHost(config.PolicyHost)) {
		return errors.New("gateway mode SOCKS and Policy host IPs must be private or trusted")
	}
	if len(config.ProjectionTypes) != 1 || config.ProjectionTypes[0] != "*" {
		return errors.New("projection protocol scope must include all Mihomo Provider protocols")
	}
	testURL, err := url.Parse(config.NodeTestURL)
	if err != nil || (testURL.Scheme != "http" && testURL.Scheme != "https") || testURL.Hostname() == "" {
		return errors.New("node test URL must be an absolute HTTP or HTTPS URL")
	}
	if host, err := hostWithValidPort(config.NodeTestUDP); err != nil || strings.TrimSpace(host) == "" {
		return errors.New("node test UDP address must include a host and port")
	}
	if config.NodeTestTimeout < 1 || config.NodeTestTimeout > 120 {
		return errors.New("node test timeout must be between 1 and 120 seconds")
	}
	seen := map[string]bool{}
	names := make([]string, 0, len(config.Providers))
	for _, provider := range config.Providers {
		if provider.StableID != stableProviderID(provider.Name) {
			return fmt.Errorf("Provider %q: stable ID must be derived from its name", provider.Name)
		}
		if seen[provider.StableID] {
			return errors.New("duplicate Provider stable ID")
		}
		seen[provider.StableID] = true
		for _, name := range names {
			if strings.EqualFold(name, provider.Name) {
				return errors.New("duplicate Provider name")
			}
		}
		names = append(names, provider.Name)
		if err := validateProvider(provider); err != nil {
			return fmt.Errorf("Provider %q: %w", provider.Name, err)
		}
	}
	return nil
}

func hostWithValidPort(address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" || strings.IndexFunc(port, func(character rune) bool { return character < '0' || character > '9' }) >= 0 {
		return "", errors.New("invalid host or port")
	}
	value, err := strconv.ParseUint(port, 10, 16)
	if err != nil || value == 0 {
		return "", errors.New("invalid host or port")
	}
	return host, nil
}

func validPolicyToken(token string, required bool) bool {
	if token == "" {
		return !required
	}
	return len(token) >= 16
}

func validateProvider(provider Provider) error {
	if _, err := M.ProviderKey(provider.StableID); err != nil {
		return err
	}
	if strings.TrimSpace(provider.Name) == "" {
		return errors.New("name is required")
	}
	if provider.Type != "http" && provider.Type != "file" && provider.Type != "inline" {
		return errors.New("type must be http, file, or inline")
	}
	if provider.Type == "http" {
		parsed, err := url.Parse(provider.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return errors.New("URL must use http or https")
		}
		if provider.Enabled && parsed.User != nil {
			return errors.New("URL userinfo is not allowed; use a token query parameter instead")
		}
	}
	if provider.Type == "file" && strings.TrimSpace(provider.FilePath) == "" {
		return errors.New("file path is required")
	}
	if provider.Type == "inline" && len(provider.Payload) == 0 {
		return errors.New("inline payload is required")
	}
	if provider.RefreshSeconds != 0 && provider.RefreshSeconds < 60 {
		return errors.New("refresh interval must be zero or at least 60 seconds")
	}
	if provider.Type == "http" && (provider.SizeLimit < 1024 || provider.SizeLimit > 128<<20) {
		return errors.New("HTTP Provider response limit must be between 1 KiB and 128 MiB")
	}
	if provider.HealthCheck {
		parsed, err := url.Parse(provider.HealthCheckURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return errors.New("health check URL must use http or https")
		}
		if provider.HealthCheckSeconds < 60 {
			return errors.New("health check interval must be at least 60 seconds")
		}
		if provider.HealthCheckTimeout < 100 || provider.HealthCheckTimeout > 120000 {
			return errors.New("health check timeout must be between 100 and 120000 milliseconds")
		}
	}
	if _, err := regexp.Compile(provider.IncludeName); provider.IncludeName != "" && err != nil {
		return fmt.Errorf("invalid include expression: %w", err)
	}
	if _, err := regexp.Compile(provider.ExcludeName); provider.ExcludeName != "" && err != nil {
		return fmt.Errorf("invalid exclude expression: %w", err)
	}
	for name, values := range provider.Headers {
		if strings.ContainsAny(name, "\r\n:") {
			return errors.New("invalid Provider Header name")
		}
		switch strings.ToLower(name) {
		case "user-agent", "accept", "accept-language":
		default:
			if !provider.Enabled && (strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Cookie")) {
				continue
			}
			return fmt.Errorf("Provider Header %q is not allowed; Authorization and Cookie are forbidden because redirects can leak them; use User-Agent, Accept, or Accept-Language", name)
		}
		for _, value := range values {
			if strings.ContainsAny(value, "\r\n") {
				return errors.New("invalid Provider Header value")
			}
		}
	}
	return nil
}

func providerHasRedirectSensitiveCredentials(provider Provider) bool {
	if parsed, err := url.Parse(provider.URL); err == nil && parsed.User != nil {
		return true
	}
	for name := range provider.Headers {
		if strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Cookie") {
			return true
		}
	}
	return false
}

func cloneConfig(config Config) Config {
	clone := config
	clone.ProjectionTypes = append([]string(nil), config.ProjectionTypes...)
	clone.Providers = append([]Provider(nil), config.Providers...)
	for index := range clone.Providers {
		if config.Providers[index].Headers != nil {
			clone.Providers[index].Headers = make(map[string][]string, len(config.Providers[index].Headers))
			for name, values := range config.Providers[index].Headers {
				clone.Providers[index].Headers[name] = append([]string(nil), values...)
			}
		}
		if config.Providers[index].Payload != nil {
			clone.Providers[index].Payload = make(InlinePayload, len(config.Providers[index].Payload))
			for payloadIndex, item := range config.Providers[index].Payload {
				clone.Providers[index].Payload[payloadIndex] = make(map[string]any, len(item))
				for key, value := range item {
					clone.Providers[index].Payload[payloadIndex][key] = value
				}
			}
		}
	}
	return clone
}

func assignProviderID(provider *Provider) {
	provider.Name = strings.TrimSpace(provider.Name)
	provider.StableID = stableProviderID(provider.Name)
}

func assignProviderIDs(config *Config) {
	for index := range config.Providers {
		assignProviderID(&config.Providers[index])
	}
}

func stableProviderID(name string) string {
	sum := sha256.Sum256([]byte("SurgeEB Provider v1\x00" + strings.TrimSpace(name)))
	return "p_" + hex.EncodeToString(sum[:12])
}

func projectionMasterKey(key string) []byte {
	sum := sha256.Sum256([]byte("SurgeEB Projection Key v1\x00" + key))
	return append([]byte(nil), sum[:]...)
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.TrimSuffix(host, "."), "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isUnspecifiedHost(host string) bool {
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsUnspecified()
}

func isValidPublishedHost(raw string) bool {
	if raw == "" || raw != strings.TrimSpace(raw) || strings.ContainsAny(raw, "[]") {
		return false
	}
	host := strings.TrimSuffix(raw, ".")
	if host == "" || len(host) > 253 {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	if strings.Contains(host, ":") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func requestHostname(value string) string {
	value = strings.TrimSpace(value)
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	return strings.TrimSuffix(strings.Trim(value, "[]"), ".")
}

func sameHostname(left, right string) bool {
	return strings.EqualFold(requestHostname(left), requestHostname(right))
}

// AllowsHTTPHost keeps literal loopback/private access available while adding
// exactly the configured Policy hostname. It deliberately does not trust a
// suffix such as .eb as a class.
func AllowsHTTPHost(config Config, requestHost string) bool {
	host := requestHostname(requestHost)
	if host == "" {
		return false
	}
	if sameHostname(host, config.PolicyHost) {
		return true
	}
	bindHost, _, err := net.SplitHostPort(config.HTTPBind)
	if err == nil && sameHostname(host, bindHost) {
		return true
	}
	if config.Mode == ModeLocal {
		return isLoopbackHost(host)
	}
	return config.Mode == ModeGateway && isPrivateOrTrustedHost(host)
}

func isPrivateOrTrustedHost(host string) bool {
	host = strings.Trim(strings.TrimSuffix(host, "."), "[]")
	ip := net.ParseIP(host)
	if ip == nil {
		host = strings.ToLower(host)
		if host == "" {
			return false
		}
		if host == "localhost" || !strings.Contains(host, ".") {
			return true
		}
		for _, suffix := range []string{".local", ".lan", ".internal", ".home.arpa", ".ts.net"} {
			if strings.HasSuffix(host, suffix) {
				return true
			}
		}
		return false
	}
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || cgnat.Contains(ip)
}
