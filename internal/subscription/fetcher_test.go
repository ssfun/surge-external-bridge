package subscription

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ssfun/vless2surge/internal/domain"
)

func TestFetcherStripsCustomHeadersOnCrossOriginRedirect(t *testing.T) {
	received := make(chan http.Header, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Clone()
		_, _ = io.WriteString(w, "vless://11111111-1111-4111-8111-111111111111@example.com:443#Node")
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()

	sub := domain.Subscription{URL: source.URL, Headers: map[string]string{"Authorization": "Bearer secret", "X-Airport-Token": "secret"}}
	if _, err := NewFetcher().Fetch(context.Background(), sub, "test-agent"); err != nil {
		t.Fatal(err)
	}
	headers := <-received
	if headers.Get("Authorization") != "" || headers.Get("X-Airport-Token") != "" {
		t.Fatalf("subscription credentials crossed origins: %+v", headers)
	}
	if headers.Get("User-Agent") != "test-agent" {
		t.Fatalf("non-secret User-Agent was unexpectedly removed: %+v", headers)
	}
}

func TestFetcherKeepsHeadersOnSameOriginRedirect(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, server.URL+"/result", http.StatusFound)
			return
		}
		if r.Header.Get("X-Airport-Token") != "secret" {
			t.Errorf("same-origin redirect lost custom header: %+v", r.Header)
		}
		_, _ = io.WriteString(w, "vless://11111111-1111-4111-8111-111111111111@example.com:443#Node")
	}))
	defer server.Close()
	_, err := NewFetcher().Fetch(context.Background(), domain.Subscription{URL: server.URL + "/start", Headers: map[string]string{"X-Airport-Token": "secret"}}, "")
	if err != nil {
		t.Fatal(err)
	}
}

func TestNetworkErrorRedactsURLPathAndQuery(t *testing.T) {
	message := sanitizeNetworkError(&urlErrorString{"Get \"https://feed.example/private/token?secret=yes\": connection refused"})
	if strings.Contains(message, "private/token") || strings.Contains(message, "secret=yes") || !strings.Contains(message, "https://feed.example/…") {
		t.Fatalf("network error was not redacted: %s", message)
	}
}

type urlErrorString struct{ text string }

func (e *urlErrorString) Error() string { return e.text }
