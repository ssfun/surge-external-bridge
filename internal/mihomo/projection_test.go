package mihomo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/metacubex/mihomo/common/utils"
	C "github.com/metacubex/mihomo/constant"
)

var errDialMarker = errors.New("dial marker")
var errPacketMarker = errors.New("packet marker")

type fakeProxy struct {
	name        string
	adapterType C.AdapterType
	udp         bool
	dials       int
	packets     int
}

func (p *fakeProxy) Name() string                 { return p.name }
func (p *fakeProxy) Type() C.AdapterType          { return p.adapterType }
func (p *fakeProxy) Addr() string                 { return "example.invalid:443" }
func (p *fakeProxy) SupportUDP() bool             { return p.udp }
func (p *fakeProxy) ProxyInfo() C.ProxyInfo       { return C.ProxyInfo{ProviderName: "fixture"} }
func (p *fakeProxy) MarshalJSON() ([]byte, error) { return []byte(`{"type":"fixture"}`), nil }
func (p *fakeProxy) DialContext(context.Context, *C.Metadata) (C.Conn, error) {
	p.dials++
	return nil, errDialMarker
}
func (p *fakeProxy) ListenPacketContext(context.Context, *C.Metadata) (C.PacketConn, error) {
	p.packets++
	return nil, errPacketMarker
}
func (p *fakeProxy) SupportUOT() bool                             { return p.udp }
func (p *fakeProxy) IsL3Protocol(*C.Metadata) bool                { return false }
func (p *fakeProxy) Unwrap(*C.Metadata, bool) C.Proxy             { return nil }
func (p *fakeProxy) Close() error                                 { return nil }
func (p *fakeProxy) Adapter() C.ProxyAdapter                      { return p }
func (p *fakeProxy) AliveForTestUrl(string) bool                  { return true }
func (p *fakeProxy) DelayHistory() []C.DelayHistory               { return nil }
func (p *fakeProxy) ExtraDelayHistories() map[string]C.ProxyState { return nil }
func (p *fakeProxy) LastDelayForTestUrl(string) uint16            { return 1 }
func (p *fakeProxy) URLTest(context.Context, string, utils.IntRanges[uint16]) (uint16, error) {
	return 1, nil
}

func TestProjectionIdentityIsStableAcrossProxyReplacement(t *testing.T) {
	key := make([]byte, 32)
	firstProxy := &fakeProxy{name: "香港 01", adapterType: C.Vless, udp: true}
	first := mustProjection(t, key, []ProviderView{{StableID: "provider-id", Name: "机场", Proxies: []C.Proxy{firstProxy}}})
	replacement := &fakeProxy{name: "香港 01", adapterType: C.Vless, udp: false}
	second := mustProjection(t, key, []ProviderView{{StableID: "provider-id", Name: "机场", Proxies: []C.Proxy{replacement}}})

	a := first.Entries()[0]
	b := second.Entries()[0]
	if a.Username != b.Username || a.Password != b.Password || a.PublicID != b.PublicID {
		t.Fatalf("identity changed across same-name proxy replacement: %#v != %#v", a, b)
	}
	if len(a.Username) != 30 || !strings.HasPrefix(a.Username, "surgeeb_") || len(a.Password) != 43 || len(a.PublicID) != 24 {
		t.Fatalf("unexpected derived identity lengths: user=%d pass=%d id=%d", len(a.Username), len(a.Password), len(a.PublicID))
	}
	if first.Revision() == second.Revision() {
		t.Fatal("projection revision did not change when published udp-relay capability changed")
	}
	renamed := mustProjection(t, key, []ProviderView{{StableID: "renamed-provider-id", Name: "已改名机场", Proxies: []C.Proxy{replacement}}}).Entries()[0]
	if renamed.Username == a.Username || renamed.Password == a.Password || renamed.PublicID == a.PublicID {
		t.Fatal("provider name change retained the old projected identity")
	}
}

func TestProjectionFiltersWithoutMutatingProvider(t *testing.T) {
	key := make([]byte, 32)
	all := []C.Proxy{
		&fakeProxy{name: "香港 VLESS", adapterType: C.Vless},
		&fakeProxy{name: "日本 VLESS", adapterType: C.Vless},
		&fakeProxy{name: "香港 Trojan", adapterType: C.Trojan},
	}
	snapshot := mustProjection(t, key, []ProviderView{{
		StableID: "p", Name: "provider", Proxies: all,
		IncludeName: "香港", IncludeTypes: []C.AdapterType{C.Vless},
	}})
	if got := len(snapshot.Entries()); got != 1 {
		t.Fatalf("projected %d entries, want 1", got)
	}
	if got := len(all); got != 3 {
		t.Fatalf("provider slice was mutated, len=%d", got)
	}
}

func TestProjectionIncludesMultipleProviderProtocolsWhenTypeScopeIsOpen(t *testing.T) {
	proxies := []C.Proxy{
		&fakeProxy{name: "VLESS", adapterType: C.Vless},
		&fakeProxy{name: "Trojan", adapterType: C.Trojan},
		&fakeProxy{name: "WireGuard", adapterType: C.WireGuard},
	}
	snapshot := mustProjection(t, make([]byte, 32), []ProviderView{{StableID: "p", Name: "provider", Proxies: proxies}})
	if got := len(snapshot.Entries()); got != len(proxies) {
		t.Fatalf("projected %d entries, want all %d protocols", got, len(proxies))
	}
}

func TestAuthenticatorAndRouterFailClosedAfterSnapshotChange(t *testing.T) {
	key := make([]byte, 32)
	proxy := &fakeProxy{name: "node", adapterType: C.Vless, udp: true}
	snapshot := mustProjection(t, key, []ProviderView{{StableID: "p", Name: "Provider", Proxies: []C.Proxy{proxy}}})
	entry := snapshot.Entries()[0]
	store := NewSnapshotStore(snapshot)
	authenticator := NewAuthenticator(store)
	router := NewRouter(store)

	if !authenticator.Verify(entry.Username, entry.Password) {
		t.Fatal("current identity was rejected")
	}
	if authenticator.Verify(entry.Username, "wrong") || authenticator.Verify("unknown", entry.Password) {
		t.Fatal("invalid identity was accepted")
	}
	metadata := &C.Metadata{InUser: entry.Username}
	if _, err := router.DialContext(context.Background(), metadata); !errors.Is(err, errDialMarker) || proxy.dials != 1 {
		t.Fatalf("TCP was not delegated to selected proxy: err=%v calls=%d", err, proxy.dials)
	}
	if _, err := router.ListenPacketContext(context.Background(), metadata); !errors.Is(err, errPacketMarker) || proxy.packets != 1 {
		t.Fatalf("UDP was not delegated to selected proxy: err=%v calls=%d", err, proxy.packets)
	}

	store.Store(EmptySnapshot())
	if authenticator.Verify(entry.Username, entry.Password) {
		t.Fatal("stale identity remained valid")
	}
	if _, err := router.DialContext(context.Background(), metadata); !errors.Is(err, ErrUnknownIdentity) {
		t.Fatalf("stale route did not fail closed: %v", err)
	}
}

func TestRouterRejectsUDPWhenSelectedProxyDoesNotSupportIt(t *testing.T) {
	proxy := &fakeProxy{name: "tcp-only", adapterType: C.Vless, udp: false}
	snapshot := mustProjection(t, make([]byte, 32), []ProviderView{{StableID: "p", Name: "Provider", Proxies: []C.Proxy{proxy}}})
	entry := snapshot.Entries()[0]
	router := NewRouter(NewSnapshotStore(snapshot))
	if _, err := router.ListenPacketContext(context.Background(), &C.Metadata{InUser: entry.Username}); !errors.Is(err, ErrUDPUnsupported) {
		t.Fatalf("got %v, want ErrUDPUnsupported", err)
	}
	if proxy.packets != 0 {
		t.Fatal("UDP was delegated to an unsupported proxy")
	}
}

func TestProjectionTreatsUOTAsSurgeUDPTransport(t *testing.T) {
	proxy := &uotOnlyProxy{fakeProxy: fakeProxy{name: "uot-only", adapterType: C.Vless}}
	snapshot := mustProjection(t, make([]byte, 32), []ProviderView{{StableID: "p", Name: "Provider", Proxies: []C.Proxy{proxy}}})
	entry := snapshot.Entries()[0]
	if !entry.SupportUDP || !entry.SupportUOT {
		t.Fatalf("UOT-only VLESS was not projected as UDP capable: %#v", entry)
	}
	if _, err := NewRouter(NewSnapshotStore(snapshot)).ListenPacketContext(context.Background(), &C.Metadata{InUser: entry.Username}); !errors.Is(err, errPacketMarker) {
		t.Fatalf("UOT-only VLESS was rejected by Router: %v", err)
	}
}

type uotOnlyProxy struct{ fakeProxy }

func (p *uotOnlyProxy) SupportUOT() bool { return true }

func TestDisplayNamesRemainUniqueWithNaturalSuffixCollision(t *testing.T) {
	providers := []ProviderView{
		{StableID: "p1", Name: "Provider 1", Proxies: []C.Proxy{&fakeProxy{name: "A", adapterType: C.Vless}}},
		{StableID: "p2", Name: "Provider 2", Proxies: []C.Proxy{&fakeProxy{name: "A", adapterType: C.Vless}}},
		{StableID: "p3", Name: "Provider 3", Proxies: []C.Proxy{&fakeProxy{name: "A · 2", adapterType: C.Vless}}},
		{StableID: "p4", Name: "Provider 4", Proxies: []C.Proxy{&fakeProxy{name: "A=B", adapterType: C.Vless}}},
		{StableID: "p5", Name: "Provider 5", Proxies: []C.Proxy{&fakeProxy{name: "A-B", adapterType: C.Vless}}},
		{StableID: "p6", Name: "Provider 6", Proxies: []C.Proxy{&fakeProxy{name: "#comment\nnode", adapterType: C.Vless}}},
	}
	snapshot := mustProjection(t, make([]byte, 32), providers)
	seen := map[string]bool{}
	for _, entry := range snapshot.Entries() {
		if seen[entry.DisplayName] {
			t.Fatalf("duplicate display name %q", entry.DisplayName)
		}
		if strings.ContainsAny(entry.DisplayName, "=\r\n") || strings.HasPrefix(entry.DisplayName, "#") || strings.HasPrefix(entry.DisplayName, ";") {
			t.Fatalf("unsafe Surge display name %q", entry.DisplayName)
		}
		seen[entry.DisplayName] = true
	}
}

func TestProjectionSupportsOneHundredFiftyUniqueIdentitiesOnOnePort(t *testing.T) {
	proxies := make([]C.Proxy, 150)
	for index := range proxies {
		proxies[index] = &fakeProxy{name: fmt.Sprintf("Node %03d", index), adapterType: C.Vless, udp: true}
	}
	snapshot := mustProjection(t, []byte("01234567890123456789012345678901"), []ProviderView{{StableID: "provider", Name: "Provider", Proxies: proxies}})
	if len(snapshot.Entries()) != 150 {
		t.Fatalf("projection count = %d", len(snapshot.Entries()))
	}
	users, ids := map[string]bool{}, map[string]bool{}
	store := NewSnapshotStore(snapshot)
	authenticator := NewAuthenticator(store)
	router := NewRouter(store)
	for _, entry := range snapshot.Entries() {
		if users[entry.Username] || ids[entry.PublicID] {
			t.Fatal("derived identity collision")
		}
		users[entry.Username], ids[entry.PublicID] = true, true
		if !authenticator.Verify(entry.Username, entry.Password) {
			t.Fatalf("identity for %q did not authenticate", entry.ProxyName)
		}
		selected, ok := entry.Proxy.(*fakeProxy)
		if !ok {
			t.Fatalf("entry %q retained unexpected proxy type %T", entry.ProxyName, entry.Proxy)
		}
		metadata := &C.Metadata{InUser: entry.Username}
		if _, err := router.DialContext(context.Background(), metadata); !errors.Is(err, errDialMarker) || selected.dials != 1 {
			t.Fatalf("identity for %q selected the wrong TCP proxy: err=%v calls=%d", entry.ProxyName, err, selected.dials)
		}
		if _, err := router.ListenPacketContext(context.Background(), metadata); !errors.Is(err, errPacketMarker) || selected.packets != 1 {
			t.Fatalf("identity for %q selected the wrong UDP proxy: err=%v calls=%d", entry.ProxyName, err, selected.packets)
		}
	}
	for _, proxy := range proxies {
		selected := proxy.(*fakeProxy)
		if selected.dials != 1 || selected.packets != 1 {
			t.Fatalf("proxy %q route counts TCP=%d UDP=%d, want 1/1", selected.name, selected.dials, selected.packets)
		}
	}
}

func mustProjection(t *testing.T, key []byte, providers []ProviderView) *Snapshot {
	t.Helper()
	snapshot, err := BuildProjection(providers, BuildOptions{MasterKey: key, SocksAdvertise: net.IPv4(127, 0, 0, 1).String(), SocksPort: 1080})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
