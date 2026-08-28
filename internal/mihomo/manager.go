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

	U "github.com/metacubex/mihomo/common/utils"
	"github.com/metacubex/mihomo/component/resolver"
	MConfig "github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	P "github.com/metacubex/mihomo/constant/provider"
	"github.com/metacubex/mihomo/hub/executor"
	"github.com/metacubex/mihomo/hub/route"
	MLog "github.com/metacubex/mihomo/log"
	"github.com/metacubex/mihomo/tunnel"
	"github.com/metacubex/mihomo/tunnel/statistic"
)

var processManager struct {
	sync.Mutex
	owner       *Manager
	initialized bool
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
	nextHealth     map[string]time.Time
	lastHealth     map[string]time.Time
	providerErrors map[string]string
	hostCounts     map[string]int
	hostResolver   *providerHostResolver
	status         ManagerStatus
	healthMu       sync.Mutex
	healthTasks    map[string]*providerHealthTask
}

type providerHealthTask struct {
	cancel context.CancelFunc
	done   chan struct{}
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
		nextHealth:     make(map[string]time.Time),
		lastHealth:     make(map[string]time.Time),
		providerErrors: make(map[string]string),
		hostCounts:     make(map[string]int),
		hostResolver:   sharedProviderHostResolver,
		status:         ManagerStatus{State: "stopped", CoreVersion: CoreVersion},
		healthTasks:    make(map[string]*providerHealthTask),
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
	nextRefresh, lastError = m.nextPull[key], m.providerErrors[key]
	cfg := m.config
	m.mu.RUnlock()
	if lastError == "" && cfg != nil {
		report := providerFilterReport(cfg.Providers[key])
		if report.SourceCount > 0 && report.AvailableCount == 0 && report.FilteredCount() > 0 {
			lastError = fmt.Sprintf("全部 %d 个节点均因使用 dialer-proxy 被过滤", report.FilteredCount())
		}
	}
	return nextRefresh, lastError
}

func (m *Manager) ProviderFilterState(stableID string) ProviderFilterReport {
	key, err := ProviderKey(stableID)
	if err != nil {
		return ProviderFilterReport{}
	}
	m.mu.RLock()
	cfg := m.config
	m.mu.RUnlock()
	if cfg == nil {
		return ProviderFilterReport{}
	}
	return providerFilterReport(cfg.Providers[key])
}

func (m *Manager) ProviderHostsCount(stableID string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.hostCounts[stableID]
}

func (m *Manager) Start() error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	return m.startLocked()
}

// startLocked starts the manager while the caller holds applyMu.
func (m *Manager) startLocked() error {
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
	processInitialized := processManager.initialized
	processManager.owner = m
	processManager.Unlock()

	m.setState("starting", "")
	var bootstrap *MConfig.Config
	var err error
	if !coreReady && !processInitialized {
		// Apply the process-global Mihomo settings before parsing user Provider
		// adapters. Stateful inline adapters can start worker goroutines during
		// parsing, and executor.ApplyConfig writes Mihomo's unsynchronised global
		// log level. Bootstrapping an empty topology keeps those events ordered.
		bootstrap, err = BuildControlledConfig(m.options.HomeDir, m.options.ControllerSocket, m.options.ControllerSecret, nil, m.store)
		if err != nil {
			m.fail(err)
			m.releaseOwnership()
			return err
		}
		applyInitialConfig(bootstrap)
		processManager.Lock()
		processManager.initialized = true
		processManager.Unlock()
	}
	cfg, err := BuildControlledConfig(m.options.HomeDir, m.options.ControllerSocket, m.options.ControllerSecret, m.options.Providers, m.store)
	if err != nil {
		m.fail(err)
		if !coreReady {
			if bootstrap != nil {
				m.shutdownCore(bootstrap)
			}
			m.releaseOwnership()
		}
		return err
	}
	if coreReady {
		m.stopHealthChecks()
	}
	if err := initializeProviders(cfg.Providers); err != nil {
		m.fail(err)
		if !coreReady {
			if bootstrap != nil {
				m.shutdownCore(bootstrap)
			}
			m.releaseOwnership()
		}
		return err
	}
	hostResolver, hostCounts, err := buildProviderHostResolver(cfg.Providers, m.options.Providers)
	if err != nil {
		closeProviders(cfg.Providers)
		m.fail(err)
		if !coreReady {
			if bootstrap != nil {
				m.shutdownCore(bootstrap)
			}
			m.releaseOwnership()
		}
		return err
	}
	providerTransaction, err := prepareProviderStates(cfg.Providers)
	if err != nil {
		err = providerTransaction.reject(err)
		closeProviders(cfg.Providers)
		m.fail(err)
		if !coreReady {
			if bootstrap != nil {
				m.shutdownCore(bootstrap)
			}
			m.releaseOwnership()
		}
		return err
	}
	replaceProviderConfig(cfg, m.hostResolver, hostResolver)
	if !coreReady && processInitialized {
		recreateController(cfg)
	}
	if err := SecurePrivateTree(m.options.HomeDir); err != nil {
		err = providerTransaction.reject(err)
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
		err = providerTransaction.reject(err)
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
		err = providerTransaction.reject(err)
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
		err = providerTransaction.reject(err)
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
	if err := providerTransaction.commit(); err != nil {
		err = providerTransaction.reject(err)
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
	providerTransaction.accept()
	m.mu.Lock()
	m.coreReady = true
	m.config = cfg
	m.hostCounts = hostCounts
	m.mu.Unlock()
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
	m.emitProviderFilterWarnings(cfg, m.options.Providers)
	m.emit("info", fmt.Sprintf("网关已启动，加载 %d 个节点", len(m.store.Load().Entries())))
	return nil
}

// applyInitialConfig preserves Mihomo's public hub.ApplyConfig semantics while
// ordering its two public operations safely. Mihomo v1.19.30 starts the
// Controller goroutines before executor.ApplyConfig writes the package-global
// log level, so the Controller's startup log can race that write. Applying the
// data-plane configuration first and starting the immutable private Controller
// afterwards removes that upstream race without a fork or a second config
// application.
func applyInitialConfig(cfg *MConfig.Config) {
	executor.ApplyConfig(cfg, true)
	// The embedded gateway owns one process-lifetime proxy-server resolver.
	// Mihomo clears this package-level hook when DNS is disabled; reinstall the
	// stable wrapper before any user Provider adapters can dial, then refresh
	// only its immutable trie snapshot for the rest of the process lifetime.
	resolver.ProxyServerHostResolver = sharedProviderHostResolver
	recreateController(cfg)
}

func recreateController(cfg *MConfig.Config) {
	if cfg.Controller.ExternalUI != "" {
		route.SetUIPath(cfg.Controller.ExternalUI)
	}
	route.ReCreateServer(&route.Config{
		Addr:           cfg.Controller.ExternalController,
		TLSAddr:        cfg.Controller.ExternalControllerTLS,
		UnixAddr:       cfg.Controller.ExternalControllerUnix,
		PipeAddr:       cfg.Controller.ExternalControllerPipe,
		RoutingMark:    cfg.Controller.ExternalControllerRoutingMark,
		Secret:         cfg.Controller.Secret,
		Certificate:    cfg.TLS.Certificate,
		PrivateKey:     cfg.TLS.PrivateKey,
		ClientAuthType: cfg.TLS.ClientAuthType,
		ClientAuthCert: cfg.TLS.ClientAuthCert,
		EchKey:         cfg.TLS.EchKey,
		DohServer:      cfg.Controller.ExternalDohServer,
		IsDebug:        cfg.General.LogLevel == MLog.DEBUG,
		Cors: route.Cors{
			AllowOrigins:        cfg.Controller.Cors.AllowOrigins,
			AllowPrivateNetwork: cfg.Controller.Cors.AllowPrivateNetwork,
		},
	})
}

func (m *Manager) ApplyProviders(definitions []ProviderDefinition) error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	m.mu.RLock()
	running := m.listener != nil
	current := m.config
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
	hostResolver, hostCounts, err := buildProviderHostResolver(candidate.Providers, definitions)
	if err != nil {
		closeProviders(candidate.Providers)
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
	providerTransaction, err := prepareProviderStates(candidate.Providers)
	if err != nil {
		err = providerTransaction.reject(err)
		closeProviders(candidate.Providers)
		return err
	}
	if err := ValidateRuntimeInvariants(candidate, m.options.HomeDir); err != nil {
		err = providerTransaction.reject(err)
		closeProviders(candidate.Providers)
		m.failClosedLocked(current, err)
		return err
	}
	if err := providerTransaction.commit(); err != nil {
		err = providerTransaction.reject(err)
		closeProviders(candidate.Providers)
		return err
	}
	// The private Controller and every General setting are immutable for the
	// lifetime of this process. Apply only the Provider/proxy topology that can
	// actually change. Besides avoiding a needless Controller restart, this
	// avoids Mihomo v1.19.30's unsynchronised global log-level write while old
	// outbound finalizers may still be logging.
	m.stopHealthChecks()
	replaceProviderConfig(candidate, m.hostResolver, hostResolver)
	providerTransaction.accept()
	m.store.Store(candidateSnapshot)
	m.mu.Lock()
	m.config = candidate
	m.options.Providers = append([]ProviderDefinition(nil), definitions...)
	m.hostCounts = hostCounts
	m.captureVersionsLocked(candidate, definitions)
	m.scheduleProvidersLocked(definitions, time.Now())
	clear(m.providerErrors)
	m.status.State = "running"
	m.status.LastError = ""
	m.status.ProjectionHash = m.store.Load().Revision()
	m.status.ProjectionCount = len(m.store.Load().Entries())
	m.mu.Unlock()
	m.emitProviderFilterWarnings(candidate, definitions)
	m.emit("info", fmt.Sprintf("applied Mihomo Provider configuration with %d projected nodes", len(m.store.Load().Entries())))
	return nil
}

func (m *Manager) StartWithProviders(definitions []ProviderDefinition) error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	m.mu.Lock()
	if m.listener != nil {
		m.mu.Unlock()
		return errors.New("Mihomo manager is already running")
	}
	m.options.Providers = append([]ProviderDefinition(nil), definitions...)
	m.mu.Unlock()
	return m.startLocked()
}

func (m *Manager) ConfigureWhenStopped(bind, advertise string, port uint16, prefixProvider bool, masterKey []byte, definitions []ProviderDefinition) error {
	if strings.TrimSpace(bind) == "" || strings.TrimSpace(advertise) == "" || port == 0 || len(masterKey) < MasterKeySize {
		return errors.New("invalid projection settings")
	}
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listener != nil {
		return errors.New("Mihomo manager is running")
	}
	m.options.SocksBind, m.options.SocksAdvertise, m.options.SocksPort = bind, advertise, port
	m.options.PrefixProvider = prefixProvider
	m.options.MasterKey = append([]byte(nil), masterKey...)
	m.options.Providers = append([]ProviderDefinition(nil), definitions...)
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
		m.failClosedLocked(cfg, err)
		return err
	}
	hostResolver, hostCounts, err := buildProviderHostResolver(cfg.Providers, definitions)
	if err != nil {
		err = rejectProviderStates(cfg.Providers, map[string]struct{}{key: {}}, err)
		wrapped := fmt.Errorf("refresh Mihomo Provider: %w", err)
		m.mu.Lock()
		m.providerErrors[key] = wrapped.Error()
		m.scheduleProviderLocked(stableID, definitions, time.Now())
		m.mu.Unlock()
		return wrapped
	}
	candidate, err := m.buildProjection(cfg, definitions)
	if err != nil {
		err = rejectProviderStates(cfg.Providers, map[string]struct{}{key: {}}, err)
		wrapped := fmt.Errorf("refresh Mihomo Provider: %w", err)
		m.mu.Lock()
		m.providerErrors[key] = wrapped.Error()
		m.scheduleProviderLocked(stableID, definitions, time.Now())
		m.mu.Unlock()
		return wrapped
	}
	providerTransaction, err := prepareProviderStates(cfg.Providers)
	if err != nil {
		err = providerTransaction.reject(err)
		wrapped := fmt.Errorf("refresh Mihomo Provider: %w", err)
		m.mu.Lock()
		m.providerErrors[key] = wrapped.Error()
		m.scheduleProviderLocked(stableID, definitions, time.Now())
		m.mu.Unlock()
		return wrapped
	}
	if err := providerTransaction.commit(); err != nil {
		err = providerTransaction.reject(err)
		wrapped := fmt.Errorf("refresh Mihomo Provider: %w", err)
		m.mu.Lock()
		m.providerErrors[key] = wrapped.Error()
		m.scheduleProviderLocked(stableID, definitions, time.Now())
		m.mu.Unlock()
		return wrapped
	}
	replaceProviderRuntime(candidate, m.hostResolver, hostResolver, m.store)
	providerTransaction.accept()
	m.mu.Lock()
	delete(m.providerErrors, key)
	m.hostCounts = hostCounts
	m.captureVersionsLocked(cfg, definitions)
	m.scheduleProviderLocked(stableID, definitions, time.Now())
	m.status.ProjectionHash = m.store.Load().Revision()
	m.status.ProjectionCount = len(m.store.Load().Entries())
	m.status.LastError = ""
	m.mu.Unlock()
	m.emitProviderFilterWarnings(cfg, definitions)
	return nil
}

func (m *Manager) HealthCheckProvider(stableID string) error {
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
	definition, ok := findProviderDefinition(stableID, definitions)
	if !ok {
		return errors.New("Provider definition not found")
	}
	if err := m.startProviderHealthCheckLocked(key, definition, provider); err != nil {
		return err
	}
	m.mu.Lock()
	m.scheduleHealthProviderLocked(stableID, definitions, time.Now())
	m.mu.Unlock()
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
	m.stopHealthChecks()
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
	clear(m.hostCounts)
	m.mu.Unlock()
	m.releaseOwnership()
	m.emit("info", "网关已停止")
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
	views, err := candidateProviderViews(cfg, definitions)
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
	definitions := append([]ProviderDefinition(nil), m.options.Providers...)
	m.mu.RUnlock()
	if cfg == nil {
		return
	}
	now := time.Now()
	changed := false
	changedKeys := make(map[string]struct{})
	for _, definition := range definitions {
		key, _ := ProviderKey(definition.StableID)
		m.mu.RLock()
		refreshDue := definition.Type == "http" && definition.RefreshSeconds > 0 && !now.Before(m.nextPull[key])
		healthDue := definition.HealthCheck && definition.HealthCheckSeconds > 0 && !now.Before(m.nextHealth[key])
		m.mu.RUnlock()
		provider := cfg.Providers[key]
		if refreshDue {
			if provider != nil {
				if err := provider.Update(); err != nil {
					m.mu.Lock()
					m.providerErrors[key] = err.Error()
					m.mu.Unlock()
					m.emit("warn", fmt.Sprintf("scheduled Provider %s refresh failed: %v", definition.Name, err))
				} else {
					if err := SecurePrivateTree(m.options.HomeDir); err != nil {
						// pollProviders runs inside the watcher goroutine whose done
						// channel failClosedLocked waits on. Queue fail-closed behind
						// applyMu so the watcher can return and release the lock first.
						m.failClosedAsync(cfg, err)
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
		}
		m.mu.RLock()
		previousVersion := m.versions[key]
		m.mu.RUnlock()
		currentVersion := previousVersion
		if provider != nil {
			currentVersion = provider.Version()
		}
		hostError := providerHostError(provider)
		if hostError != "" {
			m.mu.Lock()
			m.providerErrors[key] = hostError
			m.mu.Unlock()
		}
		if healthDue && provider != nil && hostError == "" {
			m.mu.RLock()
			lastHealth := m.lastHealth[key]
			m.mu.RUnlock()
			shouldCheck := !definition.HealthCheckLazy || lastHealth.IsZero() || m.store.ProviderTouchedSince(definition.StableID, lastHealth)
			if shouldCheck {
				if err := m.startProviderHealthCheckLocked(key, definition, provider); err != nil {
					m.emit("warn", fmt.Sprintf("scheduled Provider %s health check failed to start: %v", definition.Name, err))
				}
			}
			m.mu.Lock()
			m.scheduleHealthProviderLocked(definition.StableID, definitions, now)
			m.mu.Unlock()
		}
		if provider != nil && previousVersion != currentVersion {
			changed = true
			changedKeys[key] = struct{}{}
		}
	}
	if !changed {
		return
	}
	if err := SecurePrivateTree(m.options.HomeDir); err != nil {
		m.failClosedAsync(cfg, err)
		return
	}
	hostResolver, hostCounts, err := buildProviderHostResolver(cfg.Providers, definitions)
	if err != nil {
		err = rejectProviderStates(cfg.Providers, changedKeys, err)
		m.mu.Lock()
		for key := range changedKeys {
			m.providerErrors[key] = err.Error()
		}
		m.mu.Unlock()
		m.emit("warn", "Provider hosts update rejected: "+err.Error())
		return
	}
	candidate, err := m.buildProjection(cfg, definitions)
	if err != nil {
		err = rejectProviderStates(cfg.Providers, changedKeys, err)
		m.mu.Lock()
		for key := range changedKeys {
			m.providerErrors[key] = err.Error()
		}
		m.mu.Unlock()
		m.emit("warn", "Provider projection update rejected: "+err.Error())
		return
	}
	providerTransaction, err := prepareProviderStates(cfg.Providers)
	if err != nil {
		err = providerTransaction.reject(err)
		m.mu.Lock()
		for key := range changedKeys {
			m.providerErrors[key] = err.Error()
		}
		m.mu.Unlock()
		m.emit("warn", "Provider cache update rejected: "+err.Error())
		return
	}
	if err := providerTransaction.commit(); err != nil {
		err = providerTransaction.reject(err)
		m.mu.Lock()
		for key := range changedKeys {
			m.providerErrors[key] = err.Error()
		}
		m.mu.Unlock()
		m.emit("warn", "Provider cache update rejected: "+err.Error())
		return
	}
	replaceProviderRuntime(candidate, m.hostResolver, hostResolver, m.store)
	providerTransaction.accept()
	m.mu.Lock()
	for key := range changedKeys {
		delete(m.providerErrors, key)
	}
	m.hostCounts = hostCounts
	m.captureVersionsLocked(cfg, definitions)
	m.status.ProjectionHash = m.store.Load().Revision()
	m.status.ProjectionCount = len(m.store.Load().Entries())
	m.status.LastError = ""
	m.mu.Unlock()
	m.emitProviderFilterWarnings(cfg, definitions)
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
	clear(m.nextHealth)
	clear(m.lastHealth)
	for _, definition := range definitions {
		m.scheduleProviderLocked(definition.StableID, definitions, now)
		m.scheduleInitialHealthProviderLocked(definition.StableID, definitions, now)
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

func (m *Manager) scheduleInitialHealthProviderLocked(stableID string, definitions []ProviderDefinition, now time.Time) {
	for _, definition := range definitions {
		if definition.StableID != stableID {
			continue
		}
		key, _ := ProviderKey(stableID)
		if !definition.HealthCheck || definition.HealthCheckSeconds <= 0 {
			delete(m.nextHealth, key)
			return
		}
		// Mihomo performs one health check immediately when its automatic
		// worker starts. Preserve that startup behavior in the Manager watcher.
		m.nextHealth[key] = now
		return
	}
}

func (m *Manager) scheduleHealthProviderLocked(stableID string, definitions []ProviderDefinition, now time.Time) {
	for _, definition := range definitions {
		if definition.StableID != stableID {
			continue
		}
		key, _ := ProviderKey(stableID)
		if !definition.HealthCheck || definition.HealthCheckSeconds <= 0 {
			delete(m.nextHealth, key)
			return
		}
		m.nextHealth[key] = now.Add(time.Duration(definition.HealthCheckSeconds) * time.Second)
		m.lastHealth[key] = now
		return
	}
}

func findProviderDefinition(stableID string, definitions []ProviderDefinition) (ProviderDefinition, bool) {
	for _, definition := range definitions {
		if definition.StableID == stableID {
			return definition, true
		}
	}
	return ProviderDefinition{}, false
}

func (m *Manager) startProviderHealthCheckLocked(key string, definition ProviderDefinition, provider P.ProxyProvider) error {
	if !definition.HealthCheck || strings.TrimSpace(definition.HealthCheckURL) == "" {
		return nil
	}
	expectedStatus, err := U.NewUnsignedRanges[uint16](definition.ExpectedStatus)
	if err != nil {
		return fmt.Errorf("parse Provider health-check expected status: %w", err)
	}
	proxies := append([]C.Proxy(nil), provider.Proxies()...)
	if len(proxies) == 0 {
		return nil
	}
	timeout := time.Duration(definition.HealthCheckTimeout) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	m.healthMu.Lock()
	if m.healthTasks[key] != nil {
		m.healthMu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	task := &providerHealthTask{cancel: cancel, done: make(chan struct{})}
	m.healthTasks[key] = task
	m.healthMu.Unlock()
	go m.runProviderHealthCheck(ctx, key, task, proxies, definition.HealthCheckURL, expectedStatus, timeout)
	return nil
}

func (m *Manager) runProviderHealthCheck(parent context.Context, key string, task *providerHealthTask, proxies []C.Proxy, url string, expectedStatus U.IntRanges[uint16], timeout time.Duration) {
	defer func() {
		m.healthMu.Lock()
		if m.healthTasks[key] == task {
			delete(m.healthTasks, key)
		}
		m.healthMu.Unlock()
		close(task.done)
	}()
	jobs := make(chan C.Proxy)
	workers := 10
	if len(proxies) < workers {
		workers = len(proxies)
	}
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			for {
				select {
				case <-parent.Done():
					return
				case proxy, ok := <-jobs:
					if !ok {
						return
					}
					ctx, cancel := context.WithTimeout(parent, timeout)
					_, _ = proxy.URLTest(ctx, url, expectedStatus)
					cancel()
				}
			}
		}()
	}
sendLoop:
	for _, proxy := range proxies {
		select {
		case <-parent.Done():
			break sendLoop
		case jobs <- proxy:
		}
	}
	close(jobs)
	wait.Wait()
}

func (m *Manager) stopHealthChecks() {
	m.healthMu.Lock()
	tasks := make([]*providerHealthTask, 0, len(m.healthTasks))
	for _, task := range m.healthTasks {
		task.cancel()
		tasks = append(tasks, task)
	}
	m.healthMu.Unlock()
	for _, task := range tasks {
		<-task.done
	}
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

func replaceProviderConfig(candidate *MConfig.Config, runtimeResolver, candidateResolver *providerHostResolver) {
	oldProviders := tunnel.Providers()
	tunnel.OnSuspend()
	runtimeResolver.replace(candidateResolver)
	tunnel.UpdateProxies(candidate.Proxies, candidate.Providers)
	tunnel.OnRunning()
	closeProviders(oldProviders)
}

func replaceProviderRuntime(snapshot *Snapshot, runtimeResolver, candidateResolver *providerHostResolver, store *SnapshotStore) {
	tunnel.OnSuspend()
	runtimeResolver.replace(candidateResolver)
	store.Store(snapshot)
	tunnel.OnRunning()
}

func closeProviders(providers map[string]P.ProxyProvider) {
	for _, provider := range providers {
		for _, proxy := range providerSourceProxies(provider) {
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
	// to tear down. Drain tracked connections before emptying the shared resolver
	// snapshot so no old proxy can reconnect against a later Manager's mappings.
	tunnel.OnSuspend()
	statistic.DefaultManager.Range(func(connection statistic.Tracker) bool {
		_ = connection.Close()
		return true
	})
	m.hostResolver.replace(nil)
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

// failClosedLocked is used only for a post-ApplyConfig security invariant
// failure. The caller must hold applyMu.
// It closes the product listener and Embedded Core while leaving the outer
// management HTTP server alive.
func (m *Manager) failClosedLocked(cfg *MConfig.Config, err error) {
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
	m.stopHealthChecks()
	if listener != nil {
		_ = listener.Close()
	}
	m.store.Store(EmptySnapshot())
	m.shutdownCore(cfg)
	m.releaseOwnership()
	m.emit("error", err.Error())
}

// failClosedAsync queues watcher-triggered cleanup behind every other runtime
// mutation. A stale failure must not tear down a newer configuration.
func (m *Manager) failClosedAsync(cfg *MConfig.Config, err error) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.applyMu.Lock()
		defer m.applyMu.Unlock()
		m.mu.RLock()
		current := m.config == cfg && m.listener != nil
		m.mu.RUnlock()
		if current {
			m.failClosedLocked(cfg, err)
		}
	}()
	return done
}

func (m *Manager) emit(level, message string) {
	if m.options.OnEvent != nil {
		m.options.OnEvent(level, message)
	}
}

func (m *Manager) emitProviderFilterWarnings(cfg *MConfig.Config, definitions []ProviderDefinition) {
	if cfg == nil {
		return
	}
	for _, definition := range definitions {
		key, err := ProviderKey(definition.StableID)
		if err != nil {
			continue
		}
		report := providerFilterReport(cfg.Providers[key])
		if report.FilteredCount() == 0 {
			continue
		}
		m.emit("warn", fmt.Sprintf("Provider %s 已过滤 %d 个使用 dialer-proxy 的节点", definition.Name, report.FilteredCount()))
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
