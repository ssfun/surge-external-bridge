package mihomo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	MConfig "github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	P "github.com/metacubex/mihomo/constant/provider"
	"github.com/metacubex/mihomo/hub"
	"github.com/metacubex/mihomo/hub/executor"
	"github.com/metacubex/mihomo/tunnel"
)

var processManager struct {
	sync.Mutex
	owner *Manager
}

var CoreVersion = linkedCoreVersion()

type ManagerOptions struct {
	HomeDir          string
	ControllerSocket string
	ControllerSecret string
	SocksBind        string
	SocksAdvertise   string
	SocksPort        uint16
	MasterKey        []byte
	PrefixProvider   bool
	Providers        []ProviderDefinition
	PollInterval     time.Duration
	OnEvent          func(level, message string)
}

type ManagerStatus struct {
	State           string    `json:"state"`
	CoreVersion     string    `json:"core_version"`
	SocksAddress    string    `json:"socks_address,omitempty"`
	ProjectionHash  string    `json:"projection_hash,omitempty"`
	ProjectionCount int       `json:"projection_count"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
}

type Manager struct {
	applyMu        sync.Mutex
	mu             sync.RWMutex
	options        ManagerOptions
	config         *MConfig.Config
	coreReady      bool
	store          *SnapshotStore
	listener       *SOCKSListener
	cancel         context.CancelFunc
	done           chan struct{}
	versions       map[string]uint32
	nextPull       map[string]time.Time
	providerErrors map[string]string
	status         ManagerStatus
}

func NewManager(options ManagerOptions) (*Manager, error) {
	if options.SocksPort == 0 || strings.TrimSpace(options.SocksBind) == "" || strings.TrimSpace(options.SocksAdvertise) == "" {
		return nil, errors.New("SOCKS bind, advertise, and port are required")
	}
	if len(options.MasterKey) < MasterKeySize {
		return nil, errors.New("projection master key is missing or invalid")
	}
	if options.PollInterval <= 0 {
		options.PollInterval = time.Second
	}
	return &Manager{
		options:        options,
		store:          NewSnapshotStore(nil),
		versions:       make(map[string]uint32),
		nextPull:       make(map[string]time.Time),
		providerErrors: make(map[string]string),
		status:         ManagerStatus{State: "stopped", CoreVersion: CoreVersion},
	}, nil
}

func (m *Manager) Snapshot() *Snapshot { return m.store.Load() }

func (m *Manager) Status() ManagerStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

func (m *Manager) ProviderState(stableID string) (nextRefresh time.Time, lastError string) {
	key, err := ProviderKey(stableID)
	if err != nil {
		return time.Time{}, ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.nextPull[key], m.providerErrors[key]
}

func (m *Manager) Start() error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	m.mu.RLock()
	alreadyRunning := m.listener != nil
	coreReady := m.coreReady
	m.mu.RUnlock()
	if alreadyRunning {
		return errors.New("Mihomo manager is already running")
	}
	processManager.Lock()
	if processManager.owner != nil && processManager.owner != m {
		processManager.Unlock()
		return errors.New("only one embedded Mihomo manager can run in a process")
	}
	processManager.owner = m
	processManager.Unlock()

	m.setState("starting", "")
	cfg, err := BuildControlledConfig(m.options.HomeDir, m.options.ControllerSocket, m.options.ControllerSecret, m.options.Providers, m.store)
	if err != nil {
		m.fail(err)
		if !coreReady {
			m.releaseOwnership()
		}
		return err
	}
	if coreReady {
		if err := applyProviderConfig(cfg); err != nil {
			m.fail(err)
			return err
		}
	} else {
		hub.ApplyConfig(cfg)
		m.mu.Lock()
		m.coreReady = true
		m.config = cfg
		m.mu.Unlock()
	}
	if err := SecurePrivateTree(m.options.HomeDir); err != nil {
		m.fail(err)
		m.shutdownCore(cfg)
		m.mu.Lock()
		m.coreReady = false
		m.config = nil
		m.mu.Unlock()
		m.releaseOwnership()
		return err
	}
	if err := ValidateRuntimeInvariants(cfg, m.options.HomeDir); err != nil {
		m.fail(err)
		m.shutdownCore(cfg)
		m.mu.Lock()
		m.coreReady = false
		m.config = nil
		m.mu.Unlock()
		m.releaseOwnership()
		return err
	}
	if err := SecurePrivateTree(m.options.HomeDir); err != nil {
		m.fail(err)
		m.shutdownCore(cfg)
		m.mu.Lock()
		m.coreReady = false
		m.config = nil
		m.mu.Unlock()
		m.releaseOwnership()
		return err
	}
	if err := m.rebuildProjection(cfg, m.options.Providers); err != nil {
		m.store.Store(EmptySnapshot())
		m.fail(err)
		m.shutdownCore(cfg)
		m.mu.Lock()
		m.coreReady = false
		m.config = nil
		m.mu.Unlock()
		m.releaseOwnership()
		return err
	}
	address := net.JoinHostPort(m.options.SocksBind, fmt.Sprint(m.options.SocksPort))
	listener, err := NewSOCKSListener(address, tunnel.Tunnel, NewAuthenticator(m.store))
	if err != nil {
		m.store.Store(EmptySnapshot())
		m.fail(err)
		m.mu.Lock()
		m.config = cfg
		m.mu.Unlock()
		return err
	}
	if err := validateSOCKSAuthenticationRuntime(listener.Address(), m.store.Load()); err != nil {
		_ = listener.Close()
		m.store.Store(EmptySnapshot())
		m.fail(err)
		m.shutdownCore(cfg)
		m.mu.Lock()
		m.coreReady = false
		m.config = nil
		m.mu.Unlock()
		m.releaseOwnership()
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.config = cfg
	m.listener = listener
	m.cancel = cancel
	m.done = make(chan struct{})
	m.status = ManagerStatus{
		State: "running", CoreVersion: CoreVersion, SocksAddress: listener.Address(),
		ProjectionHash: m.store.Load().Revision(), ProjectionCount: len(m.store.Load().Entries()), StartedAt: time.Now().UTC(),
	}
	done := m.done
	m.captureVersionsLocked(cfg, m.options.Providers)
	m.scheduleProvidersLocked(m.options.Providers, time.Now())
	clear(m.providerErrors)
	m.mu.Unlock()
	go m.watchProviders(ctx, done)
	m.emit("info", fmt.Sprintf("Embedded Mihomo %s started with %d projected nodes", CoreVersion, len(m.store.Load().Entries())))
	return nil
}

func (m *Manager) ApplyProviders(definitions []ProviderDefinition) error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	m.mu.RLock()
	running := m.listener != nil
	m.mu.RUnlock()
	if !running {
		return errors.New("Mihomo manager is not running")
	}
	candidate, err := BuildControlledConfig(m.options.HomeDir, m.options.ControllerSocket, m.options.ControllerSecret, definitions, m.store)
	if err != nil {
		return err
	}
	if err := initializeProviders(candidate.Providers); err != nil {
		return err
	}
	if err := SecurePrivateTree(m.options.HomeDir); err != nil {
		closeProviders(candidate.Providers)
		return err
	}
	candidateSnapshot, err := m.buildProjection(candidate, definitions)
	if err != nil {
		closeProviders(candidate.Providers)
		return err
	}
	// The private Controller and every General setting are immutable for the
	// lifetime of this process. Apply only the Provider/proxy topology that can
	// actually change. Besides avoiding a needless Controller restart, this
	// avoids Mihomo v1.19.30's unsynchronised global log-level write while old
	// outbound finalizers may still be logging.
	replaceProviderConfig(candidate)
	if err := ValidateRuntimeInvariants(candidate, m.options.HomeDir); err != nil {
		m.failClosed(candidate, err)
		return err
	}
	m.store.Store(candidateSnapshot)
	m.mu.Lock()
	m.config = candidate
	m.options.Providers = append([]ProviderDefinition(nil), definitions...)
	m.captureVersionsLocked(candidate, definitions)
	m.scheduleProvidersLocked(definitions, time.Now())
	clear(m.providerErrors)
	m.status.State = "running"
	m.status.LastError = ""
	m.status.ProjectionHash = m.store.Load().Revision()
	m.status.ProjectionCount = len(m.store.Load().Entries())
	m.mu.Unlock()
	m.emit("info", fmt.Sprintf("applied Mihomo Provider configuration with %d projected nodes", len(m.store.Load().Entries())))
	return nil
}

func (m *Manager) StartWithProviders(definitions []ProviderDefinition) error {
	m.mu.Lock()
	if m.listener != nil {
		m.mu.Unlock()
		return errors.New("Mihomo manager is already running")
	}
	m.options.Providers = append([]ProviderDefinition(nil), definitions...)
	m.mu.Unlock()
	return m.Start()
}

func (m *Manager) ConfigureProjectionWhenStopped(bind, advertise string, port uint16, prefixProvider bool, masterKey []byte) error {
	if strings.TrimSpace(bind) == "" || strings.TrimSpace(advertise) == "" || port == 0 || len(masterKey) < MasterKeySize {
		return errors.New("invalid projection settings")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listener != nil {
		return errors.New("Mihomo manager is running")
	}
	m.options.SocksBind, m.options.SocksAdvertise, m.options.SocksPort = bind, advertise, port
	m.options.PrefixProvider = prefixProvider
	m.options.MasterKey = append([]byte(nil), masterKey...)
	return nil
}

// ApplyProjectionSettings changes only the product-owned projection and SOCKS
// topology. Mihomo Providers remain the authoritative node source and the
// Embedded Core is not restarted or reconfigured.
func (m *Manager) ApplyProjectionSettings(bind, advertise string, port uint16, prefixProvider bool, masterKey []byte) error {
	if strings.TrimSpace(bind) == "" || strings.TrimSpace(advertise) == "" || port == 0 {
		return errors.New("SOCKS bind, advertise, and port are required")
	}
	if len(masterKey) < MasterKeySize {
		return errors.New("projection master key is missing or invalid")
	}
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	m.mu.RLock()
	cfg := m.config
	definitions := append([]ProviderDefinition(nil), m.options.Providers...)
	oldListener := m.listener
	oldBind, oldPort := m.options.SocksBind, m.options.SocksPort
	m.mu.RUnlock()
	if cfg == nil || oldListener == nil {
		return errors.New("Mihomo manager is not running")
	}
	views, err := ProviderViews(cfg, definitions)
	if err != nil {
		return err
	}
	candidate, err := BuildProjection(views, BuildOptions{
		MasterKey: masterKey, SocksAdvertise: advertise, SocksPort: port, PrefixProvider: prefixProvider,
	})
	if err != nil {
		return err
	}
	var newListener *SOCKSListener
	if bind != oldBind || port != oldPort {
		address := net.JoinHostPort(bind, fmt.Sprint(port))
		newListener, err = BindSOCKSListener(address, tunnel.Tunnel, NewAuthenticator(m.store))
		if err != nil {
			return err
		}
	}
	m.store.Store(candidate)
	if newListener != nil {
		newListener.Start()
		m.mu.Lock()
		m.listener = newListener
		m.mu.Unlock()
		if err := oldListener.Close(); err != nil {
			m.emit("warn", "old SOCKS listener closed with error: "+err.Error())
		}
	}
	m.mu.Lock()
	m.options.SocksBind, m.options.SocksAdvertise, m.options.SocksPort = bind, advertise, port
	m.options.PrefixProvider = prefixProvider
	m.options.MasterKey = append([]byte(nil), masterKey...)
	m.status.SocksAddress = m.listener.Address()
	m.status.ProjectionHash = candidate.Revision()
	m.status.ProjectionCount = len(candidate.Entries())
	m.status.LastError = ""
	m.mu.Unlock()
	m.emit("info", fmt.Sprintf("projection settings applied with %d nodes", len(candidate.Entries())))
	return nil
}

func (m *Manager) RefreshProvider(stableID string) error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	m.mu.RLock()
	cfg := m.config
	definitions := append([]ProviderDefinition(nil), m.options.Providers...)
	m.mu.RUnlock()
	if cfg == nil {
		return errors.New("Mihomo manager is not running")
	}
	key, err := ProviderKey(stableID)
	if err != nil {
		return err
	}
	provider := cfg.Providers[key]
	if provider == nil {
		return errors.New("Provider not found")
	}
	if err := provider.Update(); err != nil {
		wrapped := fmt.Errorf("refresh Mihomo Provider: %w", err)
		m.mu.Lock()
		m.providerErrors[key] = wrapped.Error()
		m.scheduleProviderLocked(stableID, definitions, time.Now())
		m.mu.Unlock()
		return wrapped
	}
	if err := SecurePrivateTree(m.options.HomeDir); err != nil {
		m.failClosed(cfg, err)
		return err
	}
	if err := m.rebuildProjection(cfg, definitions); err != nil {
		m.store.Store(EmptySnapshot())
		m.mu.Lock()
		m.providerErrors[key] = err.Error()
		m.mu.Unlock()
		m.fail(err)
		return err
	}
	m.mu.Lock()
	delete(m.providerErrors, key)
	m.captureVersionsLocked(cfg, definitions)
	m.scheduleProviderLocked(stableID, definitions, time.Now())
	m.status.ProjectionHash = m.store.Load().Revision()
	m.status.ProjectionCount = len(m.store.Load().Entries())
	m.status.LastError = ""
	m.mu.Unlock()
	return nil
}

func (m *Manager) HealthCheckProvider(stableID string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.config == nil {
		return errors.New("Mihomo manager is not running")
	}
	key, err := ProviderKey(stableID)
	if err != nil {
		return err
	}
	provider := m.config.Providers[key]
	if provider == nil {
		return errors.New("Provider not found")
	}
	provider.HealthCheck()
	return nil
}

func (m *Manager) Stop() error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	m.mu.Lock()
	listener := m.listener
	cancel := m.cancel
	done := m.done
	cfg := m.config
	m.listener = nil
	m.cancel = nil
	m.config = nil
	m.coreReady = false
	m.status.State = "stopping"
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	var err error
	if listener != nil {
		err = listener.Close()
	}
	m.store.Store(EmptySnapshot())
	if cfg != nil {
		m.shutdownCore(cfg)
	}
	m.mu.Lock()
	m.status = ManagerStatus{State: "stopped", CoreVersion: CoreVersion}
	clear(m.providerErrors)
	m.mu.Unlock()
	m.releaseOwnership()
	m.emit("info", "Embedded Mihomo stopped")
	return err
}

func (m *Manager) rebuildProjection(cfg *MConfig.Config, definitions []ProviderDefinition) error {
	snapshot, err := m.buildProjection(cfg, definitions)
	if err != nil {
		return err
	}
	m.store.Store(snapshot)
	return nil
}

func (m *Manager) buildProjection(cfg *MConfig.Config, definitions []ProviderDefinition) (*Snapshot, error) {
	views, err := ProviderViews(cfg, definitions)
	if err != nil {
		return nil, err
	}
	snapshot, err := BuildProjection(views, BuildOptions{
		MasterKey: m.options.MasterKey, SocksAdvertise: m.options.SocksAdvertise,
		SocksPort: m.options.SocksPort, PrefixProvider: m.options.PrefixProvider,
	})
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (m *Manager) watchProviders(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(m.options.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.pollProviders()
		}
	}
}

func (m *Manager) pollProviders() {
	if !m.applyMu.TryLock() {
		return
	}
	defer m.applyMu.Unlock()
	m.mu.RLock()
	cfg := m.config
	if cfg == nil {
		m.mu.RUnlock()
		return
	}
	definitions := append([]ProviderDefinition(nil), m.options.Providers...)
	now := time.Now()
	changed := false
	for _, definition := range definitions {
		key, _ := ProviderKey(definition.StableID)
		if definition.Type == "http" && definition.RefreshSeconds > 0 && !now.Before(m.nextPull[key]) {
			provider := cfg.Providers[key]
			m.mu.RUnlock()
			if provider != nil {
				if err := provider.Update(); err != nil {
					m.mu.Lock()
					m.providerErrors[key] = err.Error()
					m.mu.Unlock()
					m.emit("warn", fmt.Sprintf("scheduled Provider %s refresh failed: %v", definition.Name, err))
				} else {
					if err := SecurePrivateTree(m.options.HomeDir); err != nil {
						// pollProviders runs inside the watcher goroutine whose done
						// channel failClosed waits on. Fail closed from a separate
						// goroutine so the watcher can return and release applyMu.
						go m.failClosed(cfg, err)
						return
					}
					m.mu.Lock()
					delete(m.providerErrors, key)
					m.mu.Unlock()
				}
			}
			m.mu.Lock()
			m.scheduleProviderLocked(definition.StableID, definitions, now)
			m.mu.Unlock()
			m.mu.RLock()
		}
		if provider := cfg.Providers[key]; provider != nil && m.versions[key] != provider.Version() {
			changed = true
			break
		}
	}
	m.mu.RUnlock()
	if !changed {
		return
	}
	if err := SecurePrivateTree(m.options.HomeDir); err != nil {
		go m.failClosed(cfg, err)
		return
	}
	if err := m.rebuildProjection(cfg, definitions); err != nil {
		m.store.Store(EmptySnapshot())
		m.fail(err)
		return
	}
	m.mu.Lock()
	m.captureVersionsLocked(cfg, definitions)
	m.status.ProjectionHash = m.store.Load().Revision()
	m.status.ProjectionCount = len(m.store.Load().Entries())
	m.status.LastError = ""
	m.mu.Unlock()
	m.emit("info", fmt.Sprintf("Provider content update projected %d nodes", len(m.store.Load().Entries())))
}

func (m *Manager) captureVersionsLocked(cfg *MConfig.Config, definitions []ProviderDefinition) {
	clear(m.versions)
	for _, definition := range definitions {
		key, _ := ProviderKey(definition.StableID)
		if provider := cfg.Providers[key]; provider != nil {
			m.versions[key] = provider.Version()
		}
	}
}

func (m *Manager) scheduleProvidersLocked(definitions []ProviderDefinition, now time.Time) {
	clear(m.nextPull)
	for _, definition := range definitions {
		m.scheduleProviderLocked(definition.StableID, definitions, now)
	}
}

func (m *Manager) scheduleProviderLocked(stableID string, definitions []ProviderDefinition, now time.Time) {
	for _, definition := range definitions {
		if definition.StableID != stableID {
			continue
		}
		key, _ := ProviderKey(stableID)
		if definition.Type != "http" || definition.RefreshSeconds <= 0 {
			delete(m.nextPull, key)
			return
		}
		m.nextPull[key] = now.Add(time.Duration(definition.RefreshSeconds) * time.Second)
		return
	}
}

func applyProviderConfig(candidate *MConfig.Config) error {
	if err := initializeProviders(candidate.Providers); err != nil {
		return err
	}
	replaceProviderConfig(candidate)
	return nil
}

func initializeProviders(providers map[string]P.ProxyProvider) error {
	for name, provider := range providers {
		if err := provider.Initial(); err != nil {
			closeProviders(providers)
			return fmt.Errorf("initialize Mihomo Provider %q: %w", name, err)
		}
	}
	return nil
}

func replaceProviderConfig(candidate *MConfig.Config) {
	oldProviders := tunnel.Providers()
	tunnel.OnSuspend()
	tunnel.UpdateProxies(candidate.Proxies, candidate.Providers)
	tunnel.OnRunning()
	closeProviders(oldProviders)
}

func closeProviders(providers map[string]P.ProxyProvider) {
	for _, provider := range providers {
		for _, proxy := range provider.Proxies() {
			_ = proxy.Close()
		}
		if closer, ok := provider.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}
}

func closeProxies(proxies map[string]C.Proxy) {
	for _, proxy := range proxies {
		_ = proxy.Close()
	}
}

func (m *Manager) shutdownCore(previous *MConfig.Config) {
	// A full executor.ApplyConfig during terminal shutdown writes Mihomo's
	// process-global log level while outbound finalizers may still be logging.
	// Detach the only data-plane state this product installs instead. The
	// controlled config guarantees there are no Mihomo proxy/TUN/DNS listeners
	// to tear down, while existing connections may naturally retain old proxy
	// references until the process exits.
	tunnel.OnSuspend()
	tunnel.UpdateRules(nil, nil, nil)
	tunnel.UpdateProxies(map[string]C.Proxy{}, map[string]P.ProxyProvider{})
	closeProviders(previous.Providers)
	closeProxies(previous.Proxies)
	previous.Providers = nil
	previous.Proxies = nil
	previous.Rules = nil
	// Unix listeners remain open until process exit because Mihomo does not
	// expose a synchronous Controller shutdown API. Unlinking the socket makes
	// it immediately unreachable to new clients; the single owning process then
	// exits and releases the remaining file descriptor.
	if previous.Controller != nil && previous.Controller.ExternalControllerUnix != "" {
		_ = os.Remove(previous.Controller.ExternalControllerUnix)
	}
}

func (m *Manager) setState(state, lastError string) {
	m.mu.Lock()
	m.status.State = state
	m.status.LastError = lastError
	m.mu.Unlock()
}

func (m *Manager) fail(err error) {
	m.setState("error", err.Error())
	m.emit("error", err.Error())
}

// failClosed is used only for a post-ApplyConfig security invariant failure.
// It closes the product listener and Embedded Core while leaving the outer
// management HTTP server alive.
func (m *Manager) failClosed(cfg *MConfig.Config, err error) {
	m.mu.Lock()
	listener, cancel, done := m.listener, m.cancel, m.done
	m.listener, m.cancel, m.done, m.config = nil, nil, nil, nil
	m.coreReady = false
	m.status.State, m.status.LastError = "error", err.Error()
	m.status.SocksAddress, m.status.ProjectionHash, m.status.ProjectionCount = "", "", 0
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	if listener != nil {
		_ = listener.Close()
	}
	m.store.Store(EmptySnapshot())
	m.shutdownCore(cfg)
	m.releaseOwnership()
	m.emit("error", err.Error())
}

func (m *Manager) emit(level, message string) {
	if m.options.OnEvent != nil {
		m.options.OnEvent(level, message)
	}
}

func (m *Manager) releaseOwnership() {
	processManager.Lock()
	if processManager.owner == m {
		processManager.owner = nil
	}
	processManager.Unlock()
}

func ValidateRuntimeInvariants(cfg *MConfig.Config, homeDir string) error {
	if err := ValidateControlledConfig(cfg, homeDir); err != nil {
		return err
	}
	runtimeGeneral := executor.GetGeneral()
	if runtimeGeneral.Port != 0 || runtimeGeneral.SocksPort != 0 || runtimeGeneral.MixedPort != 0 || runtimeGeneral.RedirPort != 0 || runtimeGeneral.TProxyPort != 0 {
		return errors.New("runtime assertion failed: Mihomo opened a forbidden top-level proxy port")
	}
	if runtimeGeneral.Tun.Enable {
		return errors.New("runtime assertion failed: Mihomo TUN is enabled")
	}
	if err := waitForController(cfg.Controller); err != nil {
		return err
	}
	return nil
}

func waitForController(controller *MConfig.Controller) error {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if controller.ExternalControllerUnix != "" {
			info, err := os.Lstat(controller.ExternalControllerUnix)
			if err == nil && info.Mode()&os.ModeSocket != 0 {
				if err := os.Chmod(controller.ExternalControllerUnix, 0o600); err != nil {
					return fmt.Errorf("secure Mihomo Unix Controller: %w", err)
				}
				// The socket path is created before Mihomo publishes its global
				// server pointer. Wait for a real response so a subsequent
				// ApplyConfig cannot overlap the still-starting Controller goroutine.
				connection, err := net.DialTimeout("unix", controller.ExternalControllerUnix, 50*time.Millisecond)
				if err == nil {
					_ = connection.SetDeadline(time.Now().Add(100 * time.Millisecond))
					_, writeErr := io.WriteString(connection, "GET /version HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n")
					responsePrefix := make([]byte, len("HTTP/1."))
					_, readErr := io.ReadFull(connection, responsePrefix)
					_ = connection.Close()
					if writeErr == nil && readErr == nil && string(responsePrefix) == "HTTP/1." {
						return nil
					}
				}
			}
		} else {
			endpoint := controller.ExternalController
			if endpoint == "" {
				endpoint = controller.ExternalControllerTLS
			}
			connection, err := net.DialTimeout("tcp", endpoint, 50*time.Millisecond)
			if err == nil {
				_ = connection.Close()
				return nil
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.New("runtime assertion failed: private Mihomo Controller did not start")
}

func linkedCoreVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, dependency := range info.Deps {
		if dependency.Path != "github.com/metacubex/mihomo" {
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
