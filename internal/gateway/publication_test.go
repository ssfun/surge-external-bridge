package gateway

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPolicyPublicationRemainsAvailableDuringProviderPreparation(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("failed_download=%t", fail), func(t *testing.T) {
			application := newRunningTestApp(t)
			_, err := application.AddProvider(Provider{
				Name: "existing", Type: "inline", Enabled: true,
				Payload: InlinePayload{{"name": "old", "type": "socks5", "server": "127.0.0.1", "port": 29999}},
			})
			if err != nil {
				t.Fatal(err)
			}
			before, revision, err := application.ProxiesForToken("")
			if err != nil || before == "" {
				t.Fatalf("initial publication: %q, %v", before, err)
			}
			entered, release := make(chan struct{}, 1), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case entered <- struct{}{}:
				default:
				}
				<-release
				if fail {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				fmt.Fprint(w, "proxies:\n  - name: new\n    type: socks5\n    server: 127.0.0.1\n    port: 29999\n")
			}))
			defer server.Close()
			// Always release the request before cleanup, including failed assertions.
			released := false
			defer func() {
				if !released {
					close(release)
				}
			}()
			done := make(chan error, 1)
			go func() {
				_, err := application.AddProvider(Provider{Name: "slow", Type: "http", URL: server.URL, Enabled: true})
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("Provider download did not start")
			}
			read := make(chan error, 1)
			go func() {
				content, hash, err := application.ProxiesForToken("")
				if err == nil && (content != before || hash != revision) {
					err = fmt.Errorf("uncommitted publication: %q", content)
				}
				read <- err
			}()
			select {
			case err := <-read:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("publication waited for a blocked subscription download")
			}
			close(release)
			released = true
			select {
			case err := <-done:
				if (err != nil) != fail {
					t.Fatalf("apply error=%v, fail=%t", err, fail)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Provider apply did not finish")
			}
			after, _, err := application.ProxiesForToken("")
			if err != nil {
				t.Fatal(err)
			}
			if fail && after != before {
				t.Fatal("failed preparation replaced the published snapshot")
			}
			if !fail && !strings.Contains(after, "new") {
				t.Fatal("committed Provider is absent from publication")
			}
		})
	}
}
