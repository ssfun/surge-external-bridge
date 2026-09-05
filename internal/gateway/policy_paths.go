package gateway

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	M "github.com/ssfun/surge-external-bridge/internal/mihomo"
	"regexp"
	"sort"
	"strings"
)

func (a *App) AddPolicyPath(path PolicyPath) (PolicyPath, error) {
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	a.mu.RLock()
	candidate := cloneConfig(a.config)
	a.mu.RUnlock()

	if strings.TrimSpace(path.Name) == "" {
		return PolicyPath{}, errors.New("Policy Path name is required")
	}
	if !path.IncludeAll && len(path.ProviderIDs) == 0 {
		return PolicyPath{}, errors.New("Policy Path must include at least one Provider")
	}
	if path.Token != "" {
		if err := validatePolicyPathTokenAvailability(candidate, path.Token, ""); err != nil {
			return PolicyPath{}, err
		}
	}
	allocated := false
	for attempts := 0; attempts < 8; attempts++ {
		value, err := randomPolicyPathID()
		if err != nil {
			return PolicyPath{}, err
		}
		path.StableID = value
		if _, exists := candidate.PolicyPath(path.StableID); !exists {
			allocated = true
			break
		}
	}
	if !allocated {
		return PolicyPath{}, errors.New("could not allocate Policy Path identity")
	}
	if path.Token == "" {
		for {
			token, err := randomToken()
			if err != nil {
				return PolicyPath{}, err
			}
			path.Token = token
			if policyPathTokenAvailable(candidate, path.Token, "") {
				break
			}
		}
	}
	normalizePolicyPathSelection(&path, candidate.Providers)
	candidate.PolicyPaths = append(candidate.PolicyPaths, path)
	if err := a.savePolicyPaths(candidate, "Policy Path 已添加"); err != nil {
		return PolicyPath{}, err
	}
	return path, nil
}

func (a *App) UpdatePolicyPath(id string, path PolicyPath) (PolicyPath, error) {
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	a.mu.RLock()
	candidate := cloneConfig(a.config)
	a.mu.RUnlock()
	if strings.TrimSpace(path.Name) == "" {
		return PolicyPath{}, errors.New("Policy Path name is required")
	}
	if !path.IncludeAll && len(path.ProviderIDs) == 0 {
		return PolicyPath{}, errors.New("Policy Path must include at least one Provider")
	}
	for index := range candidate.PolicyPaths {
		if candidate.PolicyPaths[index].StableID != id {
			continue
		}
		path.StableID = id
		if path.Token == "" {
			path.Token = candidate.PolicyPaths[index].Token
		}
		if err := validatePolicyPathTokenAvailability(candidate, path.Token, id); err != nil {
			return PolicyPath{}, err
		}
		normalizePolicyPathSelection(&path, candidate.Providers)
		candidate.PolicyPaths[index] = path
		if err := a.savePolicyPaths(candidate, "Policy Path 已更新"); err != nil {
			return PolicyPath{}, err
		}
		return path, nil
	}
	return PolicyPath{}, errors.New("Policy Path not found")
}

func (a *App) DeletePolicyPath(id string) error {
	if id == DefaultPolicyPathID {
		return errors.New("default Policy Path cannot be deleted")
	}
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	a.mu.RLock()
	candidate := cloneConfig(a.config)
	a.mu.RUnlock()
	for index := range candidate.PolicyPaths {
		if candidate.PolicyPaths[index].StableID != id {
			continue
		}
		candidate.PolicyPaths = append(candidate.PolicyPaths[:index], candidate.PolicyPaths[index+1:]...)
		return a.savePolicyPaths(candidate, "Policy Path 已删除")
	}
	return errors.New("Policy Path not found")
}

func (a *App) RegeneratePolicyPathToken(id string) (PolicyPath, error) {
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	a.mu.RLock()
	candidate := cloneConfig(a.config)
	a.mu.RUnlock()
	for index := range candidate.PolicyPaths {
		if candidate.PolicyPaths[index].StableID != id {
			continue
		}
		for {
			token, err := randomToken()
			if err != nil {
				return PolicyPath{}, err
			}
			if policyPathTokenAvailable(candidate, token, id) {
				candidate.PolicyPaths[index].Token = token
				break
			}
		}
		if err := a.savePolicyPaths(candidate, "Policy Path Token 已重新生成"); err != nil {
			return PolicyPath{}, err
		}
		return candidate.PolicyPaths[index], nil
	}
	return PolicyPath{}, errors.New("Policy Path not found")
}

func (a *App) savePolicyPaths(candidate Config, event string) error {
	a.publicationMu.Lock()
	defer a.publicationMu.Unlock()
	if err := ValidateConfig(candidate); err != nil {
		return err
	}
	if err := a.store.Save(candidate); err != nil {
		return err
	}
	a.mu.Lock()
	a.config = cloneConfig(candidate)
	a.mu.Unlock()
	a.addEvent("info", event)
	return nil
}

func (a *App) Proxies() (string, string, error) {
	return a.ProxiesForPath(DefaultPolicyPathID)
}

func (a *App) ProxiesForPath(id string) (string, string, error) {
	a.publicationMu.RLock()
	defer a.publicationMu.RUnlock()
	return a.proxiesForPathLocked(id)
}

func (a *App) ProxiesForToken(token string) (string, string, error) {
	a.publicationMu.RLock()
	defer a.publicationMu.RUnlock()
	config := a.Config()
	for _, path := range config.PolicyPaths {
		matches := token != "" && len(token) == len(path.Token) && subtle.ConstantTimeCompare([]byte(token), []byte(path.Token)) == 1
		if matches || (token == "" && path.StableID == DefaultPolicyPathID && path.Token == "") {
			return a.proxiesForPathLocked(path.StableID)
		}
	}
	return "", "", ErrInvalidPolicyToken
}

func (a *App) proxiesForPathLocked(id string) (string, string, error) {
	if status := a.Status(); status.State != "running" {
		return "", "", errors.New("Mihomo 身份数据面当前不可用")
	}
	path, ok := a.PolicyPath(id)
	if !ok {
		return "", "", errors.New("Policy Path not found")
	}
	snapshot := a.manager.Snapshot()
	entries, err := FilterPolicyPathEntries(path, snapshot.Entries())
	if err != nil {
		return "", "", err
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		lines = append(lines, formatSurgeLine(entry))
	}
	content := ""
	if len(lines) > 0 {
		content = strings.Join(lines, "\n") + "\n"
	}
	revision := sha256.Sum256([]byte(content))
	return content, hex.EncodeToString(revision[:]), nil
}

func FilterPolicyPathEntries(path PolicyPath, entries []M.Entry) ([]M.Entry, error) {
	selected := make(map[string]struct{}, len(path.ProviderIDs))
	if !path.IncludeAll {
		for _, providerID := range path.ProviderIDs {
			selected[providerID] = struct{}{}
		}
	}
	var includeName, excludeName *regexp.Regexp
	var err error
	if path.IncludeName != "" {
		includeName, err = regexp.Compile(path.IncludeName)
		if err != nil {
			return nil, fmt.Errorf("invalid Policy Path include expression: %w", err)
		}
	}
	if path.ExcludeName != "" {
		excludeName, err = regexp.Compile(path.ExcludeName)
		if err != nil {
			return nil, fmt.Errorf("invalid Policy Path exclude expression: %w", err)
		}
	}
	filtered := make([]M.Entry, 0, len(entries))
	for _, entry := range entries {
		if !path.IncludeAll {
			if _, included := selected[entry.ProviderID]; !included {
				continue
			}
		}
		if includeName != nil && !includeName.MatchString(entry.DisplayName) {
			continue
		}
		if excludeName != nil && excludeName.MatchString(entry.DisplayName) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered, nil
}

func formatSurgeLine(entry M.Entry) string {
	return fmt.Sprintf("%s = socks5, %s, %d, username=%s, password=%s, udp-relay=%t", entry.DisplayName, entry.SocksHost, entry.SocksPort, entry.Username, entry.Password, entry.SupportUDP)
}

func validPolicyToken(token string, required bool) bool {
	if token == "" {
		return !required
	}
	return len(token) >= 16
}

func validatePolicyPaths(config Config) error {
	if len(config.PolicyPaths) == 0 || len(config.PolicyPaths) > 128 {
		return errors.New("configuration must contain 1-128 Policy Paths")
	}
	providers := make(map[string]struct{}, len(config.Providers))
	for _, provider := range config.Providers {
		providers[provider.StableID] = struct{}{}
	}
	seenIDs := make(map[string]struct{}, len(config.PolicyPaths))
	seenNames := make(map[string]struct{}, len(config.PolicyPaths))
	seenTokens := make(map[string]struct{}, len(config.PolicyPaths))
	defaultCount := 0
	for _, path := range config.PolicyPaths {
		if path.StableID == DefaultPolicyPathID {
			defaultCount++
		} else if !policyPathIDPattern.MatchString(path.StableID) {
			return fmt.Errorf("Policy Path %q has an invalid identity", path.Name)
		}
		if _, duplicate := seenIDs[path.StableID]; duplicate {
			return errors.New("duplicate Policy Path identity")
		}
		seenIDs[path.StableID] = struct{}{}
		name := strings.TrimSpace(path.Name)
		if name == "" || len(name) > 80 || name != path.Name || strings.ContainsAny(name, "=\r\n") {
			return errors.New("Policy Path name must contain 1-80 characters without '=' or line breaks")
		}
		foldedName := strings.ToLower(name)
		if _, duplicate := seenNames[foldedName]; duplicate {
			return fmt.Errorf("duplicate Policy Path name %q", path.Name)
		}
		seenNames[foldedName] = struct{}{}
		if _, err := regexp.Compile(path.IncludeName); path.IncludeName != "" && err != nil {
			return fmt.Errorf("Policy Path %q has an invalid include expression: %w", path.Name, err)
		}
		if _, err := regexp.Compile(path.ExcludeName); path.ExcludeName != "" && err != nil {
			return fmt.Errorf("Policy Path %q has an invalid exclude expression: %w", path.Name, err)
		}
		requiredToken := config.Mode == ModeGateway || path.StableID != DefaultPolicyPathID
		if !validPolicyToken(path.Token, requiredToken) || path.Token == "unsafe" {
			return fmt.Errorf("Policy Path %q requires a Token of at least 16 characters", path.Name)
		}
		if path.Token != "" {
			if path.Token == config.ManagementToken {
				return fmt.Errorf("Policy Path %q must not share the Management Token", path.Name)
			}
			if _, duplicate := seenTokens[path.Token]; duplicate {
				return errors.New("Policy Paths must use distinct Tokens")
			}
			seenTokens[path.Token] = struct{}{}
		}
		if path.IncludeAll {
			if len(path.ProviderIDs) != 0 {
				return fmt.Errorf("Policy Path %q cannot combine include_all with Provider IDs", path.Name)
			}
			continue
		}
		seenProviders := make(map[string]struct{}, len(path.ProviderIDs))
		for _, providerID := range path.ProviderIDs {
			if _, ok := providers[providerID]; !ok {
				return fmt.Errorf("Policy Path %q references unknown Provider %q", path.Name, providerID)
			}
			if _, duplicate := seenProviders[providerID]; duplicate {
				return fmt.Errorf("Policy Path %q contains duplicate Provider %q", path.Name, providerID)
			}
			seenProviders[providerID] = struct{}{}
		}
	}
	if defaultCount != 1 {
		return errors.New("configuration must contain exactly one default Policy Path")
	}
	return nil
}

func normalizePolicyPathSelection(path *PolicyPath, providers []Provider) {
	if path.IncludeAll {
		path.ProviderIDs = nil
		return
	}
	wanted := make(map[string]struct{}, len(path.ProviderIDs))
	for _, id := range path.ProviderIDs {
		wanted[id] = struct{}{}
	}
	ordered := make([]string, 0, len(wanted))
	for _, provider := range providers {
		if _, ok := wanted[provider.StableID]; ok {
			ordered = append(ordered, provider.StableID)
			delete(wanted, provider.StableID)
		}
	}
	unknown := make([]string, 0, len(wanted))
	for id := range wanted {
		unknown = append(unknown, id)
	}
	sort.Strings(unknown)
	path.ProviderIDs = append(ordered, unknown...)
}

func replacePolicyPathProviderID(paths []PolicyPath, previous, next string) {
	for pathIndex := range paths {
		for providerIndex := range paths[pathIndex].ProviderIDs {
			if paths[pathIndex].ProviderIDs[providerIndex] == previous {
				paths[pathIndex].ProviderIDs[providerIndex] = next
			}
		}
	}
}

func removePolicyPathProviderID(paths []PolicyPath, id string) {
	for pathIndex := range paths {
		ids := paths[pathIndex].ProviderIDs[:0]
		for _, providerID := range paths[pathIndex].ProviderIDs {
			if providerID != id {
				ids = append(ids, providerID)
			}
		}
		paths[pathIndex].ProviderIDs = ids
	}
}

func policyPathTokenAvailable(config Config, token, exceptID string) bool {
	if token == "" || token == config.ManagementToken {
		return false
	}
	for _, path := range config.PolicyPaths {
		if path.StableID != exceptID && path.Token == token {
			return false
		}
	}
	return true
}

func validatePolicyPathTokenAvailability(config Config, token, exceptID string) error {
	for _, path := range config.PolicyPaths {
		if path.StableID != exceptID && path.Token == token {
			return errors.New("Policy Path Token is already used by another Policy Path")
		}
	}
	return nil
}
