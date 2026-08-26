package mihomo

import (
	"net"
	"testing"
	"time"

	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/transport/socks5"
)

type captureTunnel struct {
	tcp chan *C.Metadata
	udp chan *C.Metadata
}

func (t *captureTunnel) HandleTCPConn(connection net.Conn, metadata *C.Metadata) {
	t.tcp <- metadata
	_ = connection.Close()
}
func (t *captureTunnel) HandleUDPPacket(packet C.UDPPacket, metadata *C.Metadata) {
	t.udp <- metadata
	packet.Drop()
}
func (t *captureTunnel) NatTable() C.NatTable { return nil }

func TestAuthenticatedSOCKSListenerPropagatesTCPAndBoundUDPIdentity(t *testing.T) {
	proxy := &fakeProxy{name: "node", adapterType: C.Vless, udp: true}
	snapshot := mustProjection(t, make([]byte, 32), []ProviderView{{StableID: "p", Name: "Provider", Proxies: []C.Proxy{proxy}}})
	entry := snapshot.Entries()[0]
	store := NewSnapshotStore(snapshot)
	tunnel := &captureTunnel{tcp: make(chan *C.Metadata, 1), udp: make(chan *C.Metadata, 2)}
	listener, err := NewSOCKSListener("127.0.0.1:0", tunnel, NewAuthenticator(store))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	control, err := net.DialTimeout("tcp", listener.Address(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := socks5.ClientHandshake(control, socks5.ParseAddr("example.com:443"), socks5.CmdConnect, &socks5.User{Username: entry.Username, Password: entry.Password}); err != nil {
		t.Fatal(err)
	}
	select {
	case metadata := <-tunnel.tcp:
		if metadata.InUser != entry.Username || metadata.SpecialProxy != RouterName {
			t.Fatalf("TCP metadata lost identity: %#v", metadata)
		}
	case <-time.After(time.Second):
		t.Fatal("TCP inbound was not delivered")
	}
	_ = control.Close()
	unauthenticated, err := net.DialTimeout("tcp", listener.Address(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := socks5.ClientHandshake(unauthenticated, socks5.ParseAddr("example.com:443"), socks5.CmdConnect, nil); err == nil {
		t.Fatal("SOCKS listener accepted a no-authentication client")
	}
	_ = unauthenticated.Close()

	udpSocket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer udpSocket.Close()
	control, err = net.DialTimeout("tcp", listener.Address(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := socks5.ClientHandshake(control, socks5.ParseAddr(udpSocket.LocalAddr().String()), socks5.CmdUDPAssociate, &socks5.User{Username: entry.Username, Password: entry.Password})
	if err != nil {
		t.Fatal(err)
	}
	packet, err := socks5.EncodeUDPPacket(socks5.ParseAddr("1.1.1.1:53"), []byte("query"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := udpSocket.WriteToUDP(packet, bound.UDPAddr()); err != nil {
		t.Fatal(err)
	}
	select {
	case metadata := <-tunnel.udp:
		if metadata.InUser != entry.Username || metadata.SpecialProxy != RouterName {
			t.Fatalf("UDP metadata lost identity: %#v", metadata)
		}
	case <-time.After(time.Second):
		t.Fatal("bound UDP packet was not delivered")
	}
	_ = control.Close()
	time.Sleep(20 * time.Millisecond)
	if _, err := udpSocket.WriteToUDP(packet, bound.UDPAddr()); err != nil {
		t.Fatal(err)
	}
	select {
	case metadata := <-tunnel.udp:
		t.Fatalf("UDP packet survived control association close: %#v", metadata)
	case <-time.After(50 * time.Millisecond):
	}

	unknown, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer unknown.Close()
	if _, err := unknown.WriteToUDP(packet, bound.UDPAddr()); err != nil {
		t.Fatal(err)
	}
	select {
	case metadata := <-tunnel.udp:
		t.Fatalf("unbound UDP packet was delivered: %#v", metadata)
	case <-time.After(50 * time.Millisecond):
	}

}

func TestRuntimeAuthenticationProbeValidatesCurrentSnapshot(t *testing.T) {
	proxy := &fakeProxy{name: "node", adapterType: C.Vless, udp: true}
	snapshot := mustProjection(t, make([]byte, 32), []ProviderView{{StableID: "p", Name: "Provider", Proxies: []C.Proxy{proxy}}})
	listener, err := NewSOCKSListener("127.0.0.1:0", &captureTunnel{tcp: make(chan *C.Metadata, 1), udp: make(chan *C.Metadata, 1)}, NewAuthenticator(NewSnapshotStore(snapshot)))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := validateSOCKSAuthenticationRuntime(listener.Address(), snapshot); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeAuthenticationProbeSupportsEmptyProjection(t *testing.T) {
	snapshot := EmptySnapshot()
	listener, err := NewSOCKSListener("127.0.0.1:0", &captureTunnel{tcp: make(chan *C.Metadata, 1), udp: make(chan *C.Metadata, 1)}, NewAuthenticator(NewSnapshotStore(snapshot)))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := validateSOCKSAuthenticationRuntime(listener.Address(), snapshot); err != nil {
		t.Fatal(err)
	}
}

func TestAssociationRegistryBindsExactEndpointToAuthenticatedUser(t *testing.T) {
	registry := newAssociationRegistry(10, 10, time.Minute)
	association, err := registry.add("user-a", tcpAddress("192.0.2.1", 40000), udpAddress("0.0.0.0", 50000))
	if err != nil {
		t.Fatal(err)
	}
	if user, ok := registry.resolve(udpAddress("192.0.2.1", 50000)); !ok || user != "user-a" {
		t.Fatalf("exact association resolved as user=%q ok=%v", user, ok)
	}
	if _, ok := registry.resolve(udpAddress("192.0.2.1", 50001)); ok {
		t.Fatal("unassociated UDP endpoint was accepted")
	}
	registry.remove(association.id)
	if _, ok := registry.resolve(udpAddress("192.0.2.1", 50000)); ok {
		t.Fatal("removed association remained active")
	}
}

func TestAssociationRegistryZeroPortFailsClosedWhenAmbiguous(t *testing.T) {
	registry := newAssociationRegistry(10, 10, time.Minute)
	if _, err := registry.add("user-a", tcpAddress("192.0.2.1", 40000), udpAddress("0.0.0.0", 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.add("user-b", tcpAddress("192.0.2.1", 40001), udpAddress("0.0.0.0", 0)); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.resolve(udpAddress("192.0.2.1", 50000)); ok {
		t.Fatal("ambiguous zero-port association was guessed")
	}
}

func TestAssociationRegistryZeroPortBindsFirstPacketWhenUnambiguous(t *testing.T) {
	registry := newAssociationRegistry(10, 10, time.Minute)
	if _, err := registry.add("user-a", tcpAddress("192.0.2.1", 40000), udpAddress("0.0.0.0", 0)); err != nil {
		t.Fatal(err)
	}
	if user, ok := registry.resolve(udpAddress("192.0.2.1", 50000)); !ok || user != "user-a" {
		t.Fatalf("first packet did not bind: user=%q ok=%v", user, ok)
	}
	if _, ok := registry.resolve(udpAddress("192.0.2.1", 50001)); ok {
		t.Fatal("association moved to a different endpoint")
	}
}

func TestAssociationRegistryRejectsCrossIPClaimAndExpires(t *testing.T) {
	registry := newAssociationRegistry(10, 10, time.Millisecond)
	if _, err := registry.add("user", tcpAddress("192.0.2.1", 40000), udpAddress("192.0.2.2", 50000)); err == nil {
		t.Fatal("cross-IP UDP endpoint claim was accepted")
	}
	association, err := registry.add("user", tcpAddress("192.0.2.1", 40000), udpAddress("192.0.2.1", 50000))
	if err != nil {
		t.Fatal(err)
	}
	control := &testCloser{}
	registry.attachControl(association.id, control)
	registry.expire(association.lastSeen.Add(time.Millisecond))
	if !control.closed {
		t.Fatal("expiring an association did not close its TCP control connection")
	}
	if _, ok := registry.resolve(udpAddress("192.0.2.1", 50000)); ok {
		t.Fatal("expired association remained active")
	}
}

func TestAssociationRegistryEnforcesCapacityAndEndpointUniqueness(t *testing.T) {
	registry := newAssociationRegistry(2, 1, time.Minute)
	if _, err := registry.add("user-a", tcpAddress("192.0.2.1", 40000), udpAddress("192.0.2.1", 50000)); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.add("user-b", tcpAddress("192.0.2.1", 40001), udpAddress("192.0.2.1", 50001)); err == nil {
		t.Fatal("per-IP association limit was not enforced")
	}
	if _, err := registry.add("user-c", tcpAddress("192.0.2.2", 40002), udpAddress("192.0.2.2", 50000)); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.add("user-d", tcpAddress("192.0.2.3", 40003), udpAddress("192.0.2.3", 50000)); err == nil {
		t.Fatal("total association limit was not enforced")
	}

	unique := newAssociationRegistry(10, 10, time.Minute)
	if _, err := unique.add("user-a", tcpAddress("192.0.2.10", 41000), udpAddress("192.0.2.10", 51000)); err != nil {
		t.Fatal(err)
	}
	if _, err := unique.add("user-b", tcpAddress("192.0.2.10", 41001), udpAddress("192.0.2.10", 51000)); err == nil {
		t.Fatal("duplicate UDP source endpoint was assigned to a second identity")
	}
}

type testCloser struct{ closed bool }

func (c *testCloser) Close() error { c.closed = true; return nil }

func tcpAddress(ip string, port int) net.Addr { return &net.TCPAddr{IP: net.ParseIP(ip), Port: port} }
func udpAddress(ip string, port int) *net.UDPAddr {
	return &net.UDPAddr{IP: net.ParseIP(ip), Port: port}
}
