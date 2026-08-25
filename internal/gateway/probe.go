package gateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type ProbePart struct {
	Success   bool   `json:"success"`
	Stage     string `json:"stage"`
	LatencyMS int64  `json:"latency_ms"`
	Detail    string `json:"detail"`
	Target    string `json:"target"`
}

type ProbeResult struct {
	NodeID   string    `json:"node_id"`
	Name     string    `json:"name"`
	TestedAt time.Time `json:"tested_at"`
	TCP      ProbePart `json:"tcp"`
	UDP      ProbePart `json:"udp"`
}

type probeTarget struct {
	raw, scheme, host, hostHeader string
	port                          uint16
}
type stageError struct {
	stage string
	err   error
}

func (e *stageError) Error() string        { return e.err.Error() }
func (e *stageError) Unwrap() error        { return e.err }
func staged(stage string, err error) error { return &stageError{stage: stage, err: err} }

func (a *App) TestNode(ctx context.Context, id string) (ProbeResult, error) {
	entry, ok := a.Node(strings.TrimSpace(id))
	if !ok {
		return ProbeResult{}, errors.New("Node not found")
	}
	config := a.Config()
	target, err := parseProbeTarget(config.NodeTestURL)
	if err != nil {
		return ProbeResult{}, err
	}
	udpHost, udpPort, err := splitTarget(config.NodeTestUDP)
	if err != nil {
		return ProbeResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(config.NodeTestTimeout)*time.Second)
	defer cancel()
	result := ProbeResult{NodeID: id, Name: entry.DisplayName, TestedAt: time.Now().UTC(), TCP: ProbePart{Target: target.raw}, UDP: ProbePart{Target: config.NodeTestUDP}}
	type tcpResult struct {
		code    int
		elapsed int64
		err     error
	}
	type udpResult struct {
		elapsed int64
		err     error
	}
	tcpCh, udpCh := make(chan tcpResult, 1), make(chan udpResult, 1)
	address := net.JoinHostPort(config.SocksBind, strconv.Itoa(int(config.SocksPort)))
	go func() {
		started := time.Now()
		code, err := probeTCP(ctx, address, entry.Username, entry.Password, target)
		tcpCh <- tcpResult{code, time.Since(started).Milliseconds(), err}
	}()
	go func() {
		started := time.Now()
		err := probeUDP(ctx, address, entry.Username, entry.Password, udpHost, udpPort)
		udpCh <- udpResult{time.Since(started).Milliseconds(), err}
	}()
	tcp, udp := <-tcpCh, <-udpCh
	result.TCP.LatencyMS, result.UDP.LatencyMS = tcp.elapsed, udp.elapsed
	if tcp.err == nil {
		result.TCP.Success, result.TCP.Stage, result.TCP.Detail = true, "complete", fmt.Sprintf("SOCKS 认证、节点出站与 HTTP 均正常（HTTP %d）", tcp.code)
	} else {
		result.TCP.Stage, result.TCP.Detail = errorStage(tcp.err, "outbound"), tcpDetail(errorStage(tcp.err, "outbound"))
	}
	if udp.err == nil {
		result.UDP.Success, result.UDP.Stage, result.UDP.Detail = true, "complete", "SOCKS5 UDP ASSOCIATE、身份绑定、节点出站与 DNS 响应均正常"
	} else {
		result.UDP.Stage, result.UDP.Detail = errorStage(udp.err, "udp_outbound"), udpDetail(errorStage(udp.err, "udp_outbound"))
	}
	a.addEvent(map[bool]string{true: "info", false: "warn"}[result.TCP.Success && result.UDP.Success], fmt.Sprintf("节点端到端诊断 %s: TCP=%s UDP=%s", entry.DisplayName, result.TCP.Stage, result.UDP.Stage))
	return result, nil
}

func errorStage(err error, fallback string) string {
	var target *stageError
	if errors.As(err, &target) {
		return target.stage
	}
	return fallback
}
func tcpDetail(stage string) string {
	return map[string]string{"gateway": "无法连接产品 SOCKS5 listener", "authentication": "当前 Projection 凭据未通过认证", "outbound": "认证成功，但节点无法连接 Web 目标", "tls": "节点出站已建立，但 TLS 握手失败", "http": "未收到有效 HTTP 响应"}[stage]
}
func udpDetail(stage string) string {
	return map[string]string{"gateway": "无法连接产品 SOCKS5 listener", "authentication": "当前 Projection 凭据未通过认证", "udp_relay": "无法建立已认证 UDP ASSOCIATE", "udp_outbound": "UDP 身份已绑定，但未收到节点出站响应", "dns": "收到 UDP 数据但 DNS 响应无效"}[stage]
}

func parseProbeTarget(raw string) (probeTarget, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return probeTarget{}, errors.New("test target must be an absolute HTTP or HTTPS URL")
	}
	port := 80
	if u.Scheme == "https" {
		port = 443
	}
	if u.Port() != "" {
		port, err = strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return probeTarget{}, errors.New("invalid test target port")
		}
	}
	return probeTarget{raw: u.String(), scheme: u.Scheme, host: u.Hostname(), hostHeader: u.Host, port: uint16(port)}, nil
}
func splitTarget(raw string) (string, uint16, error) {
	host, text, err := net.SplitHostPort(raw)
	if err != nil || host == "" {
		return "", 0, errors.New("target must include host and port")
	}
	port, err := strconv.Atoi(text)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, errors.New("invalid target port")
	}
	return host, uint16(port), nil
}

func probeTCP(ctx context.Context, gateway, user, pass string, target probeTarget) (int, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", gateway)
	if err != nil {
		return 0, staged("gateway", err)
	}
	defer conn.Close()
	setDeadline(conn, ctx)
	if err := authenticate(conn, user, pass); err != nil {
		return 0, staged("authentication", err)
	}
	if err := connectSOCKS(conn, target.host, target.port); err != nil {
		return 0, staged("outbound", err)
	}
	var stream io.ReadWriter = conn
	if target.scheme == "https" {
		tlsConn := tls.Client(conn, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: target.host})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return 0, staged("tls", err)
		}
		stream = tlsConn
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, target.raw, nil)
	request.Host = target.hostHeader
	request.Header.Set("User-Agent", "vless2surge-diagnostic")
	request.Header.Set("Connection", "close")
	if err := request.Write(stream); err != nil {
		return 0, staged("http", err)
	}
	response, err := http.ReadResponse(bufio.NewReader(stream), request)
	if err != nil {
		return 0, staged("http", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	return response.StatusCode, nil
}

func probeUDP(ctx context.Context, gateway, user, pass, targetHost string, targetPort uint16) error {
	control, err := (&net.Dialer{}).DialContext(ctx, "tcp", gateway)
	if err != nil {
		return staged("gateway", err)
	}
	defer control.Close()
	setDeadline(control, ctx)
	if err := authenticate(control, user, pass); err != nil {
		return staged("authentication", err)
	}
	if _, err := control.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return staged("udp_relay", err)
	}
	relayHost, relayPort, err := readReply(control)
	if err != nil {
		return staged("udp_relay", err)
	}
	if ip := net.ParseIP(relayHost); ip != nil && ip.IsUnspecified() {
		relayHost, _, _ = net.SplitHostPort(gateway)
	}
	relay, err := (&net.Dialer{}).DialContext(ctx, "udp", net.JoinHostPort(relayHost, strconv.Itoa(int(relayPort))))
	if err != nil {
		return staged("udp_relay", err)
	}
	defer relay.Close()
	setDeadline(relay, ctx)
	query, transaction, err := dnsQuery("example.com")
	if err != nil {
		return staged("dns", err)
	}
	packet, err := appendAddress([]byte{0, 0, 0}, targetHost, targetPort)
	if err != nil {
		return staged("udp_relay", err)
	}
	packet = append(packet, query...)
	if _, err := relay.Write(packet); err != nil {
		return staged("udp_relay", err)
	}
	buffer := make([]byte, 4096)
	count, err := relay.Read(buffer)
	if err != nil {
		return staged("udp_outbound", err)
	}
	payload, err := udpPayload(buffer[:count])
	if err != nil {
		return staged("udp_relay", err)
	}
	if err := validDNS(payload, transaction); err != nil {
		return staged("dns", err)
	}
	return nil
}

func setDeadline(conn net.Conn, ctx context.Context) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
}
func authenticate(conn net.Conn, user, pass string) error {
	if len(user) < 1 || len(user) > 255 || len(pass) < 1 || len(pass) > 255 {
		return errors.New("invalid credential length")
	}
	if _, err := conn.Write([]byte{5, 1, 2}); err != nil {
		return err
	}
	response := make([]byte, 2)
	if _, err := io.ReadFull(conn, response); err != nil {
		return err
	}
	if response[1] != 2 {
		return errors.New("username authentication unavailable")
	}
	payload := []byte{1, byte(len(user))}
	payload = append(payload, user...)
	payload = append(payload, byte(len(pass)))
	payload = append(payload, pass...)
	if _, err := conn.Write(payload); err != nil {
		return err
	}
	if _, err := io.ReadFull(conn, response); err != nil {
		return err
	}
	if response[1] != 0 {
		return errors.New("authentication rejected")
	}
	return nil
}
func connectSOCKS(conn net.Conn, host string, port uint16) error {
	request, err := appendAddress([]byte{5, 1, 0}, host, port)
	if err != nil {
		return err
	}
	if _, err = conn.Write(request); err != nil {
		return err
	}
	_, _, err = readReply(conn)
	return err
}
func readReply(reader io.Reader) (string, uint16, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil {
		return "", 0, err
	}
	if header[0] != 5 || header[1] != 0 {
		return "", 0, fmt.Errorf("SOCKS reply %d", header[1])
	}
	return readAddress(reader, header[3])
}
func readAddress(reader io.Reader, kind byte) (string, uint16, error) {
	size := 0
	switch kind {
	case 1:
		size = 4
	case 4:
		size = 16
	case 3:
		length := []byte{0}
		if _, err := io.ReadFull(reader, length); err != nil {
			return "", 0, err
		}
		size = int(length[0])
	default:
		return "", 0, errors.New("invalid SOCKS address")
	}
	address := make([]byte, size)
	port := make([]byte, 2)
	if _, err := io.ReadFull(reader, address); err != nil {
		return "", 0, err
	}
	if _, err := io.ReadFull(reader, port); err != nil {
		return "", 0, err
	}
	host := string(address)
	if kind != 3 {
		host = net.IP(address).String()
	}
	return host, binary.BigEndian.Uint16(port), nil
}
func appendAddress(buffer []byte, host string, port uint16) ([]byte, error) {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" || port == 0 {
		return nil, errors.New("invalid SOCKS target")
	}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			buffer = append(buffer, 1)
			buffer = append(buffer, v4...)
		} else {
			buffer = append(buffer, 4)
			buffer = append(buffer, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return nil, errors.New("hostname too long")
		}
		buffer = append(buffer, 3, byte(len(host)))
		buffer = append(buffer, host...)
	}
	return binary.BigEndian.AppendUint16(buffer, port), nil
}
func udpPayload(packet []byte) ([]byte, error) {
	if len(packet) < 4 || packet[0] != 0 || packet[1] != 0 || packet[2] != 0 {
		return nil, errors.New("invalid fragmented UDP packet")
	}
	reader := bytes.NewReader(packet[4:])
	if _, _, err := readAddress(reader, packet[3]); err != nil {
		return nil, err
	}
	return io.ReadAll(reader)
}
func dnsQuery(name string) ([]byte, uint16, error) {
	transaction := make([]byte, 2)
	if _, err := rand.Read(transaction); err != nil {
		return nil, 0, err
	}
	id := binary.BigEndian.Uint16(transaction)
	query := make([]byte, 12)
	binary.BigEndian.PutUint16(query[0:2], id)
	binary.BigEndian.PutUint16(query[2:4], 0x0100)
	binary.BigEndian.PutUint16(query[4:6], 1)
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 {
			return nil, 0, errors.New("invalid DNS name")
		}
		query = append(query, byte(len(label)))
		query = append(query, label...)
	}
	query = append(query, 0, 0, 1, 0, 1)
	return query, id, nil
}
func validDNS(response []byte, id uint16) error {
	if len(response) < 12 {
		return errors.New("short DNS response")
	}
	if binary.BigEndian.Uint16(response[:2]) != id {
		return errors.New("DNS transaction mismatch")
	}
	flags := binary.BigEndian.Uint16(response[2:4])
	if flags&0x8000 == 0 {
		return errors.New("not a DNS response")
	}
	if flags&15 != 0 {
		return fmt.Errorf("DNS rcode %d", flags&15)
	}
	return nil
}
