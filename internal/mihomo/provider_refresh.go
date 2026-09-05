package mihomo

import (
	"context"
	"errors"
	"fmt"
	U "github.com/metacubex/mihomo/common/utils"
	MConfig "github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	P "github.com/metacubex/mihomo/constant/provider"
	"strings"
	"sync"
	"time"
)

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
