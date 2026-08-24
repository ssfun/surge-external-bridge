package revision

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ssfun/vless2surge/internal/domain"
)

type Builder struct{}

func NewBuilder() *Builder { return &Builder{} }

func (b *Builder) Build(config domain.Config, state *domain.RuntimeState, generatedBy string) (*domain.Revision, error) {
	exclude, err := compileOptionalRegex(config.ExcludeName)
	if err != nil {
		return nil, fmt.Errorf("invalid global exclude regex: %w", err)
	}
	include := make(map[string]bool, len(config.IncludeTypes))
	for _, typeName := range config.IncludeTypes {
		include[strings.ToLower(typeName)] = true
	}
	if len(include) == 0 {
		return nil, fmt.Errorf("protocol allowlist is empty")
	}

	now := time.Now().UTC()
	kept := make([]domain.RuntimeNode, 0)
	dropped := make([]domain.DroppedNode, 0)
	seen := map[string]bool{}
	activeNodeIDs := map[string]string{}
	activeAuthUsers := map[string]string{}
	enabledSubscriptions := 0
	for _, sub := range config.Subscriptions {
		if sub.Enabled {
			enabledSubscriptions++
		}
	}
	for _, sub := range config.Subscriptions {
		snapshot, found := state.Snapshots[sub.ID]
		if !found {
			continue
		}
		filter, filterErr := compileOptionalRegex(sub.Filter)
		if filterErr != nil {
			return nil, fmt.Errorf("invalid filter for subscription %q: %w", sub.Name, filterErr)
		}
		for _, sourceNode := range snapshot.Nodes {
			node := sourceNode
			node.SourceID = sub.ID
			node.SourceName = sub.Name
			drop := func(reason string) {
				dropped = append(dropped, domain.DroppedNode{Name: node.Name, SourceID: sub.ID, SourceName: sub.Name, Type: node.Type, Reason: reason})
			}
			if !sub.Enabled {
				drop("订阅已停用")
				continue
			}
			if !include[strings.ToLower(node.Type)] {
				drop("Surge 原生支持或未进入协议白名单")
				continue
			}
			if exclude != nil && exclude.MatchString(node.Name) {
				drop("名称排除")
				continue
			}
			if filter != nil && !filter.MatchString(node.Name) {
				drop("订阅过滤")
				continue
			}
			fingerprint, err := Fingerprint(node)
			if err != nil {
				return nil, err
			}
			registryKey := sub.ID + ":" + fingerprint
			if seen[registryKey] {
				drop("重复节点")
				continue
			}
			seen[registryKey] = true
			identity, found := state.Registry[registryKey]
			if !found {
				identity, err = newIdentity(registryKey, fingerprint, now)
				if err != nil {
					return nil, err
				}
			} else {
				identity.LastSeenAt = now
			}
			if identity.NodeID == "" || identity.AuthUser == "" || identity.Password == "" || identity.Fingerprint != fingerprint {
				drop("节点身份冲突")
				continue
			}
			if owner, exists := activeNodeIDs[identity.NodeID]; exists && owner != registryKey {
				drop("节点身份冲突")
				continue
			}
			if owner, exists := activeAuthUsers[identity.AuthUser]; exists && owner != registryKey {
				drop("节点身份冲突")
				continue
			}
			activeNodeIDs[identity.NodeID] = registryKey
			activeAuthUsers[identity.AuthUser] = registryKey
			state.Registry[registryKey] = identity
			display := node.Name
			if config.PrefixSubscription && enabledSubscriptions > 1 && strings.TrimSpace(sub.Name) != "" {
				display = sub.Name + " · " + node.Name
			}
			kept = append(kept, domain.RuntimeNode{
				Node:        node,
				NodeID:      identity.NodeID,
				DisplayName: display,
				AuthUser:    identity.AuthUser,
				Password:    identity.Password,
				OutboundTag: "vless-" + identity.NodeID,
			})
		}
	}

	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].SourceName == kept[j].SourceName {
			if kept[i].DisplayName == kept[j].DisplayName {
				return kept[i].NodeID < kept[j].NodeID
			}
			return kept[i].DisplayName < kept[j].DisplayName
		}
		return kept[i].SourceName < kept[j].SourceName
	})
	ensureUniqueDisplayNames(kept)

	rev := &domain.Revision{
		CreatedAt:      now,
		Nodes:          kept,
		Dropped:        dropped,
		SocksBind:      config.SocksBind,
		SocksPort:      config.SocksPort,
		SocksAdvertise: config.SocksAdvertise,
		GeneratedBy:    generatedBy,
	}
	revisionBytes, err := json.Marshal(struct {
		Nodes     []domain.RuntimeNode `json:"nodes"`
		Bind      string               `json:"bind"`
		Port      uint16               `json:"port"`
		Advertise string               `json:"advertise"`
	}{kept, config.SocksBind, config.SocksPort, config.SocksAdvertise})
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(revisionBytes)
	rev.ID = hex.EncodeToString(hash[:8])
	rev.ConfigHash = hex.EncodeToString(hash[:])

	if state.Applied != nil && len(state.Applied.Nodes) > 0 && config.DropThresholdPercent > 0 {
		previousCount := len(state.Applied.Nodes)
		droppedCount := previousCount - len(kept)
		if droppedCount > 0 && droppedCount*100 > previousCount*config.DropThresholdPercent {
			rev.Risky = true
			rev.RiskReason = fmt.Sprintf("节点数量从 %d 降至 %d，超过 %d%% 自动应用阈值", previousCount, len(kept), config.DropThresholdPercent)
		}
	}
	if len(kept) == 0 {
		rev.Risky = true
		rev.RiskReason = "没有可应用的 VLESS 节点"
	}
	return rev, nil
}

func Fingerprint(node domain.Node) (string, error) {
	canonical := struct {
		Type             string   `json:"type"`
		Server           string   `json:"server"`
		Port             uint16   `json:"port"`
		UUID             string   `json:"uuid"`
		Flow             string   `json:"flow"`
		Network          string   `json:"network"`
		Security         string   `json:"security"`
		ServerName       string   `json:"server_name"`
		Fingerprint      string   `json:"fingerprint"`
		ALPN             []string `json:"alpn"`
		RealityPublicKey string   `json:"reality_public_key"`
		RealityShortID   string   `json:"reality_short_id"`
		Path             string   `json:"path"`
		Host             string   `json:"host"`
		ServiceName      string   `json:"service_name"`
		Insecure         bool     `json:"insecure"`
		PacketEncoding   string   `json:"packet_encoding"`
	}{
		strings.ToLower(node.Type), strings.ToLower(node.Server), node.Port, strings.ToLower(node.UUID), node.Flow,
		strings.ToLower(node.Network), strings.ToLower(node.Security), strings.ToLower(node.ServerName), strings.ToLower(node.Fingerprint), node.ALPN,
		node.RealityPublicKey, node.RealityShortID, node.Path, node.Host, node.ServiceName, node.Insecure, node.PacketEncoding,
	}
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

func newIdentity(registryKey, fingerprint string, now time.Time) (domain.Identity, error) {
	userToken, err := randomToken(8)
	if err != nil {
		return domain.Identity{}, err
	}
	password, err := randomToken(24)
	if err != nil {
		return domain.Identity{}, err
	}
	nodeHash := sha256.Sum256([]byte(registryKey))
	return domain.Identity{
		NodeID:      "n_" + hex.EncodeToString(nodeHash[:8]),
		Fingerprint: fingerprint,
		AuthUser:    "v2s_" + userToken,
		Password:    password,
		CreatedAt:   now,
		LastSeenAt:  now,
	}, nil
}

func randomToken(bytes int) (string, error) {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func compileOptionalRegex(value string) (*regexp.Regexp, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	return regexp.Compile(value)
}

func ensureUniqueDisplayNames(nodes []domain.RuntimeNode) {
	counts := map[string]int{}
	for index := range nodes {
		base := nodes[index].DisplayName
		counts[base]++
		if counts[base] > 1 {
			nodes[index].DisplayName = fmt.Sprintf("%s [%d]", base, counts[base])
		}
	}
}
