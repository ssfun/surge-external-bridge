package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ssfun/vless2surge/internal/core"
	"github.com/ssfun/vless2surge/internal/domain"
	"github.com/ssfun/vless2surge/internal/revision"
	"github.com/ssfun/vless2surge/internal/store"
	"github.com/ssfun/vless2surge/internal/subscription"
)

var Version = "0.1.0-dev"
var BuildVersionMarker = "vless2surge-version:0.1.0-dev"

var headerNamePattern = regexp.MustCompile("^[!#$%&'*+\\-.^_`|~0-9A-Za-z]+$")

type App struct {
	mu       sync.RWMutex
	applyMu  sync.Mutex
	store    *store.Store
	config   domain.Config
	state    domain.RuntimeState
	builder  *revision.Builder
	compiler *core.Compiler
	fetcher  *subscription.Fetcher
	engine   *core.Manager
}

func (a *App) DataDir() string { return a.store.Dir() }

func New(dataDir string) (*App, error) {
	if BuildVersionMarker != "vless2surge-version:"+Version {
		return nil, errors.New("binary build metadata is inconsistent; rebuild vless2surge with the official Makefile")
	}
	persistence := store.New(dataDir)
	config, state, err := persistence.Load()
	if err != nil {
		return nil, err
	}
	if err := ValidateConfig(config); err != nil {
		return nil, fmt.Errorf("invalid saved configuration: %w", err)
	}
	hydrateRevisionEndpoint(config, state.Draft)
	hydrateRevisionEndpoint(config, state.Applied)
	if state.Applied != nil && state.AppliedConfig == nil {
		appliedConfig := clone(config)
		state.AppliedConfig = &appliedConfig
	}
	if state.Applied != nil && state.AppliedSnapshots == nil {
		state.AppliedSnapshots = clone(state.Snapshots)
	}
	if !state.LastExitClean {
		state.ConsecutiveCrash++
	} else {
		state.ConsecutiveCrash = 0
	}
	if state.ConsecutiveCrash >= 3 {
		state.SafeMode = true
	}
	state.LastExitClean = false
	application := &App{
		store:    persistence,
		config:   config,
		state:    state,
		builder:  revision.NewBuilder(),
		compiler: core.NewCompiler(),
		fetcher:  subscription.NewFetcher(),
	}
	application.engine = core.NewManager(func(level, message string) {
		application.AddEvent(level, message)
	})
	application.addEventLocked("info", fmt.Sprintf("vless2surge %s started with Embedded sing-box %s", Version, core.CoreVersion))
	if err := application.store.SaveConfig(config); err != nil {
		return nil, err
	}
	if err := application.store.SaveState(&application.state); err != nil {
		return nil, err
	}
	if state.AutoStart && state.Applied != nil && !state.SafeMode {
		if err := application.startApplied(); err != nil {
			application.mu.Lock()
			application.state.LastError = err.Error()
			application.addEventLocked("error", "automatic Engine start failed: "+err.Error())
			_ = application.store.SaveState(&application.state)
			application.mu.Unlock()
		}
	}
	return application, nil
}

func (a *App) Close() error {
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	_ = a.engine.Stop()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.LastExitClean = true
	a.state.ConsecutiveCrash = 0
	a.addEventLocked("info", "vless2surge stopped cleanly")
	return a.store.SaveState(&a.state)
}

func (a *App) Config() domain.Config {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return clone(a.config)
}

func (a *App) State() domain.RuntimeState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return clone(a.state)
}

func (a *App) EngineStatus() domain.EngineStatus {
	status := a.engine.Status()
	a.mu.RLock()
	status.SafeMode = a.state.SafeMode
	a.mu.RUnlock()
	return status
}

func (a *App) Diagnostics() domain.Diagnostics {
	a.mu.RLock()
	config, state := clone(a.config), clone(a.state)
	a.mu.RUnlock()
	engine := a.engine.Status()
	result := domain.Diagnostics{Time: time.Now().UTC()}
	add := func(name, status, detail string) {
		result.Checks = append(result.Checks, domain.DiagnosticCheck{Name: name, Status: status, Detail: detail})
	}

	enabled, cachedFailures := 0, 0
	for _, sub := range config.Subscriptions {
		if !sub.Enabled {
			continue
		}
		enabled++
		if state.Snapshots[sub.ID].LastError != "" {
			cachedFailures++
		}
	}
	if enabled == 0 {
		add("subscriptions", "warn", "没有启用的订阅或手动来源")
	} else if cachedFailures > 0 {
		add("subscriptions", "warn", fmt.Sprintf("%d 个来源使用最近成功快照，%d 个来源已启用", cachedFailures, enabled))
	} else {
		add("subscriptions", "ok", fmt.Sprintf("%d 个来源已启用，最近状态正常", enabled))
	}

	endpointDetail := fmt.Sprintf("SOCKS bind=%s advertise=%s port=%d", config.SocksBind, config.SocksAdvertise, config.SocksPort)
	if config.Mode == "linux" && isLoopbackHost(config.SocksAdvertise) {
		add("endpoint", "warn", endpointDetail+"；Linux 私网模式的 advertise 是回环地址，远端 Surge 无法访问")
	} else if config.Mode == "local" && !isLoopbackHost(config.SocksAdvertise) {
		add("endpoint", "warn", endpointDetail+"；本地模式正在对外发布 SOCKS 地址")
	} else if config.SocksBind != config.SocksAdvertise {
		add("endpoint", "ok", endpointDetail+"；bind 与 advertise 已按部署路径分离")
	} else {
		add("endpoint", "ok", endpointDetail)
	}

	if state.Draft == nil || len(state.Draft.Nodes) == 0 {
		add("draft", "error", "没有可编译的 VLESS 草稿")
	} else {
		draft := clonePtr(state.Draft)
		if _, err := a.compiler.Compile(config, draft); err != nil {
			add("draft", "error", "Embedded Core 配置校验失败: "+err.Error())
		} else if draft.Risky {
			add("draft", "warn", draft.RiskReason)
		} else {
			add("draft", "ok", fmt.Sprintf("草稿 %s 已通过 sing-box %s 配置校验，共 %d 个节点", draft.ID, core.CoreVersion, len(draft.Nodes)))
		}
	}

	if state.Applied == nil || len(state.Applied.Nodes) == 0 {
		add("applied", "error", "尚未应用可供 Surge 使用的 revision")
	} else {
		result.Revision = state.Applied.ID
		users := map[string]bool{}
		valid := true
		for _, node := range state.Applied.Nodes {
			if node.AuthUser == "" || node.Password == "" || node.OutboundTag == "" || users[node.AuthUser] {
				valid = false
				break
			}
			users[node.AuthUser] = true
		}
		if valid {
			add("applied", "ok", fmt.Sprintf("revision %s 包含 %d 个唯一 SOCKS 身份", state.Applied.ID, len(state.Applied.Nodes)))
		} else {
			add("applied", "error", "已应用 revision 的身份映射不完整或重复")
		}
	}

	switch engine.State {
	case "running":
		if state.Applied != nil && engine.Revision == state.Applied.ID {
			add("engine", "ok", fmt.Sprintf("Embedded Engine 正在 %s 运行 applied revision", engine.Inbound))
		} else {
			add("engine", "error", "Engine revision 与 applied revision 不一致")
		}
	case "stopped":
		add("engine", "warn", "Embedded Engine 已停止；配置台仍可用")
	default:
		add("engine", "error", fmt.Sprintf("Engine 状态为 %s: %s", engine.State, engine.LastError))
	}
	add("connectivity", "warn", "配置与身份检查已完成；节点 TCP/UDP 实际连通性需要通过 Surge 测速或业务流量确认")
	return result
}

func (a *App) UpdateConfig(config domain.Config) error {
	if err := ValidateConfig(config); err != nil {
		return err
	}
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	a.mu.Lock()
	previousConfig, previousState := clone(a.config), clone(a.state)
	a.config = config
	if err := a.rebuildDraftLocked("config"); err != nil {
		a.config, a.state = previousConfig, previousState
		a.mu.Unlock()
		return err
	}
	if err := a.store.SaveConfig(a.config); err != nil {
		a.config, a.state = previousConfig, previousState
		a.mu.Unlock()
		return err
	}
	err := a.store.SaveState(&a.state)
	if err != nil {
		a.config, a.state = previousConfig, previousState
		_ = a.store.SaveConfig(a.config)
		_ = a.store.SaveState(&a.state)
	}
	a.mu.Unlock()
	return err
}

func (a *App) AddSubscription(sub domain.Subscription) (domain.Subscription, error) {
	if sub.SourceType == "" {
		sub.SourceType = "url"
	}
	if err := validateSubscription(sub); err != nil {
		return domain.Subscription{}, err
	}
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, existing := range a.config.Subscriptions {
		if strings.EqualFold(existing.Name, sub.Name) {
			return domain.Subscription{}, fmt.Errorf("subscription name %q already exists", sub.Name)
		}
	}
	if sub.ID == "" {
		id, err := randomID("sub_", 9)
		if err != nil {
			return domain.Subscription{}, err
		}
		sub.ID = id
	}
	previousConfig, previousState := clone(a.config), clone(a.state)
	a.config.Subscriptions = append(a.config.Subscriptions, sub)
	if err := a.rebuildDraftLocked("subscription-add"); err != nil {
		a.config, a.state = previousConfig, previousState
		return domain.Subscription{}, err
	}
	if err := a.store.SaveConfig(a.config); err != nil {
		a.config, a.state = previousConfig, previousState
		return domain.Subscription{}, err
	}
	a.addEventLocked("info", fmt.Sprintf("subscription added: %s", sub.Name))
	if err := a.store.SaveState(&a.state); err != nil {
		a.config, a.state = previousConfig, previousState
		_ = a.store.SaveConfig(a.config)
		_ = a.store.SaveState(&a.state)
		return domain.Subscription{}, err
	}
	return sub, nil
}

func (a *App) AddManualSubscription(name string, content []byte) (domain.Subscription, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return domain.Subscription{}, errors.New("name is required")
	}
	parsed, err := subscription.Parse(content)
	if err != nil {
		return domain.Subscription{}, err
	}
	if len(parsed.Nodes) == 0 {
		return domain.Subscription{}, errors.New("pasted content contains no usable VLESS nodes")
	}
	id, err := randomID("sub_", 9)
	if err != nil {
		return domain.Subscription{}, err
	}
	sub := domain.Subscription{ID: id, Name: name, SourceType: "manual", Enabled: true}
	now := time.Now().UTC()
	for index := range parsed.Nodes {
		parsed.Nodes[index].SourceID = id
		parsed.Nodes[index].SourceName = name
	}
	for index := range parsed.Dropped {
		parsed.Dropped[index].SourceID = id
		parsed.Dropped[index].SourceName = name
	}

	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, existing := range a.config.Subscriptions {
		if strings.EqualFold(existing.Name, name) {
			return domain.Subscription{}, fmt.Errorf("subscription name %q already exists", name)
		}
	}
	previousConfig, previousState := clone(a.config), clone(a.state)
	a.config.Subscriptions = append(a.config.Subscriptions, sub)
	a.state.Snapshots[id] = domain.Snapshot{
		SubscriptionID: id,
		FetchedAt:      now,
		LastAttemptAt:  now,
		Nodes:          parsed.Nodes,
		Dropped:        parsed.Dropped,
		RawCount:       parsed.RawCount,
	}
	if err := a.rebuildDraftLocked("manual-import"); err != nil {
		a.config, a.state = previousConfig, previousState
		return domain.Subscription{}, err
	}
	a.addEventLocked("info", fmt.Sprintf("manual source imported: %s, usable=%d", name, len(parsed.Nodes)))
	if err := a.store.SaveConfig(a.config); err != nil {
		a.config, a.state = previousConfig, previousState
		return domain.Subscription{}, err
	}
	if err := a.store.SaveState(&a.state); err != nil {
		a.config, a.state = previousConfig, previousState
		_ = a.store.SaveConfig(a.config)
		_ = a.store.SaveState(&a.state)
		return domain.Subscription{}, err
	}
	return sub, nil
}

func (a *App) UpdateSubscription(id string, update domain.Subscription) (domain.Subscription, error) {
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	for index := range a.config.Subscriptions {
		if a.config.Subscriptions[index].ID != id {
			continue
		}
		existing := a.config.Subscriptions[index]
		if update.SourceType == "" {
			update.SourceType = existing.SourceType
		}
		if subscriptionSourceType(update) == "url" && strings.TrimSpace(update.URL) == "" {
			update.URL = existing.URL
		}
		if update.Headers == nil {
			update.Headers = existing.Headers
		}
		if err := validateSubscription(update); err != nil {
			return domain.Subscription{}, err
		}
		for otherIndex, other := range a.config.Subscriptions {
			if otherIndex != index && strings.EqualFold(other.Name, update.Name) {
				return domain.Subscription{}, fmt.Errorf("subscription name %q already exists", update.Name)
			}
		}
		previousConfig, previousState := clone(a.config), clone(a.state)
		update.ID = id
		a.config.Subscriptions[index] = update
		if subscriptionSourceType(existing) != subscriptionSourceType(update) || existing.URL != update.URL || !reflect.DeepEqual(existing.Headers, update.Headers) {
			if snapshot, found := a.state.Snapshots[id]; found && len(snapshot.Nodes) > 0 {
				snapshot.LastAttemptAt = time.Time{}
				snapshot.LastError = "订阅来源已变更，当前继续使用变更前的成功快照，等待刷新"
				a.state.Snapshots[id] = snapshot
			}
		}
		if err := a.rebuildDraftLocked("subscription"); err != nil {
			a.config, a.state = previousConfig, previousState
			return domain.Subscription{}, err
		}
		if err := a.store.SaveConfig(a.config); err != nil {
			a.config, a.state = previousConfig, previousState
			return domain.Subscription{}, err
		}
		if err := a.store.SaveState(&a.state); err != nil {
			a.config, a.state = previousConfig, previousState
			_ = a.store.SaveConfig(a.config)
			_ = a.store.SaveState(&a.state)
			return domain.Subscription{}, err
		}
		return update, nil
	}
	return domain.Subscription{}, errors.New("subscription not found")
}

func (a *App) DeleteSubscription(id string) error {
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	index := -1
	for i, sub := range a.config.Subscriptions {
		if sub.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return errors.New("subscription not found")
	}
	name := a.config.Subscriptions[index].Name
	previousConfig, previousState := clone(a.config), clone(a.state)
	a.config.Subscriptions = append(a.config.Subscriptions[:index], a.config.Subscriptions[index+1:]...)
	delete(a.state.Snapshots, id)
	if err := a.rebuildDraftLocked("subscription-delete"); err != nil {
		a.config, a.state = previousConfig, previousState
		return err
	}
	a.addEventLocked("warn", fmt.Sprintf("subscription deleted: %s", name))
	if err := a.store.SaveConfig(a.config); err != nil {
		a.config, a.state = previousConfig, previousState
		return err
	}
	if err := a.store.SaveState(&a.state); err != nil {
		a.config, a.state = previousConfig, previousState
		_ = a.store.SaveConfig(a.config)
		_ = a.store.SaveState(&a.state)
		return err
	}
	return nil
}

func (a *App) ImportProviders(content []byte) ([]domain.Subscription, error) {
	providers, err := subscription.ParseClashProviders(content)
	if err != nil {
		return nil, err
	}
	added := make([]domain.Subscription, 0, len(providers))
	for _, provider := range providers {
		created, addErr := a.AddSubscription(provider)
		if addErr == nil {
			added = append(added, created)
		}
	}
	if len(added) == 0 {
		return nil, errors.New("no new providers were imported")
	}
	return added, nil
}

func (a *App) Refresh(ctx context.Context, id string) error {
	a.mu.RLock()
	config := clone(a.config)
	var sub *domain.Subscription
	for index := range config.Subscriptions {
		if config.Subscriptions[index].ID == id {
			copySub := config.Subscriptions[index]
			sub = &copySub
			break
		}
	}
	a.mu.RUnlock()
	if sub == nil {
		return errors.New("subscription not found")
	}
	if subscriptionSourceType(*sub) != "url" {
		return errors.New("manual sources cannot be refreshed; paste a replacement source instead")
	}
	now := time.Now().UTC()
	content, err := a.fetcher.Fetch(ctx, *sub, config.UserAgent)
	if err != nil {
		a.recordRefreshFailure(id, *sub, config.UserAgent, now, err)
		return err
	}
	parsed, err := subscription.Parse(content)
	if err != nil {
		a.recordRefreshFailure(id, *sub, config.UserAgent, now, err)
		return err
	}
	if len(parsed.Nodes) == 0 {
		err = errors.New("subscription contains no usable VLESS nodes; previous snapshot was kept")
		a.recordRefreshFailure(id, *sub, config.UserAgent, now, err)
		return err
	}
	for index := range parsed.Nodes {
		parsed.Nodes[index].SourceID = sub.ID
		parsed.Nodes[index].SourceName = sub.Name
	}
	for index := range parsed.Dropped {
		parsed.Dropped[index].SourceID = sub.ID
		parsed.Dropped[index].SourceName = sub.Name
	}
	a.applyMu.Lock()
	engineRunning := a.engine.Status().State == "running"
	a.mu.Lock()
	if !a.subscriptionUnchangedLocked(id, *sub, config.UserAgent) {
		a.mu.Unlock()
		a.applyMu.Unlock()
		return errors.New("subscription changed while refresh was in progress; fetched result was discarded")
	}
	previousState := clone(a.state)
	a.state.Snapshots[id] = domain.Snapshot{
		SubscriptionID: id,
		FetchedAt:      now,
		LastAttemptAt:  now,
		Nodes:          parsed.Nodes,
		Dropped:        parsed.Dropped,
		RawCount:       parsed.RawCount,
	}
	if err := a.rebuildDraftLocked("refresh"); err != nil {
		a.state = previousState
		a.mu.Unlock()
		a.applyMu.Unlock()
		return err
	}
	a.addEventLocked("info", fmt.Sprintf("subscription refreshed: %s, usable=%d", sub.Name, len(parsed.Nodes)))
	saveErr := a.store.SaveState(&a.state)
	if saveErr != nil {
		a.state = previousState
	}
	autoApply := a.config.AutoApply && a.state.Draft != nil && !a.state.Draft.Risky && engineRunning
	a.mu.Unlock()
	a.applyMu.Unlock()
	if saveErr != nil {
		return saveErr
	}
	if autoApply {
		return a.Apply(false)
	}
	return nil
}

func (a *App) RefreshAll(ctx context.Context) map[string]string {
	a.mu.RLock()
	ids := make([]string, 0, len(a.config.Subscriptions))
	for _, sub := range a.config.Subscriptions {
		if sub.Enabled && subscriptionSourceType(sub) == "url" {
			ids = append(ids, sub.ID)
		}
	}
	a.mu.RUnlock()
	result := map[string]string{}
	for _, id := range ids {
		if err := a.Refresh(ctx, id); err != nil {
			result[id] = err.Error()
		} else {
			result[id] = "ok"
		}
	}
	return result
}

func (a *App) RebuildDraft(generatedBy string) (*domain.Revision, error) {
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.rebuildDraftLocked(generatedBy); err != nil {
		return nil, err
	}
	if err := a.store.SaveState(&a.state); err != nil {
		return nil, err
	}
	return clonePtr(a.state.Draft), nil
}

func (a *App) Apply(force bool) error {
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	a.mu.RLock()
	config := clone(a.config)
	draft := clonePtr(a.state.Draft)
	safeMode := a.state.SafeMode
	previousState := clone(a.state)
	a.mu.RUnlock()
	engineWasRunning := a.engine.Status().State == "running"
	if draft == nil {
		return errors.New("there is no draft to apply")
	}
	if len(draft.Nodes) == 0 {
		return errors.New("cannot apply an empty draft")
	}
	if draft.Risky && !force {
		return fmt.Errorf("risky draft requires explicit force: %s", draft.RiskReason)
	}
	if safeMode && !force {
		return errors.New("safe mode requires explicit confirmation")
	}
	compiled, err := a.compiler.Compile(config, draft)
	if err != nil {
		a.AddEvent("error", "draft validation failed: "+err.Error())
		return err
	}
	inbound := revisionInbound(config, draft)
	if err := a.engine.Apply(draft, compiled, inbound); err != nil {
		a.mu.Lock()
		a.state.LastError = err.Error()
		_ = a.store.SaveState(&a.state)
		a.mu.Unlock()
		return err
	}
	now := time.Now().UTC()
	draft.AppliedAt = &now
	a.mu.Lock()
	a.state.Applied = draft
	a.state.Draft = clonePtr(draft)
	appliedConfig := clone(config)
	a.state.AppliedConfig = &appliedConfig
	a.state.AppliedSnapshots = clone(a.state.Snapshots)
	a.state.SafeMode = false
	a.state.ConsecutiveCrash = 0
	a.state.LastError = ""
	a.state.AutoStart = true
	a.addEventLocked("info", fmt.Sprintf("revision applied: %s", draft.ID))
	err = a.store.SaveState(&a.state)
	if err != nil {
		a.state = previousState
	}
	a.mu.Unlock()
	if err != nil {
		rollbackErr := a.restoreEngine(config, previousState.Applied, engineWasRunning)
		if rollbackErr != nil {
			return fmt.Errorf("persist applied revision: %v; restore previous Engine: %w", err, rollbackErr)
		}
		return fmt.Errorf("persist applied revision: %w", err)
	}
	return err
}

func (a *App) restoreEngine(config domain.Config, previous *domain.Revision, wasRunning bool) error {
	if !wasRunning {
		return a.engine.Stop()
	}
	if previous == nil || len(previous.Nodes) == 0 {
		return a.engine.Stop()
	}
	compiled, err := a.compiler.Compile(config, previous)
	if err != nil {
		return err
	}
	return a.engine.Apply(previous, compiled, revisionInbound(config, previous))
}

func (a *App) DiscardDraft() error {
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.Applied == nil || a.state.AppliedConfig == nil {
		return errors.New("there is no applied revision to restore")
	}
	previousConfig, previousState := clone(a.config), clone(a.state)
	restored := clone(*a.state.AppliedConfig)
	// These settings control the HTTP service and refresh behavior immediately;
	// they are not part of the Embedded Engine revision transaction.
	restored.HTTPBind = a.config.HTTPBind
	restored.PolicyBaseURL = a.config.PolicyBaseURL
	restored.RefreshSeconds = a.config.RefreshSeconds
	restored.UserAgent = a.config.UserAgent
	restored.AutoApply = a.config.AutoApply
	restored.DropThresholdPercent = a.config.DropThresholdPercent
	restored.ManagementToken = a.config.ManagementToken
	restored.PolicyToken = a.config.PolicyToken
	restored.Mode = a.config.Mode
	a.config = restored
	if a.state.AppliedSnapshots != nil {
		a.state.Snapshots = clone(a.state.AppliedSnapshots)
	}
	draft := clonePtr(a.state.Applied)
	draft.AppliedAt = nil
	draft.Risky = false
	draft.RiskReason = ""
	draft.GeneratedBy = "discard"
	a.state.Draft = draft
	a.addEventLocked("warn", fmt.Sprintf("draft discarded; restored applied revision inputs: %s", a.state.Applied.ID))
	if err := a.store.SaveConfig(a.config); err != nil {
		a.config, a.state = previousConfig, previousState
		return err
	}
	if err := a.store.SaveState(&a.state); err != nil {
		a.config, a.state = previousConfig, previousState
		_ = a.store.SaveConfig(a.config)
		_ = a.store.SaveState(&a.state)
		return err
	}
	return nil
}

func (a *App) StartEngine() error {
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	if a.engine.Status().State == "running" {
		return nil
	}
	a.mu.RLock()
	applied := clonePtr(a.state.Applied)
	config := clone(a.config)
	safeMode := a.state.SafeMode
	a.mu.RUnlock()
	if safeMode {
		return errors.New("Engine is in safe mode; apply a confirmed draft to recover")
	}
	if applied == nil || len(applied.Nodes) == 0 {
		return errors.New("there is no applied revision")
	}
	compiled, err := a.compiler.Compile(config, applied)
	if err != nil {
		return err
	}
	inbound := revisionInbound(config, applied)
	if err := a.engine.Start(applied, compiled, inbound); err != nil {
		return err
	}
	a.mu.Lock()
	a.state.AutoStart = true
	a.addEventLocked("info", "Engine autostart enabled")
	err = a.store.SaveState(&a.state)
	a.mu.Unlock()
	return err
}

func (a *App) StopEngine() error {
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	if err := a.engine.Stop(); err != nil {
		return err
	}
	a.mu.Lock()
	a.state.AutoStart = false
	a.addEventLocked("warn", "Engine stopped by user")
	err := a.store.SaveState(&a.state)
	a.mu.Unlock()
	return err
}

func (a *App) RedactedDraftConfig() ([]byte, error) {
	a.mu.RLock()
	config := clone(a.config)
	draft := clonePtr(a.state.Draft)
	a.mu.RUnlock()
	compiled, err := a.compiler.Compile(config, draft)
	if err != nil {
		return nil, err
	}
	return core.Redacted(compiled), nil
}

func (a *App) RotateCredentials(nodeID string) (int, error) {
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	previous := clone(a.state)
	active := map[string]bool{}
	if a.state.Draft != nil {
		for _, node := range a.state.Draft.Nodes {
			active[node.NodeID] = true
		}
	}
	rotated := 0
	for key, identity := range a.state.Registry {
		if nodeID != "" && identity.NodeID != nodeID {
			continue
		}
		if nodeID == "" && !active[identity.NodeID] {
			continue
		}
		user, err := randomID("v2s_", 8)
		if err != nil {
			a.state = previous
			return 0, err
		}
		password, err := randomID("", 24)
		if err != nil {
			a.state = previous
			return 0, err
		}
		identity.AuthUser = user
		identity.Password = password
		a.state.Registry[key] = identity
		rotated++
	}
	if rotated == 0 {
		return 0, errors.New("no matching node identity")
	}
	if err := a.rebuildDraftLocked("credential-rotation"); err != nil {
		a.state = previous
		return 0, err
	}
	a.addEventLocked("warn", fmt.Sprintf("gateway credentials rotated: identities=%d", rotated))
	if err := a.store.SaveState(&a.state); err != nil {
		a.state = previous
		return 0, err
	}
	return rotated, nil
}

func (a *App) Proxies() (string, string, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.state.Applied == nil || len(a.state.Applied.Nodes) == 0 {
		return "", "", errors.New("no applied revision")
	}
	_, port, advertise := revisionEndpoint(a.config, a.state.Applied)
	lines := make([]string, 0, len(a.state.Applied.Nodes))
	usedNames := map[string]bool{}
	for _, node := range a.state.Applied.Nodes {
		baseName := sanitizeSurgeName(node.DisplayName)
		name := baseName
		for suffix := 2; usedNames[name]; suffix++ {
			name = fmt.Sprintf("%s [%d]", baseName, suffix)
		}
		usedNames[name] = true
		lines = append(lines, fmt.Sprintf("%s = socks5, %s, %d, %s, %s, udp-relay=true", name, advertise, port, node.AuthUser, node.Password))
	}
	return strings.Join(lines, "\n") + "\n", a.state.Applied.ID, nil
}

func (a *App) AddEvent(level, message string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.addEventLocked(level, message)
	_ = a.store.SaveState(&a.state)
}

func (a *App) RunScheduler(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.refreshDue(ctx)
		}
	}
}

func (a *App) refreshDue(ctx context.Context) {
	a.mu.RLock()
	config := clone(a.config)
	state := clone(a.state)
	a.mu.RUnlock()
	now := time.Now()
	for _, sub := range config.Subscriptions {
		if !sub.Enabled || subscriptionSourceType(sub) != "url" {
			continue
		}
		interval := sub.RefreshSeconds
		if interval <= 0 {
			interval = config.RefreshSeconds
		}
		snapshot := state.Snapshots[sub.ID]
		if snapshot.LastAttemptAt.IsZero() || now.Sub(snapshot.LastAttemptAt) >= time.Duration(interval)*time.Second {
			_ = a.Refresh(ctx, sub.ID)
		}
	}
}

func (a *App) startApplied() error {
	a.mu.RLock()
	applied := clonePtr(a.state.Applied)
	config := clone(a.config)
	a.mu.RUnlock()
	if applied == nil || len(applied.Nodes) == 0 {
		return nil
	}
	compiled, err := a.compiler.Compile(config, applied)
	if err != nil {
		return err
	}
	inbound := revisionInbound(config, applied)
	return a.engine.Start(applied, compiled, inbound)
}

func revisionEndpoint(config domain.Config, revision *domain.Revision) (string, uint16, string) {
	bind, port, advertise := revision.SocksBind, revision.SocksPort, revision.SocksAdvertise
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

func hydrateRevisionEndpoint(config domain.Config, revision *domain.Revision) {
	if revision == nil {
		return
	}
	if revision.SocksBind == "" {
		revision.SocksBind = config.SocksBind
	}
	if revision.SocksPort == 0 {
		revision.SocksPort = config.SocksPort
	}
	if revision.SocksAdvertise == "" {
		revision.SocksAdvertise = config.SocksAdvertise
	}
}

func revisionInbound(config domain.Config, revision *domain.Revision) string {
	bind, port, _ := revisionEndpoint(config, revision)
	return net.JoinHostPort(bind, fmt.Sprint(port))
}

func (a *App) rebuildDraftLocked(generatedBy string) error {
	draft, err := a.builder.Build(a.config, &a.state, generatedBy)
	if err != nil {
		return err
	}
	for _, sub := range a.config.Subscriptions {
		snapshot, found := a.state.Snapshots[sub.ID]
		if !found {
			continue
		}
		for _, dropped := range snapshot.Dropped {
			dropped.SourceID = sub.ID
			dropped.SourceName = sub.Name
			draft.Dropped = append(draft.Dropped, dropped)
		}
	}
	if len(draft.Nodes) > 0 {
		if _, err := a.compiler.Compile(a.config, draft); err != nil {
			return err
		}
	}
	a.state.Draft = draft
	a.addEventLocked("info", fmt.Sprintf("draft rebuilt: %s, nodes=%d", draft.ID, len(draft.Nodes)))
	return nil
}

func (a *App) recordRefreshFailure(id string, source domain.Subscription, userAgent string, at time.Time, cause error) {
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.subscriptionUnchangedLocked(id, source, userAgent) {
		return
	}
	snapshot := a.state.Snapshots[id]
	snapshot.SubscriptionID = id
	snapshot.LastAttemptAt = at
	snapshot.LastError = cause.Error()
	a.state.Snapshots[id] = snapshot
	a.addEventLocked("error", "subscription refresh failed: "+cause.Error())
	_ = a.store.SaveState(&a.state)
}

func (a *App) subscriptionUnchangedLocked(id string, source domain.Subscription, userAgent string) bool {
	if a.config.UserAgent != userAgent {
		return false
	}
	for _, current := range a.config.Subscriptions {
		if current.ID == id && current.URL == source.URL && reflect.DeepEqual(current.Headers, source.Headers) && subscriptionSourceType(current) == "url" {
			return true
		}
	}
	return false
}

func (a *App) addEventLocked(level, message string) {
	a.state.Events = append([]domain.Event{{Time: time.Now().UTC(), Level: level, Message: message}}, a.state.Events...)
	if len(a.state.Events) > 200 {
		a.state.Events = a.state.Events[:200]
	}
}

func ValidateConfig(config domain.Config) error {
	if config.Mode != "local" && config.Mode != "linux" {
		return errors.New("mode must be local or linux")
	}
	if config.SocksBind == "" || config.SocksPort == 0 || config.SocksAdvertise == "" {
		return errors.New("SOCKS bind, advertise, and port are required")
	}
	if err := validateEndpointHost("socks_bind", config.SocksBind); err != nil {
		return err
	}
	if err := validateEndpointHost("socks_advertise", config.SocksAdvertise); err != nil {
		return err
	}
	if isWildcardHost(config.SocksAdvertise) {
		return errors.New("socks_advertise must be a client-reachable address, not a wildcard listener")
	}
	httpHost, _, err := net.SplitHostPort(config.HTTPBind)
	if err != nil {
		return fmt.Errorf("invalid HTTP bind: %w", err)
	}
	policyURL, err := url.Parse(config.PolicyBaseURL)
	if err != nil || (policyURL.Scheme != "http" && policyURL.Scheme != "https") || policyURL.Host == "" {
		return errors.New("policy_base_url must be an absolute HTTP or HTTPS URL")
	}
	if isWildcardHost(policyURL.Hostname()) {
		return errors.New("policy_base_url must use a client-reachable host, not a wildcard listener")
	}
	if config.RefreshSeconds < 60 {
		return errors.New("refresh interval must be at least 60 seconds")
	}
	if config.DropThresholdPercent < 0 || config.DropThresholdPercent > 100 {
		return errors.New("drop threshold must be between 0 and 100")
	}
	if config.ExcludeName != "" {
		if _, err := regexp.Compile(config.ExcludeName); err != nil {
			return fmt.Errorf("invalid exclude regex: %w", err)
		}
	}
	if !isLoopbackHost(httpHost) && (config.ManagementToken == "" || config.PolicyToken == "") {
		return errors.New("binding HTTP outside loopback requires management_token and policy_token")
	}
	if config.ManagementToken != "" && len(config.ManagementToken) < 16 {
		return errors.New("management_token must contain at least 16 characters")
	}
	if config.PolicyToken != "" && len(config.PolicyToken) < 16 {
		return errors.New("policy_token must contain at least 16 characters")
	}
	if config.ManagementToken != "" && config.ManagementToken == config.PolicyToken {
		return errors.New("management_token and policy_token must be distinct")
	}
	seen := map[string]bool{}
	for _, sub := range config.Subscriptions {
		if err := validateSubscription(sub); err != nil {
			return fmt.Errorf("subscription %q: %w", sub.Name, err)
		}
		if sub.ID == "" || seen[sub.ID] {
			return errors.New("subscription IDs must be unique and non-empty")
		}
		seen[sub.ID] = true
	}
	return nil
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isWildcardHost(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	ip := net.ParseIP(host)
	return ip != nil && ip.IsUnspecified()
}

func validateEndpointHost(field, host string) error {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" || len(host) > 253 || strings.ContainsAny(host, "\r\n\t ,=/") {
		return fmt.Errorf("%s must be an IP address or hostname", field)
	}
	if net.ParseIP(host) != nil {
		return nil
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("%s must be an IP address or hostname", field)
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '-' {
				return fmt.Errorf("%s must be an IP address or hostname", field)
			}
		}
	}
	return nil
}

func validateSubscription(sub domain.Subscription) error {
	if strings.TrimSpace(sub.Name) == "" {
		return errors.New("name is required")
	}
	switch subscriptionSourceType(sub) {
	case "url":
		u, err := url.Parse(sub.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("URL must be HTTP or HTTPS")
		}
	case "manual":
		if sub.URL != "" {
			return errors.New("manual source cannot contain a URL")
		}
	default:
		return errors.New("source_type must be url or manual")
	}
	if sub.Filter != "" {
		if _, err := regexp.Compile(sub.Filter); err != nil {
			return fmt.Errorf("invalid filter regex: %w", err)
		}
	}
	if sub.RefreshSeconds != 0 && sub.RefreshSeconds < 60 {
		return errors.New("refresh interval must be zero or at least 60 seconds")
	}
	for key, value := range sub.Headers {
		if !headerNamePattern.MatchString(key) || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("invalid request header %q", key)
		}
		if strings.EqualFold(key, "Host") || strings.EqualFold(key, "Content-Length") {
			return fmt.Errorf("request header %q is managed by the HTTP client", key)
		}
	}
	return nil
}

func subscriptionSourceType(sub domain.Subscription) string {
	if sub.SourceType == "" {
		return "url"
	}
	return strings.ToLower(sub.SourceType)
}

func sanitizeSurgeName(value string) string {
	value = strings.Map(func(character rune) rune {
		if character == '=' {
			return '-'
		}
		if character < 0x20 || character == 0x7f {
			return ' '
		}
		return character
	}, value)
	value = strings.TrimSpace(value)
	if value == "" {
		value = "Unnamed VLESS node"
	}
	if strings.HasPrefix(value, "#") || strings.HasPrefix(value, ";") {
		value = "Node " + value
	}
	return value
}

func randomID(prefix string, size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(buffer), nil
}

func clone[T any](value T) T {
	// Domain values are JSON data; round-tripping avoids sharing slices/maps with handlers.
	var result T
	data, _ := jsonMarshal(value)
	_ = jsonUnmarshal(data, &result)
	return result
}

func clonePtr[T any](value *T) *T {
	if value == nil {
		return nil
	}
	result := clone(*value)
	return &result
}

func jsonMarshal(value any) ([]byte, error)      { return json.Marshal(value) }
func jsonUnmarshal(data []byte, value any) error { return json.Unmarshal(data, value) }

func SortedSubscriptions(config domain.Config) []domain.Subscription {
	result := clone(config.Subscriptions)
	sort.SliceStable(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}
