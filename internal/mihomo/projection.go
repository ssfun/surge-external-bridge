package mihomo

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	A "github.com/metacubex/mihomo/adapter"
	C "github.com/metacubex/mihomo/constant"
)

const RouterName = "surgeeb-router"

var (
	ErrUnknownIdentity = errors.New("unknown or stale SOCKS identity")
	ErrUDPUnsupported  = errors.New("selected provider proxy does not support UDP")
)

type ProviderView struct {
	StableID     string
	Name         string
	Proxies      []C.Proxy
	IncludeName  string
	ExcludeName  string
	IncludeTypes []C.AdapterType
}

type BuildOptions struct {
	MasterKey      []byte
	SocksAdvertise string
	SocksPort      uint16
	PrefixProvider bool
}

type Entry struct {
	ProviderID   string
	ProviderName string
	ProxyName    string
	PublicID     string
	DisplayName  string
	SocksHost    string
	SocksPort    uint16
	Username     string
	Password     string
	Proxy        C.Proxy
	SupportUDP   bool
	SupportUOT   bool
	Info         C.ProxyInfo
}

type Snapshot struct {
	revision string
	entries  []Entry
	byUser   map[string]Entry
	byID     map[string]Entry
}

func EmptySnapshot() *Snapshot {
	hash := sha256.Sum256([]byte("[]"))
	return &Snapshot{revision: hex.EncodeToString(hash[:]), entries: []Entry{}, byUser: map[string]Entry{}, byID: map[string]Entry{}}
}

func (s *Snapshot) Revision() string {
	if s == nil {
		return ""
	}
	return s.revision
}

func (s *Snapshot) Entries() []Entry {
	if s == nil {
		return nil
	}
	return append([]Entry(nil), s.entries...)
}

func (s *Snapshot) EntryByUser(user string) (Entry, bool) {
	if s == nil {
		return Entry{}, false
	}
	entry, ok := s.byUser[user]
	return entry, ok
}

func (s *Snapshot) EntryByID(id string) (Entry, bool) {
	if s == nil {
		return Entry{}, false
	}
	entry, ok := s.byID[id]
	return entry, ok
}

type SnapshotStore struct {
	current         atomic.Pointer[Snapshot]
	touchMu         sync.Mutex
	providerTouches map[string]time.Time
}

func NewSnapshotStore(initial *Snapshot) *SnapshotStore {
	store := &SnapshotStore{providerTouches: make(map[string]time.Time)}
	store.Store(initial)
	return store
}

func (s *SnapshotStore) Load() *Snapshot {
	if current := s.current.Load(); current != nil {
		return current
	}
	return EmptySnapshot()
}

func (s *SnapshotStore) Store(snapshot *Snapshot) {
	if snapshot == nil {
		snapshot = EmptySnapshot()
	}
	s.current.Store(snapshot)
}

func (s *SnapshotStore) TouchProvider(stableID string) {
	if stableID == "" {
		return
	}
	s.touchMu.Lock()
	if s.providerTouches == nil {
		s.providerTouches = make(map[string]time.Time)
	}
	s.providerTouches[stableID] = time.Now()
	s.touchMu.Unlock()
}

func (s *SnapshotStore) ProviderTouchedSince(stableID string, since time.Time) bool {
	s.touchMu.Lock()
	touched := s.providerTouches[stableID]
	s.touchMu.Unlock()
	return !touched.IsZero() && touched.After(since)
}

func BuildProjection(providers []ProviderView, options BuildOptions) (*Snapshot, error) {
	if len(options.MasterKey) < 32 {
		return nil, errors.New("projection master key must contain at least 32 bytes")
	}
	if strings.TrimSpace(options.SocksAdvertise) == "" || options.SocksPort == 0 {
		return nil, errors.New("SOCKS advertise address and port are required")
	}

	entries := make([]Entry, 0)
	seenProvider := make(map[string]struct{}, len(providers))
	seenProviderName := make(map[string]struct{}, len(providers))
	seenUsers := make(map[string]struct{})
	seenIDs := make(map[string]struct{})
	for _, provider := range providers {
		if provider.StableID == "" {
			return nil, errors.New("provider stable ID is required")
		}
		if _, exists := seenProvider[provider.StableID]; exists {
			return nil, fmt.Errorf("duplicate provider stable ID %q", provider.StableID)
		}
		seenProvider[provider.StableID] = struct{}{}
		providerName := strings.TrimSpace(provider.Name)
		if providerName == "" {
			return nil, errors.New("provider name is required")
		}
		foldedName := strings.ToLower(providerName)
		if _, exists := seenProviderName[foldedName]; exists {
			return nil, fmt.Errorf("duplicate provider name %q", providerName)
		}
		seenProviderName[foldedName] = struct{}{}
		include, err := compileOptionalRegexp(provider.IncludeName)
		if err != nil {
			return nil, fmt.Errorf("provider %q include filter: %w", provider.Name, err)
		}
		exclude, err := compileOptionalRegexp(provider.ExcludeName)
		if err != nil {
			return nil, fmt.Errorf("provider %q exclude filter: %w", provider.Name, err)
		}
		allowedTypes := make(map[C.AdapterType]struct{}, len(provider.IncludeTypes))
		for _, adapterType := range provider.IncludeTypes {
			allowedTypes[adapterType] = struct{}{}
		}
		for _, proxy := range provider.Proxies {
			if proxy == nil || !matchesProjection(proxy, include, exclude, allowedTypes) {
				continue
			}
			nodeKey := providerName + "\x00" + proxy.Name()
			entry := Entry{
				ProviderID:   provider.StableID,
				ProviderName: provider.Name,
				ProxyName:    proxy.Name(),
				PublicID:     "n_" + digest(nodeKey)[:22],
				SocksHost:    options.SocksAdvertise,
				SocksPort:    options.SocksPort,
				Username:     "surgeeb_" + keyedDigest(options.MasterKey, "user:"+nodeKey)[:22],
				Password:     keyedDigest(options.MasterKey, "pass:"+nodeKey),
				Proxy:        proxy,
				SupportUDP:   proxy.SupportUDP() || proxy.SupportUOT(),
				SupportUOT:   proxy.SupportUOT(),
				Info:         proxy.ProxyInfo(),
			}
			if options.PrefixProvider && provider.Name != "" {
				entry.DisplayName = surgeDisplayName(provider.Name + " · " + proxy.Name())
			} else {
				entry.DisplayName = surgeDisplayName(proxy.Name())
			}
			if _, exists := seenUsers[entry.Username]; exists {
				return nil, fmt.Errorf("derived username collision for %q", entry.ProxyName)
			}
			if _, exists := seenIDs[entry.PublicID]; exists {
				return nil, fmt.Errorf("derived public ID collision for %q", entry.ProxyName)
			}
			seenUsers[entry.Username] = struct{}{}
			seenIDs[entry.PublicID] = struct{}{}
			entries = append(entries, entry)
		}
	}
	disambiguateDisplayNames(entries)

	byUser := make(map[string]Entry, len(entries))
	byID := make(map[string]Entry, len(entries))
	for _, entry := range entries {
		byUser[entry.Username] = entry
		byID[entry.PublicID] = entry
	}
	published := make([][6]string, 0, len(entries))
	for _, entry := range entries {
		published = append(published, [6]string{entry.DisplayName, entry.SocksHost, fmt.Sprint(entry.SocksPort), entry.Username, entry.Password, fmt.Sprint(entry.SupportUDP)})
	}
	encoded, err := json.Marshal(published)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(encoded)
	return &Snapshot{revision: hex.EncodeToString(hash[:]), entries: entries, byUser: byUser, byID: byID}, nil
}

func surgeDisplayName(value string) string {
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
		return "Unnamed external node"
	}
	if strings.HasPrefix(value, "#") || strings.HasPrefix(value, ";") {
		return "Node " + value
	}
	return value
}

func compileOptionalRegexp(expression string) (*regexp.Regexp, error) {
	if expression == "" {
		return nil, nil
	}
	return regexp.Compile(expression)
}

func matchesProjection(proxy C.Proxy, include, exclude *regexp.Regexp, types map[C.AdapterType]struct{}) bool {
	if len(types) > 0 {
		if _, ok := types[proxy.Type()]; !ok {
			return false
		}
	}
	if include != nil && !include.MatchString(proxy.Name()) {
		return false
	}
	return exclude == nil || !exclude.MatchString(proxy.Name())
}

func keyedDigest(key []byte, input string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(input))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func digest(input string) string {
	hash := sha256.Sum256([]byte(input))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

func disambiguateDisplayNames(entries []Entry) {
	used := make(map[string]struct{}, len(entries))
	next := make(map[string]int, len(entries))
	for i := range entries {
		base := entries[i].DisplayName
		if _, exists := used[base]; !exists {
			used[base] = struct{}{}
			next[base] = 2
			continue
		}
		for sequence := next[base]; ; sequence++ {
			candidate := fmt.Sprintf("%s · %d", base, sequence)
			if _, exists := used[candidate]; exists {
				continue
			}
			entries[i].DisplayName = candidate
			used[candidate] = struct{}{}
			next[base] = sequence + 1
			break
		}
	}
}

type Authenticator struct {
	store *SnapshotStore
}

func NewAuthenticator(store *SnapshotStore) *Authenticator {
	return &Authenticator{store: store}
}

func (a *Authenticator) Verify(user, pass string) bool {
	entry, ok := a.store.Load().EntryByUser(user)
	expected := entry.Password
	if !ok {
		expected = keyedDigest(make([]byte, 32), "unknown")
	}
	match := subtle.ConstantTimeCompare([]byte(expected), []byte(pass)) == 1
	return ok && match
}

func (a *Authenticator) Users() []string {
	entries := a.store.Load().Entries()
	users := make([]string, 0, len(entries))
	for _, entry := range entries {
		users = append(users, entry.Username)
	}
	sort.Strings(users)
	return users
}

type Router struct {
	store *SnapshotStore
}

func NewRouter(store *SnapshotStore) *Router { return &Router{store: store} }

func (r *Router) Name() string                     { return RouterName }
func (r *Router) Type() C.AdapterType              { return C.Compatible }
func (r *Router) Addr() string                     { return "dynamic://authenticated-user" }
func (r *Router) SupportUDP() bool                 { return true }
func (r *Router) SupportUOT() bool                 { return true }
func (r *Router) ProxyInfo() C.ProxyInfo           { return C.ProxyInfo{} }
func (r *Router) IsL3Protocol(*C.Metadata) bool    { return false }
func (r *Router) Unwrap(*C.Metadata, bool) C.Proxy { return nil }
func (r *Router) Close() error                     { return nil }

func (r *Router) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"name": RouterName, "type": "AuthenticatedUserRouter", "udp": true})
}

func (r *Router) DialContext(ctx context.Context, metadata *C.Metadata) (C.Conn, error) {
	entry, err := r.entry(metadata)
	if err != nil {
		return nil, err
	}
	return entry.Proxy.DialContext(ctx, metadata)
}

func (r *Router) ListenPacketContext(ctx context.Context, metadata *C.Metadata) (C.PacketConn, error) {
	entry, err := r.entry(metadata)
	if err != nil {
		return nil, err
	}
	if !entry.SupportUDP {
		return nil, ErrUDPUnsupported
	}
	return entry.Proxy.ListenPacketContext(ctx, metadata)
}

func (r *Router) entry(metadata *C.Metadata) (Entry, error) {
	if metadata == nil || metadata.InUser == "" {
		return Entry{}, ErrUnknownIdentity
	}
	entry, ok := r.store.Load().EntryByUser(metadata.InUser)
	if !ok {
		return Entry{}, ErrUnknownIdentity
	}
	r.store.TouchProvider(entry.ProviderID)
	return entry, nil
}

func WrappedRouter(store *SnapshotStore) C.Proxy {
	return A.NewProxy(NewRouter(store))
}
