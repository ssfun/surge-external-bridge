package core

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ssfun/vless2surge/internal/domain"
	"golang.org/x/net/proxy"
)

func TestCompileCurrentSchemaAndRedaction(t *testing.T) {
	config := domain.DefaultConfig()
	config.SocksPort = freePort(t)
	revision := &domain.Revision{ID: "draft", Nodes: []domain.RuntimeNode{{
		Node:   domain.Node{Type: "vless", Name: "Reality", Server: "example.com", Port: 443, UUID: "11111111-1111-4111-8111-111111111111", Network: "tcp", Security: "reality", ServerName: "www.example.com", Fingerprint: "chrome", ALPN: []string{"h2", "http/1.1"}, RealityPublicKey: "public-key", RealityShortID: "abcd"},
		NodeID: "n_test", DisplayName: "Reality", AuthUser: "user-a", Password: "secret-password", OutboundTag: "vless-n_test",
	}}}
	compiled, err := NewCompiler().Compile(config, revision)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(compiled), `"action": "reject"`) || !strings.Contains(string(compiled), `"auth_user"`) {
		t.Fatalf("compiled route is incomplete: %s", compiled)
	}
	if !strings.Contains(string(compiled), `"alpn": [`) || !strings.Contains(string(compiled), `"http/1.1"`) {
		t.Fatalf("compiled TLS configuration omitted ALPN: %s", compiled)
	}
	redacted := string(Redacted(compiled))
	if strings.Contains(redacted, "11111111") || strings.Contains(redacted, "secret-password") || strings.Contains(redacted, "public-key") {
		t.Fatalf("redacted config leaks secrets: %s", redacted)
	}
}

func TestServerNameDoesNotImplicitlyEnableTLS(t *testing.T) {
	outbound, err := compileVLESS(domain.RuntimeNode{Node: domain.Node{
		Type: "vless", Server: "example.com", Port: 443, UUID: "11111111-1111-4111-8111-111111111111", Security: "none", ServerName: "ignored.example.com",
	}, OutboundTag: "vless-test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := outbound["tls"]; exists {
		t.Fatalf("SNI implicitly enabled TLS despite security=none: %+v", outbound)
	}
}

func TestAuthUserRoutesToDistinctVLESSOutbound(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer target.Close()
	serverPort := freePort(t)
	uuidB := "22222222-2222-4222-8222-222222222222"
	serverConfig := []byte(fmt.Sprintf(`{
  "log":{"disabled":true},
  "inbounds":[{"type":"vless","tag":"server","listen":"127.0.0.1","listen_port":%d,"users":[{"uuid":%q}]}],
  "outbounds":[{"type":"direct","tag":"direct"}],
  "route":{"final":"direct"}
}`, serverPort, uuidB))
	vlessServer, cancelServer, err := createAndStart(serverConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancelServer(); _ = vlessServer.Close() }()

	deadPort := freePort(t)
	gatewayPort := freePort(t)
	config := domain.DefaultConfig()
	config.SocksPort = gatewayPort
	rev := &domain.Revision{ID: "route-test", Nodes: []domain.RuntimeNode{
		{Node: domain.Node{Type: "vless", Name: "dead", Server: "127.0.0.1", Port: deadPort, UUID: "11111111-1111-4111-8111-111111111111", Network: "tcp"}, NodeID: "dead", DisplayName: "dead", AuthUser: "dead-user", Password: "dead-pass", OutboundTag: "vless-dead"},
		{Node: domain.Node{Type: "vless", Name: "live", Server: "127.0.0.1", Port: uint16(serverPort), UUID: uuidB, Network: "tcp"}, NodeID: "live", DisplayName: "live", AuthUser: "live-user", Password: "live-pass", OutboundTag: "vless-live"},
	}}
	compiled, err := NewCompiler().Compile(config, rev)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(nil)
	if err := manager.Start(rev, compiled, fmt.Sprintf("127.0.0.1:%d", gatewayPort)); err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()

	if body, err := getThroughSOCKS(target.URL, gatewayPort, "live-user", "live-pass"); err != nil || body != "ok" {
		t.Fatalf("live route failed: body=%q err=%v", body, err)
	}
	if _, err := getThroughSOCKS(target.URL, gatewayPort, "dead-user", "dead-pass"); err == nil {
		t.Fatal("dead user unexpectedly used the live outbound")
	}
	if _, err := getThroughSOCKS(target.URL, gatewayPort, "unknown", "wrong"); err == nil {
		t.Fatal("unknown SOCKS user was accepted")
	}
}

func TestFailedApplyRollsBackToRunningPreviousRevision(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "rollback-ok") }))
	defer target.Close()
	serverPort := freePort(t)
	uuid := "33333333-3333-4333-8333-333333333333"
	serverConfig := []byte(fmt.Sprintf(`{
  "log":{"disabled":true},
  "inbounds":[{"type":"vless","tag":"server","listen":"127.0.0.1","listen_port":%d,"users":[{"uuid":%q}]}],
  "outbounds":[{"type":"direct","tag":"direct"}],
  "route":{"final":"direct"}
}`, serverPort, uuid))
	vlessServer, cancelServer, err := createAndStart(serverConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancelServer(); _ = vlessServer.Close() }()

	goodPort := freePort(t)
	goodConfig := domain.DefaultConfig()
	goodConfig.SocksPort = goodPort
	previous := &domain.Revision{ID: "previous", SocksBind: "127.0.0.1", SocksPort: goodPort, SocksAdvertise: "127.0.0.1", Nodes: []domain.RuntimeNode{{
		Node: domain.Node{Type: "vless", Name: "live", Server: "127.0.0.1", Port: serverPort, UUID: uuid, Network: "tcp"}, NodeID: "live", DisplayName: "live", AuthUser: "live-user", Password: "live-pass", OutboundTag: "vless-live",
	}}}
	compiledPrevious, err := NewCompiler().Compile(goodConfig, previous)
	if err != nil {
		t.Fatal(err)
	}
	previousID := previous.ID
	manager := NewManager(nil)
	if err := manager.Start(previous, compiledPrevious, fmt.Sprintf("127.0.0.1:%d", goodPort)); err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	blockedPort := uint16(occupied.Addr().(*net.TCPAddr).Port)
	candidate := *previous
	candidate.ID = "candidate"
	candidate.SocksPort = blockedPort
	candidateConfig := goodConfig
	candidateConfig.SocksPort = blockedPort
	compiledCandidate, err := NewCompiler().Compile(candidateConfig, &candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Apply(&candidate, compiledCandidate, fmt.Sprintf("127.0.0.1:%d", blockedPort)); err == nil {
		t.Fatal("candidate unexpectedly bound an occupied port")
	}
	status := manager.Status()
	if status.State != "running" || status.Revision != previousID || status.LastError == "" {
		t.Fatalf("successful rollback was not reported as running: %+v", status)
	}
	if body, err := getThroughSOCKS(target.URL, goodPort, "live-user", "live-pass"); err != nil || body != "rollback-ok" {
		t.Fatalf("previous route did not survive failed apply: body=%q err=%v", body, err)
	}
}

func TestAuthUserRoutesSOCKSUDPThroughVLESS(t *testing.T) {
	echo, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	echoDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, 2048)
		count, peer, err := echo.ReadFromUDP(buffer)
		if err != nil {
			echoDone <- err
			return
		}
		_, err = echo.WriteToUDP(append([]byte("echo:"), buffer[:count]...), peer)
		echoDone <- err
	}()

	serverPort := freePort(t)
	uuid := "44444444-4444-4444-8444-444444444444"
	serverConfig := []byte(fmt.Sprintf(`{
  "log":{"disabled":true},
  "inbounds":[{"type":"vless","tag":"server","listen":"127.0.0.1","listen_port":%d,"users":[{"uuid":%q}]}],
  "outbounds":[{"type":"direct","tag":"direct"}],
  "route":{"final":"direct"}
}`, serverPort, uuid))
	vlessServer, cancelServer, err := createAndStart(serverConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancelServer(); _ = vlessServer.Close() }()

	gatewayPort := freePort(t)
	config := domain.DefaultConfig()
	config.SocksPort = gatewayPort
	revision := &domain.Revision{ID: "udp-route", SocksBind: "127.0.0.1", SocksPort: gatewayPort, SocksAdvertise: "127.0.0.1", Nodes: []domain.RuntimeNode{{
		Node: domain.Node{Type: "vless", Name: "udp", Server: "127.0.0.1", Port: serverPort, UUID: uuid, Network: "tcp", PacketEncoding: "xudp"}, NodeID: "udp", DisplayName: "udp", AuthUser: "udp-user", Password: "udp-pass", OutboundTag: "vless-udp",
	}}}
	compiled, err := NewCompiler().Compile(config, revision)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(nil)
	if err := manager.Start(revision, compiled, fmt.Sprintf("127.0.0.1:%d", gatewayPort)); err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()

	response, err := udpThroughSOCKS(gatewayPort, "udp-user", "udp-pass", echo.LocalAddr().(*net.UDPAddr), []byte("hello"))
	if err != nil || string(response) != "echo:hello" {
		t.Fatalf("UDP route failed: response=%q err=%v", response, err)
	}
	if err := <-echoDone; err != nil {
		t.Fatal(err)
	}
	if _, err := udpThroughSOCKS(gatewayPort, "unknown", "wrong", echo.LocalAddr().(*net.UDPAddr), []byte("blocked")); err == nil {
		t.Fatal("unknown SOCKS credentials established a UDP relay")
	}
}

func udpThroughSOCKS(port uint16, username, password string, target *net.UDPAddr, payload []byte) ([]byte, error) {
	control, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 3*time.Second)
	if err != nil {
		return nil, err
	}
	defer control.Close()
	_ = control.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := control.Write([]byte{5, 1, 2}); err != nil {
		return nil, err
	}
	response := make([]byte, 2)
	if _, err := io.ReadFull(control, response); err != nil || response[0] != 5 || response[1] != 2 {
		return nil, fmt.Errorf("SOCKS authentication method rejected: response=%v err=%v", response, err)
	}
	auth := []byte{1, byte(len(username))}
	auth = append(auth, username...)
	auth = append(auth, byte(len(password)))
	auth = append(auth, password...)
	if _, err := control.Write(auth); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(control, response); err != nil || response[1] != 0 {
		return nil, fmt.Errorf("SOCKS credentials rejected: response=%v err=%v", response, err)
	}
	if _, err := control.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return nil, err
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(control, header); err != nil || header[0] != 5 || header[1] != 0 {
		return nil, fmt.Errorf("UDP associate failed: response=%v err=%v", header, err)
	}
	relayHost, relayPort, err := readSOCKSAddress(control, header[3])
	if err != nil {
		return nil, err
	}
	if relayHost == "0.0.0.0" || relayHost == "::" {
		relayHost = "127.0.0.1"
	}
	relay, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP(relayHost), Port: relayPort})
	if err != nil {
		return nil, err
	}
	defer relay.Close()
	_ = relay.SetDeadline(time.Now().Add(5 * time.Second))
	targetIP := target.IP.To4()
	if targetIP == nil {
		return nil, errors.New("test target must be IPv4")
	}
	packet := []byte{0, 0, 0, 1}
	packet = append(packet, targetIP...)
	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, uint16(target.Port))
	packet = append(packet, portBytes...)
	packet = append(packet, payload...)
	if _, err := relay.Write(packet); err != nil {
		return nil, err
	}
	buffer := make([]byte, 2048)
	count, err := relay.Read(buffer)
	if err != nil {
		return nil, err
	}
	if count < 4 || buffer[2] != 0 {
		return nil, fmt.Errorf("invalid SOCKS UDP response: %v", buffer[:count])
	}
	reader := bytes.NewReader(buffer[4:count])
	_, _, err = readSOCKSAddress(reader, buffer[3])
	if err != nil {
		return nil, err
	}
	return io.ReadAll(reader)
}

func readSOCKSAddress(reader io.Reader, addressType byte) (string, int, error) {
	var size int
	switch addressType {
	case 1:
		size = net.IPv4len
	case 4:
		size = net.IPv6len
	case 3:
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
	if addressType == 1 || addressType == 4 {
		host = net.IP(address).String()
	}
	return host, int(binary.BigEndian.Uint16(port)), nil
}

func getThroughSOCKS(target string, port uint16, username, password string) (string, error) {
	dialer, err := proxy.SOCKS5("tcp", fmt.Sprintf("127.0.0.1:%d", port), &proxy.Auth{User: username, Password: password}, proxy.Direct)
	if err != nil {
		return "", err
	}
	transport := &http.Transport{DialContext: func(_ context.Context, network, address string) (net.Conn, error) {
		return dialer.Dial(network, address)
	}}
	client := &http.Client{Transport: transport}
	response, err := client.Get(target)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	return string(body), err
}

func freePort(t *testing.T) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return uint16(listener.Addr().(*net.TCPAddr).Port)
}

func TestCompiledJSONIsDeterministic(t *testing.T) {
	config := domain.DefaultConfig()
	revision := &domain.Revision{Nodes: []domain.RuntimeNode{{Node: domain.Node{Type: "vless", Name: "A", Server: "127.0.0.1", Port: 443, UUID: "11111111-1111-4111-8111-111111111111", Network: "tcp"}, NodeID: "a", DisplayName: "A", AuthUser: "a", Password: "a", OutboundTag: "vless-a"}}}
	one, err := NewCompiler().Compile(config, revision)
	if err != nil {
		t.Fatal(err)
	}
	two, err := NewCompiler().Compile(config, revision)
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	_ = json.Unmarshal(one, &a)
	_ = json.Unmarshal(two, &b)
	if Compact(one) != Compact(two) {
		t.Fatal("compiled JSON changed for identical input")
	}
}

func TestCompileSupportedVLESSTransports(t *testing.T) {
	cases := []struct {
		name string
		node domain.Node
	}{
		{name: "tcp-tls", node: domain.Node{Type: "vless", Server: "example.com", Port: 443, UUID: "11111111-1111-4111-8111-111111111111", Network: "tcp", Security: "tls", ServerName: "example.com", Fingerprint: "chrome"}},
		{name: "websocket", node: domain.Node{Type: "vless", Server: "example.com", Port: 443, UUID: "11111111-1111-4111-8111-111111111111", Network: "ws", Security: "tls", ServerName: "example.com", Path: "/ws", Host: "cdn.example.com"}},
		{name: "grpc", node: domain.Node{Type: "vless", Server: "example.com", Port: 443, UUID: "11111111-1111-4111-8111-111111111111", Network: "grpc", Security: "tls", ServerName: "example.com", ServiceName: "gateway"}},
		{name: "http", node: domain.Node{Type: "vless", Server: "example.com", Port: 443, UUID: "11111111-1111-4111-8111-111111111111", Network: "http", Security: "tls", ServerName: "example.com", Path: "/h2", Host: "cdn.example.com"}},
		{name: "httpupgrade", node: domain.Node{Type: "vless", Server: "example.com", Port: 443, UUID: "11111111-1111-4111-8111-111111111111", Network: "httpupgrade", Security: "tls", ServerName: "example.com", Path: "/upgrade", Host: "cdn.example.com"}},
		{name: "reality-vision", node: domain.Node{Type: "vless", Server: "example.com", Port: 443, UUID: "11111111-1111-4111-8111-111111111111", Network: "tcp", Security: "reality", ServerName: "www.example.com", Flow: "xtls-rprx-vision", Fingerprint: "chrome", RealityPublicKey: "public-key", RealityShortID: "abcd"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			revision := &domain.Revision{Nodes: []domain.RuntimeNode{{Node: test.node, NodeID: "node", DisplayName: test.name, AuthUser: "user", Password: "password", OutboundTag: "vless-node"}}}
			if _, err := NewCompiler().Compile(domain.DefaultConfig(), revision); err != nil {
				t.Fatalf("supported transport did not compile: %v", err)
			}
		})
	}
}

func TestSupportedVLESSTransportsRouteThroughEmbeddedCore(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "transport-ok") }))
	defer target.Close()
	tests := []struct {
		name      string
		node      domain.Node
		transport map[string]any
	}{
		{name: "websocket", node: domain.Node{Network: "ws", Path: "/vless-ws", Host: "example.com"}, transport: map[string]any{"type": "ws", "path": "/vless-ws"}},
		{name: "grpc", node: domain.Node{Network: "grpc", ServiceName: "vless-grpc"}, transport: map[string]any{"type": "grpc", "service_name": "vless-grpc"}},
		{name: "http", node: domain.Node{Network: "http", Path: "/vless-http", Host: "example.com"}, transport: map[string]any{"type": "http", "path": "/vless-http"}},
		{name: "http-upgrade", node: domain.Node{Network: "httpupgrade", Path: "/vless-upgrade", Host: "example.com"}, transport: map[string]any{"type": "httpupgrade", "path": "/vless-upgrade"}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			serverPort := freePort(t)
			uuid := fmt.Sprintf("81818181-8181-4181-8181-%012d", index+1)
			serverOptions := map[string]any{
				"log": map[string]any{"disabled": true},
				"inbounds": []map[string]any{{
					"type": "vless", "tag": "server", "listen": "127.0.0.1", "listen_port": serverPort,
					"users": []map[string]any{{"uuid": uuid}}, "transport": test.transport,
				}},
				"outbounds": []map[string]any{{"type": "direct", "tag": "direct"}},
				"route":     map[string]any{"final": "direct"},
			}
			serverConfig, err := json.Marshal(serverOptions)
			if err != nil {
				t.Fatal(err)
			}
			vlessServer, cancelServer, err := createAndStart(serverConfig)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { cancelServer(); _ = vlessServer.Close() }()

			gatewayPort := freePort(t)
			config := domain.DefaultConfig()
			config.SocksPort = gatewayPort
			node := test.node
			node.Type, node.Name, node.Server, node.Port, node.UUID = "vless", test.name, "127.0.0.1", serverPort, uuid
			revision := &domain.Revision{SocksBind: "127.0.0.1", SocksPort: gatewayPort, SocksAdvertise: "127.0.0.1", Nodes: []domain.RuntimeNode{{
				Node: node, NodeID: test.name, DisplayName: test.name, AuthUser: test.name + "-user", Password: test.name + "-pass", OutboundTag: "vless-" + test.name,
			}}}
			compiled, err := NewCompiler().Compile(config, revision)
			if err != nil {
				t.Fatal(err)
			}
			manager := NewManager(nil)
			if err := manager.Start(revision, compiled, fmt.Sprintf("127.0.0.1:%d", gatewayPort)); err != nil {
				t.Fatal(err)
			}
			defer manager.Stop()

			body, err := getThroughSOCKS(target.URL, gatewayPort, test.name+"-user", test.name+"-pass")
			if err != nil || body != "transport-ok" {
				t.Fatalf("%s route failed: body=%q err=%v", test.name, body, err)
			}
		})
	}
}

func TestCompilerRejectsDuplicateIdentityAndNonVLESS(t *testing.T) {
	base := domain.RuntimeNode{Node: domain.Node{Type: "vless", Server: "example.com", Port: 443, UUID: "11111111-1111-4111-8111-111111111111", Network: "tcp"}, NodeID: "one", DisplayName: "one", AuthUser: "duplicate", Password: "password", OutboundTag: "vless-one"}
	duplicate := base
	duplicate.NodeID = "two"
	duplicate.DisplayName = "two"
	duplicate.OutboundTag = "vless-two"
	if _, err := NewCompiler().Compile(domain.DefaultConfig(), &domain.Revision{Nodes: []domain.RuntimeNode{base, duplicate}}); err == nil {
		t.Fatal("duplicate auth_user was accepted")
	}
	base.Type = "trojan"
	if _, err := NewCompiler().Compile(domain.DefaultConfig(), &domain.Revision{Nodes: []domain.RuntimeNode{base}}); err == nil {
		t.Fatal("non-VLESS node was accepted by the VLESS compiler")
	}
}

func TestSingleSOCKSInboundSupports150NodeIdentities(t *testing.T) {
	port := freePort(t)
	config := domain.DefaultConfig()
	config.SocksPort = port
	revision := &domain.Revision{SocksBind: "127.0.0.1", SocksPort: port, SocksAdvertise: "127.0.0.1"}
	for index := 1; index <= 150; index++ {
		uuid := fmt.Sprintf("77777777-7777-4777-8777-%012d", index)
		revision.Nodes = append(revision.Nodes, domain.RuntimeNode{
			Node:   domain.Node{Type: "vless", Name: fmt.Sprintf("Node %03d", index), Server: "example.com", Port: 443, UUID: uuid, Network: "tcp"},
			NodeID: fmt.Sprintf("n-%03d", index), DisplayName: fmt.Sprintf("Node %03d", index), AuthUser: fmt.Sprintf("user-%03d", index), Password: fmt.Sprintf("password-%03d", index), OutboundTag: fmt.Sprintf("vless-%03d", index),
		})
	}
	compiled, err := NewCompiler().Compile(config, revision)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Inbounds  []map[string]any `json:"inbounds"`
		Outbounds []map[string]any `json:"outbounds"`
		Route     struct {
			Rules []map[string]any `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal(compiled, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Inbounds) != 1 || len(decoded.Outbounds) != 150 || len(decoded.Route.Rules) != 151 {
		t.Fatalf("unexpected 150-node topology: inbounds=%d outbounds=%d rules=%d", len(decoded.Inbounds), len(decoded.Outbounds), len(decoded.Route.Rules))
	}
	manager := NewManager(nil)
	if err := manager.Start(revision, compiled, fmt.Sprintf("127.0.0.1:%d", port)); err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()
	status := manager.Status()
	if status.State != "running" || status.Users != 150 || status.Outbounds != 150 {
		t.Fatalf("150-node Engine did not start on one inbound: %+v", status)
	}
}
