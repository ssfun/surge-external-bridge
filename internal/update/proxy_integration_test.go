package update

// Failure cases established before implementation: implicit environment routing;
// metadata/checksums/binary or redirect bypassing the explicit proxy; invalid
// settings silently becoming DIRECT; settings lost on reload; busy replacement;
// transport errors exposing proxy credentials; checksum validation bypass.
import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestUpdateProxyIntegration(t *testing.T) {
	payload := []byte("proxy integration release")
	sum := fmt.Sprintf("%x", sha256.Sum256(payload))
	base := "https://github.com/" + Repository + "/releases/download/v1.2.0/"
	release := Release{Tag: "v1.2.0", Assets: []Asset{{Name: "SHA256SUMS", URL: base + "SHA256SUMS"}, {Name: "SurgeEB-linux-arm64", URL: base + "SurgeEB-linux-arm64", Size: int64(len(payload))}}}
	var mu sync.Mutex
	seen := map[string]int{}
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.Host+r.URL.Path]++
		mu.Unlock()
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/"):
			json.NewEncoder(w).Encode([]Release{release})
		case strings.HasSuffix(r.URL.Path, "SHA256SUMS"):
			fmt.Fprintf(w, "%s  SurgeEB-linux-arm64\n", sum)
		case r.Host == "github.com":
			http.Redirect(w, r, "https://release-assets.githubusercontent.com/binary", http.StatusFound)
		default:
			w.Write(payload)
		}
	}))
	defer origin.Close()
	for _, scheme := range []string{"http", "https", "socks5", "socks5h"} {
		t.Run(scheme, func(t *testing.T) {
			proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "CONNECT" || r.Header.Get("Proxy-Authorization") != "Basic dXNlcjpzZWNyZXQ=" {
					http.Error(w, "bad proxy request", 407)
					return
				}
				upstream, err := net.Dial("tcp", strings.TrimPrefix(origin.URL, "https://"))
				if err != nil {
					http.Error(w, "upstream", 502)
					return
				}
				client, buf, err := w.(http.Hijacker).Hijack()
				if err != nil {
					upstream.Close()
					return
				}
				fmt.Fprint(buf, "HTTP/1.1 200 Connection Established\r\n\r\n")
				buf.Flush()
				go func() { io.Copy(upstream, buf); upstream.Close() }()
				io.Copy(client, upstream)
				client.Close()
			}))
			if scheme == "https" {
				proxy.StartTLS()
			} else {
				proxy.Start()
			}
			defer proxy.Close()
			address := strings.Replace(proxy.URL, "://", "://user:secret@", 1)
			closeProxy := proxy.Close
			if strings.HasPrefix(scheme, "socks") {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				closeProxy = func() { listener.Close() }
				defer closeProxy()
				address = scheme + "://user:secret@" + listener.Addr().String()
				go func() {
					for {
						conn, err := listener.Accept()
						if err != nil {
							return
						}
						go serveUpdateSOCKS(conn, strings.TrimPrefix(origin.URL, "https://"))
					}
				}()
			}
			dir := t.TempDir()
			m, err := NewManager(dir, "v1.0.0", Runtime{})
			if err != nil {
				t.Fatal(err)
			}
			prefs := m.prefs
			prefs.ProxyURL = address
			if err = m.Preferences(prefs); err != nil {
				t.Fatal(err)
			}
			m, err = NewManager(dir, "v1.0.0", Runtime{})
			if err != nil || m.prefs.ProxyURL != address {
				t.Fatalf("reload: %v", err)
			}
			tr := m.client.HTTP.Transport.(*http.Transport)
			tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // local fixture only
			defer tr.CloseIdleConnections()
			if err = m.Check(context.Background()); err != nil {
				t.Fatal(err)
			}
			path, digest, err := m.client.Download(context.Background(), *m.latest, "linux", "arm64", filepath.Join(dir, "SurgeEB"))
			if err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(path)
			if string(data) != string(payload) || digest != sum {
				t.Fatal("download mismatch")
			}
			persisted, _ := os.ReadFile(filepath.Join(dir, "update-settings.json"))
			before := m.client
			m.busy = true
			prefs.ProxyURL = ""
			if m.Preferences(prefs) == nil {
				t.Fatal("busy settings accepted")
			}
			m.busy = false
			if m.client != before {
				t.Fatal("busy client replaced")
			}
			for _, bad := range []string{"ftp://user:secret@localhost:80", "http://user:secret@localhost", "http://user:secret@localhost:0", "http://user:secret@localhost:65536", "http://user:secret@localhost:80/path", "http://user:secret@localhost:80?x", "http://user:secret@localhost:80#x"} {
				prefs.ProxyURL = bad
				err = m.Preferences(prefs)
				if err == nil || strings.Contains(err.Error(), "secret") {
					t.Fatalf("invalid setting error: %v", err)
				}
				if m.client != before || m.prefs.ProxyURL != address {
					t.Fatal("invalid settings mutated client")
				}
			}
			after, _ := os.ReadFile(filepath.Join(dir, "update-settings.json"))
			if string(after) != string(persisted) {
				t.Fatal("invalid preferences changed disk")
			}
			prefs.ProxyURL = address
			if err = m.Preferences(prefs); err != nil || m.client != before {
				t.Fatal("unchanged client replaced")
			}
			closeProxy()
			tr.CloseIdleConnections()
			err = m.Check(context.Background())
			if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "user") {
				t.Fatalf("credential leakage/fallback: %v", err)
			}
			prefs.ProxyURL = ""
			if err = m.Preferences(prefs); err != nil || m.client.HTTP.Transport.(*http.Transport).Proxy != nil {
				t.Fatalf("clear to DIRECT: %v", err)
			}
			if err = os.WriteFile(filepath.Join(dir, "update-settings.json"), []byte(`{"proxy_url":"http://user:secret@localhost"}`), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = NewManager(dir, "v1.0.0", Runtime{}); err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("invalid disk settings: %v", err)
			}
		})
	}
	mu.Lock()
	defer mu.Unlock()
	for _, key := range []string{"api.github.com/repos/" + Repository + "/releases", "github.com/" + Repository + "/releases/download/v1.2.0/SHA256SUMS", "github.com/" + Repository + "/releases/download/v1.2.0/SurgeEB-linux-arm64", "release-assets.githubusercontent.com/binary"} {
		if seen[key] != 4 {
			t.Errorf("missing proxied request %s: %d", key, seen[key])
		}
	}
	direct := NewClient()
	if direct.HTTP.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("DIRECT inherits environment")
	}
	for _, scheme := range []string{"socks5", "socks5h"} {
		if _, err := NewClientWithProxy(scheme + "://user:secret@127.0.0.1:1080"); err != nil {
			t.Fatal(err)
		}
	}
}

// serveUpdateSOCKS implements an authenticated SOCKS5 fixture and requires
// domain addressing, proving DNS resolution remains on the proxy side.
func serveUpdateSOCKS(conn net.Conn, target string) {
	defer conn.Close()
	read := func(n int) ([]byte, error) { b := make([]byte, n); _, err := io.ReadFull(conn, b); return b, err }
	greeting, err := read(2)
	if err != nil || greeting[0] != 5 {
		return
	}
	if _, err = read(int(greeting[1])); err != nil {
		return
	}
	conn.Write([]byte{5, 2})
	auth, err := read(2)
	if err != nil || auth[0] != 1 {
		return
	}
	user, err := read(int(auth[1]))
	if err != nil {
		return
	}
	length, err := read(1)
	if err != nil {
		return
	}
	password, err := read(int(length[0]))
	if err != nil || string(user) != "user" || string(password) != "secret" {
		return
	}
	conn.Write([]byte{1, 0})
	request, err := read(5)
	if err != nil || request[0] != 5 || request[1] != 1 || request[3] != 3 {
		return
	}
	destination, err := read(int(request[4]) + 2)
	if err != nil {
		return
	}
	host := string(destination[:len(destination)-2])
	if host != "api.github.com" && host != "github.com" && host != "release-assets.githubusercontent.com" {
		return
	}
	upstream, err := net.Dial("tcp", target)
	if err != nil {
		return
	}
	defer upstream.Close()
	conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 80})
	go func() { io.Copy(upstream, conn); upstream.Close() }()
	io.Copy(conn, upstream)
}
