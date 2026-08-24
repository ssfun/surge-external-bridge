//go:build with_utls

package core

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ssfun/vless2surge/internal/domain"
)

func TestTLSUTLSRouteThroughEmbeddedCore(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "tls-ok") }))
	defer target.Close()
	serverPort := freePort(t)
	uuid := "73737373-7373-4373-8373-737373737373"
	serverConfig := []byte(fmt.Sprintf(`{
  "log":{"disabled":true},
  "inbounds":[{"type":"vless","tag":"server","listen":"127.0.0.1","listen_port":%d,
    "users":[{"uuid":%q}],"tls":{"enabled":true,"server_name":"example.com","insecure":true,"alpn":["http/1.1"]}}],
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
	revision := &domain.Revision{SocksBind: "127.0.0.1", SocksPort: gatewayPort, SocksAdvertise: "127.0.0.1", Nodes: []domain.RuntimeNode{{
		Node: domain.Node{
			Type: "vless", Name: "TLS uTLS", Server: "127.0.0.1", Port: serverPort, UUID: uuid,
			Network: "tcp", Security: "tls", ServerName: "example.com", Fingerprint: "chrome", ALPN: []string{"http/1.1"}, Insecure: true,
		},
		NodeID: "tls", DisplayName: "TLS uTLS", AuthUser: "tls-user", Password: "tls-pass", OutboundTag: "vless-tls",
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

	body, err := getThroughSOCKS(target.URL, gatewayPort, "tls-user", "tls-pass")
	if err != nil || body != "tls-ok" {
		t.Fatalf("TLS/uTLS route failed: body=%q err=%v", body, err)
	}
}
