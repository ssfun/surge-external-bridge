package mihomo

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/outbound"
	"github.com/metacubex/mihomo/adapter/outboundgroup"
	"github.com/metacubex/mihomo/adapter/provider"
	MConfig "github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	P "github.com/metacubex/mihomo/constant/provider"
	"github.com/metacubex/mihomo/tunnel"
)

// buildProviderConfig changes only the provider topology. MConfig.Parse also
// writes process-global DNS/general settings, so it is unsafe on a live core.
// The remaining envelope is immutable after startup and is only read by the
// validation/publication path; none of its maps or pointer fields are modified.
func buildProviderConfig(current *MConfig.Config, homeDir string, definitions []ProviderDefinition, store *SnapshotStore) (_ *MConfig.Config, err error) {
	if current == nil || store == nil {
		return nil, errors.New("running configuration and projection store are required")
	}
	homeDir, err = filepath.Abs(homeDir)
	if err != nil || homeDir == "" {
		return nil, errors.New("valid Mihomo HomeDir is required")
	}
	if err := EnsurePrivateDir(homeDir); err != nil {
		return nil, err
	}
	candidate := *current
	candidate.Proxies = map[string]C.Proxy{
		"DIRECT":      adapter.NewProxy(outbound.NewDirect()),
		"REJECT":      adapter.NewProxy(outbound.NewReject()),
		"REJECT-DROP": adapter.NewProxy(outbound.NewRejectDrop()),
		"COMPATIBLE":  adapter.NewProxy(outbound.NewCompatible()),
		"PASS":        adapter.NewProxy(outbound.NewPass()),
		"PASS-RULE":   adapter.NewProxy(outbound.NewPassRule()),
	}
	candidate.Providers = make(map[string]P.ProxyProvider, len(definitions)+1)
	defer func() {
		if err != nil {
			closeProviders(candidate.Providers)
			closeProxies(candidate.Proxies)
		}
	}()
	// Match v1.19.30 config.parseProxies for the controlled, empty proxies/groups
	// configuration. Fresh builtins are essential: retiring the old compatible
	// provider closes its DIRECT/REJECT proxies.
	ps := []C.Proxy{candidate.Proxies["DIRECT"], candidate.Proxies["REJECT"]}
	hc := provider.NewHealthCheck(ps, "", 5000, 0, true, nil)
	compatible, err := provider.NewCompatibleProvider(provider.ReservedName, ps, hc)
	if err != nil {
		return nil, err
	}
	candidate.Providers[provider.ReservedName] = compatible
	global, err := outboundgroup.NewSelector(outboundgroup.GroupCommonOption{Name: "GLOBAL"}, outboundgroup.SelectorOption{}, candidate.Proxies["COMPATIBLE"], []P.ProxyProvider{compatible})
	if err != nil {
		return nil, fmt.Errorf("new GLOBAL proxy group: %w", err)
	}
	candidate.Proxies["GLOBAL"] = adapter.NewProxy(global)
	candidate.Proxies[RouterName] = WrappedRouter(store)
	for _, definition := range definitions {
		key, err := ProviderKey(definition.StableID)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", definition.Name, err)
		}
		if _, exists := candidate.Providers[key]; exists {
			return nil, fmt.Errorf("duplicate provider stable ID %q", definition.StableID)
		}
		mapping, err := providerMapping(homeDir, definition)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", definition.Name, err)
		}
		native, err := provider.ParseProxyProvider(key, mapping, tunnel.Tunnel)
		if err != nil {
			return nil, fmt.Errorf("parse proxy provider %s: %w", key, err)
		}
		// Register ownership before wrapping: host validation may reject the source.
		candidate.Providers[key] = native
		wrapped, err := newFilteredProxyProvider(native, newProviderHostSource(homeDir, definition))
		if err != nil {
			return nil, fmt.Errorf("prepare Provider %q hosts: %w", key, err)
		}
		candidate.Providers[key] = wrapped
	}
	if err := ValidateControlledConfig(&candidate, homeDir); err != nil {
		return nil, err
	}
	return &candidate, nil
}
