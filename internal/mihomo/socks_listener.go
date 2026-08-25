package mihomo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	I "github.com/metacubex/mihomo/adapter/inbound"
	N "github.com/metacubex/mihomo/common/net"
	"github.com/metacubex/mihomo/component/auth"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/transport/socks5"
)

const (
	defaultMaxAssociations      = 512
	defaultMaxAssociationsPerIP = 64
	defaultAssociationIdle      = 5 * time.Minute
)

type SOCKSListener struct {
	tcp      net.Listener
	udp      net.PacketConn
	tunnel   C.Tunnel
	auth     auth.Authenticator
	registry *associationRegistry
	closed   atomic.Bool
	started  atomic.Bool
	ctx      context.Context
	cancel   context.CancelFunc
	wait     sync.WaitGroup
}

func NewSOCKSListener(address string, tunnel C.Tunnel, authenticator auth.Authenticator) (*SOCKSListener, error) {
	listener, err := BindSOCKSListener(address, tunnel, authenticator)
	if err != nil {
		return nil, err
	}
	listener.Start()
	return listener, nil
}

// BindSOCKSListener reserves both TCP and UDP sockets without accepting any
// traffic. Manager configuration changes use this to ensure the new endpoint is
// available before atomically publishing credentials and opening the listener.
func BindSOCKSListener(address string, tunnel C.Tunnel, authenticator auth.Authenticator) (*SOCKSListener, error) {
	if tunnel == nil || authenticator == nil {
		return nil, errors.New("SOCKS tunnel and authenticator are required")
	}
	tcpListener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("listen SOCKS TCP: %w", err)
	}
	bound := tcpListener.Addr().String()
	udpListener, err := net.ListenPacket("udp", bound)
	if err != nil {
		_ = tcpListener.Close()
		return nil, fmt.Errorf("listen SOCKS UDP: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	listener := &SOCKSListener{
		tcp:      tcpListener,
		udp:      udpListener,
		tunnel:   tunnel,
		auth:     authenticator,
		registry: newAssociationRegistry(defaultMaxAssociations, defaultMaxAssociationsPerIP, defaultAssociationIdle),
		ctx:      ctx,
		cancel:   cancel,
	}
	return listener, nil
}

func (l *SOCKSListener) Start() {
	if !l.started.CompareAndSwap(false, true) || l.closed.Load() {
		return
	}
	l.wait.Add(3)
	go l.acceptTCP()
	go l.acceptUDP()
	go l.expireAssociations(l.ctx)
}

func (l *SOCKSListener) Address() string { return l.tcp.Addr().String() }

func (l *SOCKSListener) Close() error {
	if !l.closed.CompareAndSwap(false, true) {
		return nil
	}
	l.cancel()
	tcpErr := l.tcp.Close()
	udpErr := l.udp.Close()
	l.registry.closeAll()
	if l.started.Load() {
		l.wait.Wait()
	}
	return errors.Join(tcpErr, udpErr)
}

// validateSOCKSAuthenticationRuntime proves that the published listener is
// backed by the current Projection Snapshot before Manager reports itself as
// running. It deliberately stops after RFC 1929 authentication, so the probe
// never creates an outbound connection or falls back to any routing rule.
func validateSOCKSAuthenticationRuntime(address string, snapshot *Snapshot) error {
	dialAddress, err := localDialAddress(address)
	if err != nil {
		return err
	}
	if err := probeNoAuthentication(dialAddress); err != nil {
		return fmt.Errorf("SOCKS runtime no-authentication assertion: %w", err)
	}
	unknownPassword := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if accepted, err := probeUserPassword(dialAddress, "surgeeb_runtime_probe_unknown", unknownPassword); err != nil {
		return fmt.Errorf("SOCKS runtime unknown-user assertion: %w", err)
	} else if accepted {
		return errors.New("SOCKS runtime assertion failed: unknown identity was accepted")
	}
	entries := snapshot.Entries()
	if len(entries) == 0 {
		return nil
	}
	entry := entries[0]
	if accepted, err := probeUserPassword(dialAddress, entry.Username, entry.Password); err != nil {
		return fmt.Errorf("SOCKS runtime valid-identity assertion: %w", err)
	} else if !accepted {
		return errors.New("SOCKS runtime assertion failed: current Projection identity was rejected")
	}
	wrongPassword := entry.Password
	if wrongPassword[0] == 'A' {
		wrongPassword = "B" + wrongPassword[1:]
	} else {
		wrongPassword = "A" + wrongPassword[1:]
	}
	if accepted, err := probeUserPassword(dialAddress, entry.Username, wrongPassword); err != nil {
		return fmt.Errorf("SOCKS runtime wrong-password assertion: %w", err)
	} else if accepted {
		return errors.New("SOCKS runtime assertion failed: wrong password was accepted")
	}
	return nil
}

func localDialAddress(address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("resolve SOCKS runtime probe address: %w", err)
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil && ip.IsUnspecified() {
		if ip.To4() == nil {
			host = "::1"
		} else {
			host = "127.0.0.1"
		}
	}
	return net.JoinHostPort(host, port), nil
}

func probeNoAuthentication(address string) error {
	connection, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		return err
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(time.Second))
	if _, err := connection.Write([]byte{5, 1, 0}); err != nil {
		return err
	}
	response := make([]byte, 2)
	if _, err := io.ReadFull(connection, response); err != nil {
		return err
	}
	if response[0] != 5 {
		return fmt.Errorf("invalid SOCKS response version %#x", response[0])
	}
	// Mihomo's handshake selects RFC 1929 even when the client offered only
	// no-authentication. Such a client still cannot continue without credentials;
	// the hard invariant is that method 0x00 must never be selected.
	if response[1] == 0 {
		return errors.New("listener accepted the no-authentication method")
	}
	return nil
}

func probeUserPassword(address, username, password string) (bool, error) {
	if len(username) == 0 || len(username) > 255 || len(password) == 0 || len(password) > 255 {
		return false, errors.New("invalid runtime probe credential length")
	}
	connection, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		return false, err
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(time.Second))
	if _, err := connection.Write([]byte{5, 1, 2}); err != nil {
		return false, err
	}
	response := make([]byte, 2)
	if _, err := io.ReadFull(connection, response); err != nil {
		return false, err
	}
	if response[0] != 5 || response[1] != 2 {
		return false, fmt.Errorf("listener selected authentication method %#x", response[1])
	}
	payload := []byte{1, byte(len(username))}
	payload = append(payload, username...)
	payload = append(payload, byte(len(password)))
	payload = append(payload, password...)
	if _, err := connection.Write(payload); err != nil {
		return false, err
	}
	if _, err := io.ReadFull(connection, response); err != nil {
		return false, err
	}
	if response[0] != 1 {
		return false, fmt.Errorf("invalid RFC 1929 response version %#x", response[0])
	}
	return response[1] == 0, nil
}

func (l *SOCKSListener) acceptTCP() {
	defer l.wait.Done()
	for {
		conn, err := l.tcp.Accept()
		if err != nil {
			if l.closed.Load() {
				return
			}
			continue
		}
		go l.handleTCP(conn)
	}
}

func (l *SOCKSListener) handleTCP(conn net.Conn) {
	buffered := N.NewBufferedConn(conn)
	version, err := buffered.Peek(1)
	if err != nil || len(version) != 1 || version[0] != socks5.Version {
		_ = conn.Close()
		return
	}
	target, command, user, err := socks5.ServerHandshake(buffered, l.auth)
	if err != nil {
		_ = conn.Close()
		return
	}
	additions := []I.Addition{
		I.WithInName("surgeeb-socks"),
		I.WithInUser(user),
		I.WithSpecialProxy(RouterName),
	}
	switch command {
	case socks5.CmdConnect:
		l.tunnel.HandleTCPConn(I.NewSocket(target, buffered, C.SOCKS5, additions...))
	case socks5.CmdUDPAssociate:
		defer conn.Close()
		association, err := l.registry.add(user, conn.RemoteAddr(), target.UDPAddr())
		if err != nil {
			return
		}
		l.registry.attachControl(association.id, conn)
		defer l.registry.remove(association.id)
		_, _ = io.Copy(io.Discard, buffered)
	default:
		_ = conn.Close()
	}
}

func (l *SOCKSListener) acceptUDP() {
	defer l.wait.Done()
	buffer := make([]byte, 64*1024)
	for {
		n, remote, err := l.udp.ReadFrom(buffer)
		if err != nil {
			if l.closed.Load() {
				return
			}
			continue
		}
		user, ok := l.registry.resolve(remote)
		if !ok {
			continue
		}
		target, payload, err := socks5.DecodeUDPPacket(buffer[:n])
		if err != nil {
			continue
		}
		data := append([]byte(nil), payload...)
		packet := &socksPacket{pc: l.udp, remote: remote, data: data}
		additions := []I.Addition{
			I.WithInName("surgeeb-socks"),
			I.WithInUser(user),
			I.WithSpecialProxy(RouterName),
		}
		l.tunnel.HandleUDPPacket(I.NewPacket(target, packet, C.SOCKS5, additions...))
	}
}

func (l *SOCKSListener) expireAssociations(ctx context.Context) {
	defer l.wait.Done()
	interval := l.registry.idle / 2
	if interval > time.Minute {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			l.registry.expire(now)
		}
	}
}

type socksPacket struct {
	pc     net.PacketConn
	remote net.Addr
	data   []byte
}

func (p *socksPacket) Data() []byte        { return p.data }
func (p *socksPacket) LocalAddr() net.Addr { return p.remote }
func (p *socksPacket) InAddr() net.Addr    { return p.pc.LocalAddr() }
func (p *socksPacket) Drop()               { p.data = nil }
func (p *socksPacket) WriteBack(data []byte, source net.Addr) (int, error) {
	encoded, err := socks5.EncodeUDPPacket(socks5.ParseAddrToSocksAddr(source), data)
	if err != nil {
		return 0, err
	}
	return p.pc.WriteTo(encoded, p.remote)
}

type association struct {
	id       uint64
	user     string
	ip       netip.Addr
	endpoint netip.AddrPort
	bound    bool
	lastSeen time.Time
	control  io.Closer
}

type associationRegistry struct {
	mu         sync.Mutex
	nextID     uint64
	byID       map[uint64]*association
	byEndpoint map[netip.AddrPort]*association
	maxTotal   int
	maxPerIP   int
	idle       time.Duration
}

func newAssociationRegistry(maxTotal, maxPerIP int, idle time.Duration) *associationRegistry {
	return &associationRegistry{
		byID: make(map[uint64]*association), byEndpoint: make(map[netip.AddrPort]*association),
		maxTotal: maxTotal, maxPerIP: maxPerIP, idle: idle,
	}
}

func (r *associationRegistry) add(user string, remote net.Addr, requested *net.UDPAddr) (*association, error) {
	ip, err := addressIP(remote)
	if err != nil || user == "" {
		return nil, errors.New("invalid UDP association identity or source")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.byID) >= r.maxTotal || r.countIPLocked(ip) >= r.maxPerIP {
		return nil, errors.New("UDP association limit reached")
	}
	r.nextID++
	item := &association{id: r.nextID, user: user, ip: ip, lastSeen: time.Now()}
	if requested != nil && requested.Port != 0 {
		requestedIP, ok := netip.AddrFromSlice(requested.IP)
		requestedIP = requestedIP.Unmap()
		if !ok || (!requestedIP.IsUnspecified() && requestedIP != ip) {
			return nil, errors.New("UDP association address does not match TCP source")
		}
		item.endpoint = netip.AddrPortFrom(ip, uint16(requested.Port))
		if _, exists := r.byEndpoint[item.endpoint]; exists {
			return nil, errors.New("UDP association endpoint is already in use")
		}
		item.bound = true
		r.byEndpoint[item.endpoint] = item
	}
	r.byID[item.id] = item
	return item, nil
}

func (r *associationRegistry) resolve(remote net.Addr) (string, bool) {
	endpoint, err := addressPort(remote)
	if err != nil {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if item := r.byEndpoint[endpoint]; item != nil {
		item.lastSeen = time.Now()
		return item.user, true
	}
	var candidate *association
	for _, item := range r.byID {
		if item.bound || item.ip != endpoint.Addr() {
			continue
		}
		if candidate != nil {
			return "", false
		}
		candidate = item
	}
	if candidate == nil {
		return "", false
	}
	candidate.endpoint = endpoint
	candidate.bound = true
	candidate.lastSeen = time.Now()
	r.byEndpoint[endpoint] = candidate
	return candidate.user, true
}

func (r *associationRegistry) remove(id uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removeLocked(id)
}

func (r *associationRegistry) attachControl(id uint64, control io.Closer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if item := r.byID[id]; item != nil {
		item.control = control
	}
}

func (r *associationRegistry) removeLocked(id uint64) {
	item := r.byID[id]
	if item == nil {
		return
	}
	delete(r.byID, id)
	if item.bound {
		delete(r.byEndpoint, item.endpoint)
	}
}

func (r *associationRegistry) expire(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, item := range r.byID {
		if now.Sub(item.lastSeen) >= r.idle {
			if item.control != nil {
				_ = item.control.Close()
			}
			r.removeLocked(id)
		}
	}
}

func (r *associationRegistry) closeAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, item := range r.byID {
		if item.control != nil {
			_ = item.control.Close()
		}
	}
	clear(r.byID)
	clear(r.byEndpoint)
}

func (r *associationRegistry) countIPLocked(ip netip.Addr) int {
	count := 0
	for _, item := range r.byID {
		if item.ip == ip {
			count++
		}
	}
	return count
}

func addressIP(address net.Addr) (netip.Addr, error) {
	endpoint, err := addressPort(address)
	return endpoint.Addr(), err
}

func addressPort(address net.Addr) (netip.AddrPort, error) {
	if address == nil {
		return netip.AddrPort{}, errors.New("missing network address")
	}
	endpoint, err := netip.ParseAddrPort(address.String())
	if err != nil {
		return netip.AddrPort{}, err
	}
	return netip.AddrPortFrom(endpoint.Addr().Unmap(), endpoint.Port()), nil
}
