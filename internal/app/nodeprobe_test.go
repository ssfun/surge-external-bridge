package app

import (
	"bytes"
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
	"time"
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
		RawURL:     tlsServer.URL + "/generate_204",
		Scheme:     "https",
		Host:       targetHost,
		HostHeader: tlsServer.Listener.Addr().String(),
		Port:       uint16(targetPort),
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

func TestNodeUDPThroughSOCKSReportsSuccessAndAuthenticationStage(t *testing.T) {
	dnsAddress := startTestDNSServer(t)
	host, portText, err := net.SplitHostPort(dnsAddress)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	proxyAddress := startTestSOCKS5UDPProxy(t, "known-user", "known-password")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := testNodeUDPThroughSOCKS(ctx, proxyAddress, "known-user", "known-password", host, uint16(port)); err != nil {
		t.Fatalf("full SOCKS/VLESS-style UDP/DNS probe failed: %v", err)
	}
	err = testNodeUDPThroughSOCKS(ctx, proxyAddress, "known-user", "wrong-password", host, uint16(port))
	var staged *nodeTestError
	if !errors.As(err, &staged) || staged.stage != "authentication" {
		t.Fatalf("wrong UDP credentials did not report the authentication stage: %v", err)
	}
}

func TestParseNodeTestTargets(t *testing.T) {
	target, err := parseNodeTestTarget("https://example.com:8443/generate_204?source=test")
	if err != nil || target.Scheme != "https" || target.Host != "example.com" || target.Port != 8443 || target.HostHeader != "example.com:8443" {
		t.Fatalf("unexpected HTTP target: %+v err=%v", target, err)
	}
	host, port, err := parseNodeTestUDPAddress("1.1.1.1:53")
	if err != nil || host != "1.1.1.1" || port != 53 {
		t.Fatalf("unexpected UDP target: host=%q port=%d err=%v", host, port, err)
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

func startTestDNSServer(t *testing.T) string {
	t.Helper()
	server, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	go func() {
		buffer := make([]byte, 2048)
		for {
			count, peer, err := server.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			if count < 12 {
				continue
			}
			response := append([]byte(nil), buffer[:count]...)
			binary.BigEndian.PutUint16(response[2:4], 0x8180)
			_, _ = server.WriteToUDP(response, peer)
		}
	}()
	return server.LocalAddr().String()
}

func startTestSOCKS5UDPProxy(t *testing.T, username, password string) string {
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
			go handleTestSOCKS5UDPConnection(connection, username, password)
		}
	}()
	return listener.Addr().String()
}

func handleTestSOCKS5UDPConnection(control net.Conn, username, password string) {
	defer control.Close()
	_ = control.SetDeadline(time.Now().Add(5 * time.Second))
	greeting := make([]byte, 3)
	if _, err := io.ReadFull(control, greeting); err != nil || greeting[0] != 0x05 {
		return
	}
	if _, err := control.Write([]byte{0x05, 0x02}); err != nil {
		return
	}
	authHeader := make([]byte, 2)
	if _, err := io.ReadFull(control, authHeader); err != nil || authHeader[0] != 0x01 {
		return
	}
	providedUser := make([]byte, int(authHeader[1]))
	if _, err := io.ReadFull(control, providedUser); err != nil {
		return
	}
	passwordLength := []byte{0}
	if _, err := io.ReadFull(control, passwordLength); err != nil {
		return
	}
	providedPassword := make([]byte, int(passwordLength[0]))
	if _, err := io.ReadFull(control, providedPassword); err != nil {
		return
	}
	if string(providedUser) != username || string(providedPassword) != password {
		_, _ = control.Write([]byte{0x01, 0x01})
		return
	}
	if _, err := control.Write([]byte{0x01, 0x00}); err != nil {
		return
	}
	command := make([]byte, 4)
	if _, err := io.ReadFull(control, command); err != nil || command[0] != 0x05 || command[1] != 0x03 {
		return
	}
	if _, _, err := readSOCKSAddress(control, command[3]); err != nil {
		return
	}
	relay, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		return
	}
	defer relay.Close()
	_ = relay.SetDeadline(time.Now().Add(5 * time.Second))
	relayAddress := relay.LocalAddr().(*net.UDPAddr)
	reply, err := appendSOCKSAddress([]byte{0x05, 0x00, 0x00}, relayAddress.IP.String(), uint16(relayAddress.Port))
	if err != nil {
		return
	}
	if _, err := control.Write(reply); err != nil {
		return
	}
	buffer := make([]byte, 4096)
	count, client, err := relay.ReadFromUDP(buffer)
	if err != nil || count < 4 || buffer[2] != 0 {
		return
	}
	reader := bytes.NewReader(buffer[4:count])
	host, port, err := readSOCKSAddress(reader, buffer[3])
	if err != nil {
		return
	}
	payload, err := io.ReadAll(reader)
	if err != nil {
		return
	}
	upstreamAddress, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(int(port))))
	if err != nil {
		return
	}
	upstream, err := net.DialUDP("udp", nil, upstreamAddress)
	if err != nil {
		return
	}
	defer upstream.Close()
	_ = upstream.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := upstream.Write(payload); err != nil {
		return
	}
	responsePayload := make([]byte, 4096)
	responseCount, err := upstream.Read(responsePayload)
	if err != nil {
		return
	}
	response, err := appendSOCKSAddress([]byte{0, 0, 0}, host, port)
	if err != nil {
		return
	}
	response = append(response, responsePayload[:responseCount]...)
	_, _ = relay.WriteToUDP(response, client)
}
