package app

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ssfun/vless2surge/internal/domain"
)

const nodeTestTimeout = 15 * time.Second

type nodeTestTarget struct {
	Host       string
	Port       uint16
	Path       string
	ServerName string
	RootCAs    *x509.CertPool
}

var defaultNodeTestTarget = nodeTestTarget{
	Host:       "www.gstatic.com",
	Port:       443,
	Path:       "/generate_204",
	ServerName: "www.gstatic.com",
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

	testedAt := time.Now().UTC()
	result := domain.NodeTestResult{
		NodeID:   selected.NodeID,
		Name:     selected.DisplayName,
		Stage:    "starting",
		Target:   "https://" + defaultNodeTestTarget.Host + defaultNodeTestTarget.Path,
		TestedAt: testedAt,
	}
	testContext, cancel := context.WithTimeout(ctx, nodeTestTimeout)
	defer cancel()
	started := time.Now()
	statusCode, err := testNodeThroughSOCKS(testContext, status.Inbound, selected.AuthUser, selected.Password, defaultNodeTestTarget)
	result.LatencyMS = time.Since(started).Milliseconds()
	if err != nil {
		stage := "outbound"
		var staged *nodeTestError
		if errors.As(err, &staged) {
			stage = staged.stage
		}
		result.Stage = stage
		result.Detail = nodeTestFailureDetail(stage)
		a.AddEvent("warn", fmt.Sprintf("node test failed: %s stage=%s: %s", selected.DisplayName, stage, redactDiagnosticError(err.Error(), config, state)))
		return result, nil
	}
	result.Success = true
	result.Stage = "complete"
	result.Detail = fmt.Sprintf("网关认证、VLESS 出站、TLS 和 HTTP 均正常（HTTP %d）", statusCode)
	a.AddEvent("info", fmt.Sprintf("node test passed: %s latency=%dms", selected.DisplayName, result.LatencyMS))
	return result, nil
}

func nodeTestFailureDetail(stage string) string {
	switch stage {
	case "gateway":
		return "无法连接本地 SOCKS5 网关，请先检查网关状态和监听地址"
	case "authentication":
		return "节点凭据未被当前网关接受，请确认草稿已经应用"
	case "outbound":
		return "网关认证成功，但 VLESS 出站无法连接测试目标，请检查节点服务器、Reality/TLS 和网络"
	case "tls":
		return "VLESS 出站已建立连接，但到测试目标的 TLS 握手失败"
	case "http":
		return "TLS 已建立，但没有收到有效 HTTP 响应"
	default:
		return "节点测试失败"
	}
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
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	if err := socksAuthenticate(connection, username, password); err != nil {
		return 0, nodeTestStageError("authentication", err)
	}
	if err := socksConnect(connection, target.Host, target.Port); err != nil {
		return 0, nodeTestStageError("outbound", err)
	}

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
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+net.JoinHostPort(target.Host, strconv.Itoa(int(target.Port)))+target.Path, nil)
	if err != nil {
		return 0, nodeTestStageError("http", err)
	}
	request.Host = target.Host
	request.Header.Set("User-Agent", "vless2surge-node-test")
	request.Header.Set("Connection", "close")
	if err := request.Write(tlsConnection); err != nil {
		return 0, nodeTestStageError("http", err)
	}
	response, err := http.ReadResponse(bufio.NewReader(tlsConnection), request)
	if err != nil {
		return 0, nodeTestStageError("http", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	return response.StatusCode, nil
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
	if host == "" || len(host) > 255 || port == 0 {
		return errors.New("invalid node test target")
	}
	request := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
	request = append(request, host...)
	request = binary.BigEndian.AppendUint16(request, port)
	if _, err := connection.Write(request); err != nil {
		return err
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(connection, header); err != nil {
		return err
	}
	if header[0] != 0x05 {
		return errors.New("invalid SOCKS5 connect response")
	}
	if header[1] != 0x00 {
		return fmt.Errorf("SOCKS5 connect failed with reply 0x%02x", header[1])
	}
	var addressLength int
	switch header[3] {
	case 0x01:
		addressLength = net.IPv4len
	case 0x04:
		addressLength = net.IPv6len
	case 0x03:
		length := make([]byte, 1)
		if _, err := io.ReadFull(connection, length); err != nil {
			return err
		}
		addressLength = int(length[0])
	default:
		return errors.New("invalid SOCKS5 bound address type")
	}
	_, err := io.CopyN(io.Discard, connection, int64(addressLength+2))
	return err
}
