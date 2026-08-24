package app

import (
	"context"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestNodeThroughSOCKSReportsSuccessAndAuthenticationStage(t *testing.T) {
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer tlsServer.Close()
	targetHost, targetPortText, err := net.SplitHostPort(tlsServer.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	targetPort, err := strconv.Atoi(targetPortText)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(tlsServer.Certificate())
	target := nodeTestTarget{
		Host:       targetHost,
		Port:       uint16(targetPort),
		Path:       "/generate_204",
		ServerName: targetHost,
		RootCAs:    roots,
	}
	proxyAddress := startTestSOCKS5Proxy(t, "known-user", "known-password")
	if status, err := testNodeThroughSOCKS(context.Background(), proxyAddress, "known-user", "known-password", target); err != nil || status != http.StatusNoContent {
		t.Fatalf("full SOCKS/TLS/HTTP probe failed: status=%d err=%v", status, err)
	}
	_, err = testNodeThroughSOCKS(context.Background(), proxyAddress, "known-user", "wrong-password", target)
	var staged *nodeTestError
	if !errors.As(err, &staged) || staged.stage != "authentication" {
		t.Fatalf("wrong credentials did not report the authentication stage: %v", err)
	}
}

func startTestSOCKS5Proxy(t *testing.T, username, password string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go handleTestSOCKS5Connection(connection, username, password)
		}
	}()
	return listener.Addr().String()
}

func handleTestSOCKS5Connection(connection net.Conn, username, password string) {
	defer connection.Close()
	greeting := make([]byte, 3)
	if _, err := io.ReadFull(connection, greeting); err != nil || greeting[0] != 0x05 {
		return
	}
	if _, err := connection.Write([]byte{0x05, 0x02}); err != nil {
		return
	}
	authHeader := make([]byte, 2)
	if _, err := io.ReadFull(connection, authHeader); err != nil || authHeader[0] != 0x01 {
		return
	}
	providedUser := make([]byte, int(authHeader[1]))
	if _, err := io.ReadFull(connection, providedUser); err != nil {
		return
	}
	passwordLength := make([]byte, 1)
	if _, err := io.ReadFull(connection, passwordLength); err != nil {
		return
	}
	providedPassword := make([]byte, int(passwordLength[0]))
	if _, err := io.ReadFull(connection, providedPassword); err != nil {
		return
	}
	if string(providedUser) != username || string(providedPassword) != password {
		_, _ = connection.Write([]byte{0x01, 0x01})
		return
	}
	if _, err := connection.Write([]byte{0x01, 0x00}); err != nil {
		return
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(connection, header); err != nil || header[0] != 0x05 || header[1] != 0x01 {
		return
	}
	var host string
	switch header[3] {
	case 0x01:
		value := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(connection, value); err != nil {
			return
		}
		host = net.IP(value).String()
	case 0x03:
		length := make([]byte, 1)
		if _, err := io.ReadFull(connection, length); err != nil {
			return
		}
		value := make([]byte, int(length[0]))
		if _, err := io.ReadFull(connection, value); err != nil {
			return
		}
		host = string(value)
	default:
		return
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(connection, portBytes); err != nil {
		return
	}
	upstream, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(portBytes)))))
	if err != nil {
		_, _ = connection.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer upstream.Close()
	if _, err := connection.Write([]byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, 0, 0}); err != nil {
		return
	}
	go func() { _, _ = io.Copy(upstream, connection) }()
	_, _ = io.Copy(connection, upstream)
}
