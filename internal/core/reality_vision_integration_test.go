//go:build with_utls && !race

package core

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ssfun/vless2surge/internal/domain"
)

// The upstream Vision implementation in sing-vmess v0.2.8 uses pointer
// arithmetic that is intentionally incompatible with Go race/checkptr
// instrumentation. Functional Vision coverage therefore runs in the normal
// with_utls suite; the race suite still covers TLS/uTLS, standard gRPC, plain
// VLESS TCP/UDP and all vless2surge control-plane concurrency.
func TestRealityVisionUTLSRouteThroughEmbeddedCore(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "reality-ok") }))
	defer target.Close()
	decoy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer decoy.Close()

	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyText := base64.RawURLEncoding.EncodeToString(privateKey.Bytes())
	publicKeyText := base64.RawURLEncoding.EncodeToString(privateKey.PublicKey().Bytes())
	serverPort := freePort(t)
	uuid := "92929292-9292-4292-8292-929292929292"
	shortID := "0123456789abcdef"
	serverConfig := []byte(fmt.Sprintf(`{
  "log":{"disabled":true},
  "inbounds":[{"type":"vless","tag":"server","listen":"127.0.0.1","listen_port":%d,
    "users":[{"uuid":%q,"flow":"xtls-rprx-vision"}],
    "tls":{"enabled":true,"server_name":"example.com","reality":{"enabled":true,
      "handshake":{"server":"127.0.0.1","server_port":%d},"private_key":%q,"short_id":[%q]}}}],
  "outbounds":[{"type":"direct","tag":"direct"}],
  "route":{"final":"direct"}
}`, serverPort, uuid, decoy.Listener.Addr().(*net.TCPAddr).Port, privateKeyText, shortID))
	vlessServer, cancelServer, err := createAndStart(serverConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancelServer(); _ = vlessServer.Close() }()

	gatewayPort := freePort(t)
	config := domain.DefaultConfig()
	config.SocksPort = gatewayPort
	revision := &domain.Revision{ID: "reality-route", SocksBind: "127.0.0.1", SocksPort: gatewayPort, SocksAdvertise: "127.0.0.1", Nodes: []domain.RuntimeNode{{
		Node: domain.Node{
			Type: "vless", Name: "Reality Vision", Server: "127.0.0.1", Port: serverPort, UUID: uuid,
			Network: "tcp", Security: "reality", ServerName: "example.com", Fingerprint: "chrome",
			Flow: "xtls-rprx-vision", RealityPublicKey: publicKeyText, RealityShortID: shortID,
		},
		NodeID: "reality", DisplayName: "Reality Vision", AuthUser: "reality-user", Password: "reality-pass", OutboundTag: "vless-reality",
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

	body, err := getThroughSOCKS(target.URL, gatewayPort, "reality-user", "reality-pass")
	if err != nil || body != "reality-ok" {
		t.Fatalf("Reality/Vision/uTLS route failed: body=%q err=%v", body, err)
	}
}

func TestSingleSOCKSInboundStarts150RealityVisionIdentities(t *testing.T) {
	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := base64.RawURLEncoding.EncodeToString(privateKey.PublicKey().Bytes())
	port := freePort(t)
	config := domain.DefaultConfig()
	config.SocksPort = port
	revision := &domain.Revision{SocksBind: "127.0.0.1", SocksPort: port, SocksAdvertise: "127.0.0.1"}
	for index := 1; index <= 150; index++ {
		uuid := fmt.Sprintf("69696969-6969-4696-8696-%012d", index)
		revision.Nodes = append(revision.Nodes, domain.RuntimeNode{
			Node: domain.Node{
				Type: "vless", Name: fmt.Sprintf("Reality %03d", index), Server: "example.com", Port: 443, UUID: uuid,
				Network: "tcp", Security: "reality", ServerName: "example.com", Fingerprint: "chrome",
				Flow: "xtls-rprx-vision", RealityPublicKey: publicKey, RealityShortID: "0123456789abcdef",
			},
			NodeID: fmt.Sprintf("reality-%03d", index), DisplayName: fmt.Sprintf("Reality %03d", index),
			AuthUser: fmt.Sprintf("reality-user-%03d", index), Password: fmt.Sprintf("reality-pass-%03d", index), OutboundTag: fmt.Sprintf("vless-reality-%03d", index),
		})
	}
	compiled, err := NewCompiler().Compile(config, revision)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(nil)
	if err := manager.Start(revision, compiled, fmt.Sprintf("127.0.0.1:%d", port)); err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()
	status := manager.Status()
	if status.State != "running" || status.Users != 150 || status.Outbounds != 150 {
		t.Fatalf("150-node Reality Engine did not start on one inbound: %+v", status)
	}
}
