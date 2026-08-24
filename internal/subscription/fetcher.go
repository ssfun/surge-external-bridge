package subscription

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ssfun/vless2surge/internal/domain"
)

const maxSubscriptionBytes = 16 << 20

type Fetcher struct {
	client *http.Client
}

func NewFetcher() *Fetcher {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 4
	return &Fetcher{client: &http.Client{Timeout: 30 * time.Second, Transport: transport}}
}

func (f *Fetcher) Fetch(ctx context.Context, sub domain.Subscription, userAgent string) ([]byte, error) {
	u, err := url.Parse(sub.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("subscription URL must be HTTP or HTTPS")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sub.URL, nil)
	if err != nil {
		return nil, errors.New("create subscription request")
	}
	if userAgent == "" {
		userAgent = "vless2surge"
	}
	req.Header.Set("User-Agent", userAgent)
	for key, value := range sub.Headers {
		if strings.EqualFold(key, "Host") || strings.EqualFold(key, "Content-Length") {
			continue
		}
		req.Header.Set(key, value)
	}
	client := *f.client
	client.CheckRedirect = func(redirect *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if len(via) > 0 && !sameOriginURL(redirect.URL, via[0].URL) {
			for key := range sub.Headers {
				redirect.Header.Del(key)
			}
			redirect.Header.Del("Authorization")
			redirect.Header.Del("Cookie")
			redirect.Header.Del("Proxy-Authorization")
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request subscription: %s", sanitizeNetworkError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("subscription returned HTTP %d", resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, maxSubscriptionBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, errors.New("read subscription response")
	}
	if len(data) > maxSubscriptionBytes {
		return nil, fmt.Errorf("subscription exceeds %d MiB", maxSubscriptionBytes>>20)
	}
	return data, nil
}

func sameOriginURL(left, right *url.URL) bool {
	return left != nil && right != nil && strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}

func sanitizeNetworkError(err error) string {
	if err == nil {
		return "unknown error"
	}
	text := err.Error()
	for _, marker := range []string{"http://", "https://"} {
		if index := strings.Index(text, marker); index >= 0 {
			remaining := text[index:]
			if end := strings.IndexAny(remaining, " \""); end >= 0 {
				remaining = remaining[:end]
			}
			if parsed, parseErr := url.Parse(remaining); parseErr == nil && parsed.Host != "" {
				text = strings.Replace(text, remaining, parsed.Scheme+"://"+parsed.Host+"/…", 1)
			}
		}
	}
	return text
}
