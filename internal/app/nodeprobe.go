package app

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
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

	"github.com/ssfun/vless2surge/internal/domain"
)

type nodeTestTarget struct {
	RawURL     string
	Scheme     string
	Host       string
	HostHeader string
	Port       uint16
	ServerName string
	RootCAs    *x509.CertPool
}

type nodeTestError struct {
	stage string
	err   error
}

func (e *nodeTestError) Error() string { return e.err.Error() }
func (e *nodeTestError) Unwrap() error { return e.err }

func nodeTestStageError(stage string, err error) error {
	return &nodeTestError{stage: stage, err: err}
}

func (a *App) TestNode(ctx context.Context, nodeID string) (domain.NodeTestResult, error) {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return domain.NodeTestResult{}, errors.New("node ID is required")
	}
	status := a.engine.Status()
	a.mu.RLock()
	state := clone(a.state)
	config := clone(a.config)
	a.mu.RUnlock()
	if status.State != "running" || state.Applied == nil {
		return domain.NodeTestResult{}, errors.New("网关未运行，无法测试节点")
	}
	var selected *domain.RuntimeNode
	for index := range state.Applied.Nodes {
		if state.Applied.Nodes[index].NodeID == nodeID {
			node := state.Applied.Nodes[index]
			selected = &node
			break
		}
	}
	if selected == nil {
		return domain.NodeTestResult{}, errors.New("该节点尚未应用，无法通过当前网关测试")
	}

	tcpTarget, err := parseNodeTestTarget(config.NodeTestURL)
	if err != nil {
		return domain.NodeTestResult{}, fmt.Errorf("invalid saved node test URL: %w", err)
	}
	udpHost, udpPort, err := parseNodeTestUDPAddress(config.NodeTestUDPAddress)
	if err != nil {
		return domain.NodeTestResult{}, fmt.Errorf("invalid saved UDP test address: %w", err)
	}
	result := domain.NodeTestResult{
		NodeID:   selected.NodeID,
		Name:     selected.DisplayName,
		Stage:    "starting",
		Target:   tcpTarget.RawURL,
		TestedAt: time.Now().UTC(),
		UDP: domain.NodeUDPTestResult{
			Stage:  "starting",
			Target: config.NodeTestUDPAddress,
		},
	}

	testContext, cancel := context.WithTimeout(ctx, time.Duration(config.NodeTestTimeoutSeconds)*time.Second)
	defer cancel()
	type tcpOutcome struct {
		statusCode int
		latency    int64
		err        error
	}
	type udpOutcome struct {
		latency int64
		err     error
	}
	tcpResults := make(chan tcpOutcome, 1)
	udpResults := make(chan udpOutcome, 1)
	go func() {
		started := time.Now()
		statusCode, testErr := testNodeThroughSOCKS(testContext, status.Inbound, selected.AuthUser, selected.Password, tcpTarget)
		tcpResults <- tcpOutcome{statusCode: statusCode, latency: time.Since(started).Milliseconds(), err: testErr}
	}()
	go func() {
		started := time.Now()
		testErr := testNodeUDPThroughSOCKS(testContext, status.Inbound, selected.AuthUser, selected.Password, udpHost, udpPort)
		udpResults <- udpOutcome{latency: time.Since(started).Milliseconds(), err: testErr}
	}()
	tcpResult, udpResult := <-tcpResults, <-udpResults

	result.LatencyMS = tcpResult.latency
	if tcpResult.err != nil {
		result.Stage = nodeTestErrorStage(tcpResult.err, "outbound")
		result.Detail = nodeTestFailureDetail(result.Stage, tcpTarget.Scheme)
	} else {
		result.Success = true
		result.Stage = "complete"
		if tcpTarget.Scheme == "https" {
			result.Detail = fmt.Sprintf("SOCKS 认证、VLESS 出站、TLS 和 HTTP 均正常（HTTP %d）", tcpResult.statusCode)
		} else {
			result.Detail = fmt.Sprintf("SOCKS 认证、VLESS 出站和 HTTP 均正常（HTTP %d）", tcpResult.statusCode)
		}
	}

	result.UDP.LatencyMS = udpResult.latency
	if udpResult.err != nil {
		result.UDP.Stage = nodeTestErrorStage(udpResult.err, "udp_outbound")
		result.UDP.Detail = nodeUDPFailureDetail(result.UDP.Stage)
	} else {
		result.UDP.Success = true
		result.UDP.Stage = "complete"
		result.UDP.Detail = "SOCKS5 UDP Relay、VLESS UDP 出站和 DNS 响应均正常"
	}

	level, outcome := "warn", "partial"
	if result.Success && result.UDP.Success {
		level, outcome = "info", "passed"
	} else if !result.Success && !result.UDP.Success {
		outcome = "failed"
	}
	diagnostic := fmt.Sprintf(
		"node test %s: %s tcp_stage=%s tcp=%dms udp_stage=%s udp=%dms tcp_error=%v udp_error=%v",
		outcome, selected.DisplayName, result.Stage, result.LatencyMS, result.UDP.Stage, result.UDP.LatencyMS, tcpResult.err, udpResult.err,
	)
	a.AddEvent(level, redactDiagnosticError(diagnostic, config, state))
	return result, nil
}

func nodeTestErrorStage(err error, fallback string) string {
	var staged *nodeTestError
	if errors.As(err, &staged) {
		return staged.stage
	}
	return fallback
}

func nodeTestFailureDetail(stage, scheme string) string {
	switch stage {
	case "gateway":
		return "无法连接本地 SOCKS5 网关，请先检查网关状态和监听地址"
	case "authentication":
		return "节点凭据未被当前网关接受，请确认草稿已经应用"
	case "outbound":
		return "网关认证成功，但 VLESS 出站无法连接 Web 测试目标，请检查节点服务器和网络"
	case "tls":
		return "VLESS 出站已建立连接，但到 Web 测试目标的 TLS 握手失败"
	case "http":
		if scheme == "https" {
			return "TLS 已建立，但没有收到有效 HTTP 响应"
		}
		return "VLESS 出站已建立连接，但没有收到有效 HTTP 响应"
	default:
		return "TCP 测试失败"
	}
}

func nodeUDPFailureDetail(stage string) string {
	switch stage {
	case "gateway":
		return "无法连接本地 SOCKS5 网关"
	case "authentication":
		return "节点凭据未被当前网关接受"
	case "udp_relay":
		return "本地 SOCKS5 网关未能建立 UDP Relay"
	case "udp_outbound":
		return "UDP Relay 已建立，但没有收到 DNS 测试响应；节点可能不支持 UDP/XUDP 或目标不可达"
	case "dns":
		return "收到 UDP 数据，但 DNS 响应无效"
	default:
		return "UDP 测试失败"
	}
}

func parseNodeTestTarget(raw string) (nodeTestTarget, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nodeTestTarget{}, errors.New("test target must be an absolute HTTP or HTTPS URL")
	}
	port := uint16(80)
	if parsed.Scheme == "https" {
		port = 443
	}
	if parsed.Port() != "" {
		value, err := strconv.Atoi(parsed.Port())
		if err != nil || value < 1 || value > 65535 {
			return nodeTestTarget{}, errors.New("test target port is invalid")
		}
		port = uint16(value)
	}
	return nodeTestTarget{
		RawURL:     parsed.String(),
		Scheme:     parsed.Scheme,
		Host:       parsed.Hostname(),
		HostHeader: parsed.Host,
		Port:       port,
		ServerName: parsed.Hostname(),
	}, nil
}

func parseNodeTestUDPAddress(raw string) (string, uint16, error) {
	host, portText, err := net.SplitHostPort(strings.TrimSpace(raw))
	if err != nil || strings.TrimSpace(host) == "" {
		return "", 0, errors.New("UDP test address must include a host and port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, errors.New("UDP test port is invalid")
	}
	return host, uint16(port), nil
}

func testNodeThroughSOCKS(ctx context.Context, inbound, username, password string, target nodeTestTarget) (int, error) {
	address, err := diagnosticDialAddress(inbound)
	if err != nil {
		return 0, nodeTestStageError("gateway", err)
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return 0, nodeTestStageError("gateway", err)
	}
	defer connection.Close()
	setConnectionDeadline(connection, ctx)
	if err := socksAuthenticate(connection, username, password); err != nil {
		return 0, nodeTestStageError("authentication", err)
	}
	if err := socksConnect(connection, target.Host, target.Port); err != nil {
		return 0, nodeTestStageError("outbound", err)
	}

	stream := io.ReadWriter(connection)
	if target.Scheme == "https" {
		serverName := target.ServerName
		if serverName == "" {
			serverName = target.Host
		}
		tlsConnection := tls.Client(connection, &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: serverName,
			RootCAs:    target.RootCAs,
		})
		if err := tlsConnection.HandshakeContext(ctx); err != nil {
			return 0, nodeTestStageError("tls", err)
		}
		stream = tlsConnection
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.RawURL, nil)
	if err != nil {
		return 0, nodeTestStageError("http", err)
	}
	request.Host = target.HostHeader
	request.Header.Set("User-Agent", "vless2surge-node-test")
	request.Header.Set("Connection", "close")
	if err := request.Write(stream); err != nil {
		return 0, nodeTestStageError("http", err)
	}
	response, err := http.ReadResponse(bufio.NewReader(stream), request)
	if err != nil {
		return 0, nodeTestStageError("http", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	return response.StatusCode, nil
}

func testNodeUDPThroughSOCKS(ctx context.Context, inbound, username, password, targetHost string, targetPort uint16) error {
	address, err := diagnosticDialAddress(inbound)
	if err != nil {
		return nodeTestStageError("gateway", err)
	}
	control, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nodeTestStageError("gateway", err)
	}
	defer control.Close()
	setConnectionDeadline(control, ctx)
	if err := socksAuthenticate(control, username, password); err != nil {
		return nodeTestStageError("authentication", err)
	}
	if _, err := control.Write([]byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return nodeTestStageError("udp_relay", err)
	}
	relayHost, relayPort, err := readSOCKSReply(control)
	if err != nil {
		return nodeTestStageError("udp_relay", err)
	}
	if net.ParseIP(relayHost) != nil && net.ParseIP(relayHost).IsUnspecified() {
		relayHost, _, err = net.SplitHostPort(address)
		if err != nil {
			return nodeTestStageError("udp_relay", err)
		}
	}
	relay, err := (&net.Dialer{}).DialContext(ctx, "udp", net.JoinHostPort(relayHost, strconv.Itoa(int(relayPort))))
	if err != nil {
		return nodeTestStageError("udp_relay", err)
	}
	defer relay.Close()
	setConnectionDeadline(relay, ctx)
	dnsQuery, transactionID, err := buildDNSQuery("example.com")
	if err != nil {
		return nodeTestStageError("dns", err)
	}
	packet := []byte{0, 0, 0}
	packet, err = appendSOCKSAddress(packet, targetHost, targetPort)
	if err != nil {
		return nodeTestStageError("udp_relay", err)
	}
	packet = append(packet, dnsQuery...)
	if _, err := relay.Write(packet); err != nil {
		return nodeTestStageError("udp_relay", err)
	}
	buffer := make([]byte, 4096)
	count, err := relay.Read(buffer)
	if err != nil {
		return nodeTestStageError("udp_outbound", err)
	}
	payload, err := parseSOCKSUDPPacket(buffer[:count])
	if err != nil {
		return nodeTestStageError("udp_relay", err)
	}
	if err := validateDNSResponse(payload, transactionID); err != nil {
		return nodeTestStageError("dns", err)
	}
	return nil
}

func setConnectionDeadline(connection net.Conn, ctx context.Context) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
}

func socksAuthenticate(connection net.Conn, username, password string) error {
	if len(username) == 0 || len(username) > 255 || len(password) == 0 || len(password) > 255 {
		return errors.New("invalid SOCKS5 credential length")
	}
	if _, err := connection.Write([]byte{0x05, 0x01, 0x02}); err != nil {
		return err
	}
	response := make([]byte, 2)
	if _, err := io.ReadFull(connection, response); err != nil {
		return err
	}
	if response[0] != 0x05 || response[1] != 0x02 {
		return errors.New("SOCKS5 username/password authentication is unavailable")
	}
	payload := []byte{0x01, byte(len(username))}
	payload = append(payload, username...)
	payload = append(payload, byte(len(password)))
	payload = append(payload, password...)
	if _, err := connection.Write(payload); err != nil {
		return err
	}
	if _, err := io.ReadFull(connection, response); err != nil {
		return err
	}
	if response[0] != 0x01 || response[1] != 0x00 {
		return errors.New("SOCKS5 authentication was rejected")
	}
	return nil
}

func socksConnect(connection net.Conn, host string, port uint16) error {
	request := []byte{0x05, 0x01, 0x00}
	var err error
	request, err = appendSOCKSAddress(request, host, port)
	if err != nil {
		return err
	}
	if _, err := connection.Write(request); err != nil {
		return err
	}
	_, _, err = readSOCKSReply(connection)
	return err
}

func readSOCKSReply(reader io.Reader) (string, uint16, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil {
		return "", 0, err
	}
	if header[0] != 0x05 {
		return "", 0, errors.New("invalid SOCKS5 response version")
	}
	if header[1] != 0x00 {
		return "", 0, fmt.Errorf("SOCKS5 command failed with reply 0x%02x", header[1])
	}
	return readSOCKSAddress(reader, header[3])
}

func readSOCKSAddress(reader io.Reader, addressType byte) (string, uint16, error) {
	var size int
	switch addressType {
	case 0x01:
		size = net.IPv4len
	case 0x04:
		size = net.IPv6len
	case 0x03:
		length := []byte{0}
		if _, err := io.ReadFull(reader, length); err != nil {
			return "", 0, err
		}
		size = int(length[0])
	default:
		return "", 0, fmt.Errorf("unknown SOCKS address type %d", addressType)
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
	if addressType == 0x01 || addressType == 0x04 {
		host = net.IP(address).String()
	}
	return host, binary.BigEndian.Uint16(port), nil
}

func appendSOCKSAddress(buffer []byte, host string, port uint16) ([]byte, error) {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" || port == 0 {
		return nil, errors.New("invalid SOCKS target")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ipv4 := ip.To4(); ipv4 != nil {
			buffer = append(buffer, 0x01)
			buffer = append(buffer, ipv4...)
		} else {
			buffer = append(buffer, 0x04)
			buffer = append(buffer, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return nil, errors.New("SOCKS target hostname is too long")
		}
		buffer = append(buffer, 0x03, byte(len(host)))
		buffer = append(buffer, host...)
	}
	return binary.BigEndian.AppendUint16(buffer, port), nil
}

func parseSOCKSUDPPacket(packet []byte) ([]byte, error) {
	if len(packet) < 4 || packet[0] != 0 || packet[1] != 0 || packet[2] != 0 {
		return nil, errors.New("invalid or fragmented SOCKS UDP response")
	}
	reader := bytes.NewReader(packet[4:])
	if _, _, err := readSOCKSAddress(reader, packet[3]); err != nil {
		return nil, err
	}
	return io.ReadAll(reader)
}

func buildDNSQuery(name string) ([]byte, uint16, error) {
	transaction := make([]byte, 2)
	if _, err := rand.Read(transaction); err != nil {
		return nil, 0, err
	}
	id := binary.BigEndian.Uint16(transaction)
	query := make([]byte, 12)
	binary.BigEndian.PutUint16(query[0:2], id)
	binary.BigEndian.PutUint16(query[2:4], 0x0100)
	binary.BigEndian.PutUint16(query[4:6], 1)
	for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		if label == "" || len(label) > 63 {
			return nil, 0, errors.New("invalid DNS query name")
		}
		query = append(query, byte(len(label)))
		query = append(query, label...)
	}
	query = append(query, 0, 0, 1, 0, 1)
	return query, id, nil
}

func validateDNSResponse(response []byte, transactionID uint16) error {
	if len(response) < 12 {
		return errors.New("DNS response is too short")
	}
	if binary.BigEndian.Uint16(response[0:2]) != transactionID {
		return errors.New("DNS transaction ID does not match")
	}
	flags := binary.BigEndian.Uint16(response[2:4])
	if flags&0x8000 == 0 {
		return errors.New("DNS response flag is missing")
	}
	if flags&0x000f != 0 {
		return fmt.Errorf("DNS server returned rcode %d", flags&0x000f)
	}
	return nil
}
