package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/ssfun/surge-external-bridge/internal/gateway"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func mustDefaultGatewayConfig(t *testing.T) gateway.Config {
	t.Helper()
	config, err := gateway.DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func TestManagementProviderLifecycleAndMutationBoundaries(t *testing.T) {
	application, _, endpoint := testManagementServer(t, "management-token-1234567890")
	defer application.Close()
	request := func(method, path string, body []byte, origin string) *http.Response {
		req, err := http.NewRequest(method, endpoint+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer management-token-1234567890")
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	payload := []byte(`{"name":"Lifecycle","type":"inline","enabled":true,"payload":[{"name":"Lifecycle Node","type":"vless","server":"127.0.0.1","port":65530,"uuid":"11111111-1111-4111-8111-111111111111","network":"tcp","tls":false}]}`)
	response := request(http.MethodPost, "/api/providers", payload, endpoint)
	if response.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		t.Fatalf("create Provider status=%d body=%s", response.StatusCode, data)
	}
	var created publicProvider
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		_ = response.Body.Close()
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if created.StableID == "" || len(application.Snapshot().Entries()) != 1 {
		t.Fatalf("created Provider was not projected: %#v", created)
	}

	disabled := []byte(`{"name":"Lifecycle","type":"inline","enabled":false}`)
	response = request(http.MethodPut, "/api/providers/"+created.StableID, disabled, endpoint)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || len(application.Snapshot().Entries()) != 0 {
		t.Fatalf("disable Provider status=%d projection=%d", response.StatusCode, len(application.Snapshot().Entries()))
	}
	enabled := []byte(`{"name":"Lifecycle","type":"inline","enabled":true}`)
	response = request(http.MethodPut, "/api/providers/"+created.StableID, enabled, endpoint)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || len(application.Snapshot().Entries()) != 1 {
		t.Fatalf("enable Provider status=%d projection=%d", response.StatusCode, len(application.Snapshot().Entries()))
	}
	response = request(http.MethodDelete, "/api/providers/"+created.StableID, nil, endpoint)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent || len(application.Snapshot().Entries()) != 0 {
		t.Fatalf("delete Provider status=%d projection=%d", response.StatusCode, len(application.Snapshot().Entries()))
	}

	response = request(http.MethodPost, "/api/providers", payload, "https://attacker.example")
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin Provider mutation status=%d, want 403", response.StatusCode)
	}
	response = request(http.MethodPost, "/api/providers", []byte(`{"name":"Invalid","unknown":true}`), endpoint)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown Provider field status=%d, want 400", response.StatusCode)
	}
}

func TestControllerAllowlistUsesPrivateCredentialAndBlocksDangerousRoutes(t *testing.T) {
	application, server, endpoint := testManagementServer(t, "management-token-1234567890")
	defer application.Close()

	request := func(method, path string, headers map[string]string) *http.Response {
		req, err := http.NewRequest(method, endpoint+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer management-token-1234567890")
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}

	for _, path := range []string{"/api/mihomo/version", "/api/mihomo/configs", "/api/mihomo/connections"} {
		response := request(http.MethodGet, path, nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d", path, response.StatusCode)
		}
		_ = response.Body.Close()
	}
	response := request(http.MethodGet, "/proxies", nil)
	emptyContent, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || len(emptyContent) != 0 {
		t.Fatalf("empty /proxies status=%d content=%q err=%v", response.StatusCode, emptyContent, err)
	}
	emptyETag := response.Header.Get("ETag")
	if emptyETag == "" || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("empty /proxies cache contract missing: ETag=%q Cache-Control=%q", emptyETag, response.Header.Get("Cache-Control"))
	}
	response = request(http.MethodGet, "/proxies", map[string]string{"If-None-Match": emptyETag})
	if response.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional empty /proxies status=%d, want 304", response.StatusCode)
	}
	_ = response.Body.Close()

	provider, err := application.AddProvider(gateway.Provider{
		Name: "Path Provider", Type: "inline", Enabled: true,
		Payload: []map[string]any{{
			"name": "Node / 100%", "type": "vless", "server": "127.0.0.1", "port": 65530,
			"uuid": "11111111-1111-4111-8111-111111111111", "network": "tcp", "tls": false,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider.StableID == "" || len(application.Snapshot().Entries()) != 1 {
		t.Fatal("inline Provider was not projected")
	}
	entry := application.Snapshot().Entries()[0]
	expectedLine, err := application.SurgeLine(entry.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	response = request(http.MethodGet, "/proxies", nil)
	content, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || string(content) != expectedLine+"\n" {
		t.Fatalf("/proxies output status=%d content=%q err=%v, want %q", response.StatusCode, content, err, expectedLine+"\n")
	}
	etag := response.Header.Get("ETag")
	if etag == "" || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("/proxies cache contract missing: ETag=%q Cache-Control=%q", etag, response.Header.Get("Cache-Control"))
	}
	response = request(http.MethodGet, "/proxies", map[string]string{"If-None-Match": etag})
	if response.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional /proxies status=%d, want 304", response.StatusCode)
	}
	_ = response.Body.Close()
	provider.IncludeName = "Node"
	provider, err = application.UpdateProvider(provider.StableID, provider)
	if err != nil || provider.IncludeName != "Node" {
		t.Fatalf("set Provider projection filter: provider=%#v err=%v", provider, err)
	}
	provider.IncludeName = ""
	provider, err = application.UpdateProvider(provider.StableID, provider)
	if err != nil || provider.IncludeName != "" {
		t.Fatalf("clear Provider projection filter: provider=%#v err=%v", provider, err)
	}
	response = request(http.MethodGet, "/api/nodes/"+application.Snapshot().Entries()[0].PublicID+"/runtime", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("escaped node runtime path = %d", response.StatusCode)
	}
	_ = response.Body.Close()
	response = request(http.MethodGet, "/api/nodes/"+application.Snapshot().Entries()[0].PublicID+"/healthcheck", nil)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("node healthcheck GET status=%d, want 404", response.StatusCode)
	}
	_ = response.Body.Close()
	response = request(http.MethodPost, "/api/nodes/"+application.Snapshot().Entries()[0].PublicID+"/healthcheck", map[string]string{"Origin": "https://attacker.example"})
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin node healthcheck status=%d, want 403", response.StatusCode)
	}
	_ = response.Body.Close()
	for _, item := range []struct{ method, path string }{
		{http.MethodPut, "/api/mihomo/configs"}, {http.MethodPatch, "/api/mihomo/configs"},
		{http.MethodPost, "/api/mihomo/restart"}, {http.MethodPost, "/api/mihomo/upgrade"},
		{http.MethodPost, "/api/mihomo/upgrade/ui"}, {http.MethodGet, "/api/mihomo/debug/gc"},
	} {
		response := request(item.method, item.path, nil)
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("%s %s = %d, want 404", item.method, item.path, response.StatusCode)
		}
		_ = response.Body.Close()
	}
	response = request(http.MethodDelete, "/api/mihomo/connections", nil)
	if response.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("close all without confirmation = %d", response.StatusCode)
	}
	_ = response.Body.Close()
	response = request(http.MethodDelete, "/api/mihomo/connections", map[string]string{"Origin": endpoint, "X-SurgeEB-Confirm": "close-all-connections"})
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("confirmed close all = %d", response.StatusCode)
	}
	_ = response.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	header := http.Header{"Authorization": []string{"Bearer management-token-1234567890"}, "Origin": []string{endpoint}}
	connection, _, err := websocket.Dial(ctx, strings.Replace(endpoint, "http://", "ws://", 1)+"/api/mihomo/connections", &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	_, payload, err := connection.Read(ctx)
	_ = connection.Close(websocket.StatusNormalClosure, "done")
	if err != nil || !json.Valid(payload) {
		t.Fatalf("authenticated WebSocket facade failed: %v %q", err, payload)
	}
	streamDeadline := time.Now().Add(3 * time.Second)
	for server.core.streams.Load() != 0 && time.Now().Before(streamDeadline) {
		time.Sleep(time.Millisecond)
	}
	if active := server.core.streams.Load(); active != 0 {
		t.Fatalf("closed WebSocket retained %d active streams", active)
	}

	server.core.streams.Store(maxControllerStreams)
	limitedCtx, limitedCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer limitedCancel()
	limitedHeader := http.Header{"Authorization": []string{"Bearer management-token-1234567890"}, "Origin": []string{endpoint}}
	limited, response, err := websocket.Dial(limitedCtx, strings.Replace(endpoint, "http://", "ws://", 1)+"/api/mihomo/logs", &websocket.DialOptions{HTTPHeader: limitedHeader})
	if limited != nil {
		_ = limited.Close(websocket.StatusNormalClosure, "unexpected")
	}
	if err == nil || response == nil || response.StatusCode != http.StatusTooManyRequests {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("stream limit = %d, err=%v", status, err)
	}
	_ = response.Body.Close()
	server.core.streams.Store(0)

	response, err = http.Get(endpoint + "/api/overview")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", response.StatusCode)
	}
	if policy := response.Header.Get("Content-Security-Policy"); !strings.Contains(policy, "style-src 'self' 'unsafe-inline'") || !strings.Contains(policy, "script-src 'self'") {
		t.Fatalf("unexpected Content-Security-Policy: %q", policy)
	}
	_ = response.Body.Close()
	req, _ := http.NewRequest(http.MethodGet, endpoint+"/api/overview", nil)
	req.Host = "attacker.example"
	req.Header.Set("Authorization", "Bearer management-token-1234567890")
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("untrusted local Host status = %d", response.StatusCode)
	}
	_ = response.Body.Close()
	req, _ = http.NewRequest(http.MethodGet, endpoint+"/api/overview", nil)
	req.Header.Set("Authorization", "Bearer management-token-1234567890")
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authorized status = %d", response.StatusCode)
	}
	cookies := response.Cookies()
	_ = response.Body.Close()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("management cookie is not strict: %+v", cookies)
	}
	req, _ = http.NewRequest(http.MethodGet, endpoint+"/api/overview", nil)
	req.AddCookie(cookies[0])
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("cookie authorization status = %d", response.StatusCode)
	}
	_ = response.Body.Close()

	publicPolicySettings := application.Config().Settings()
	publicPolicySettings.PolicyToken = "unsafe"
	if err := application.UpdateSettings(publicPolicySettings); err != nil {
		t.Fatal(err)
	}
	req, _ = http.NewRequest(http.MethodGet, endpoint+"/api/settings", nil)
	req.Header.Set("Authorization", "Bearer management-token-1234567890")
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.NewDecoder(response.Body).Decode(&settings); err != nil {
		_ = response.Body.Close()
		t.Fatal(err)
	}
	_ = response.Body.Close()
	for _, required := range []string{"virtual_host", "projection_key", "version", "core_version", "gateway_state", "data_directory_protected", "configuration_protected", "controller_key_protected"} {
		if _, ok := settings[required]; !ok {
			t.Fatalf("settings DTO omitted %q: %#v", required, settings)
		}
	}
	if settings["projection_key"] != application.Config().ProjectionKey {
		t.Fatalf("settings DTO did not expose the configured projection key: %#v", settings)
	}
	if settings["policy_token"] != application.Config().PolicyToken {
		t.Fatalf("settings DTO did not expose the configured Policy Token: %#v", settings)
	}
	if _, ok := settings["management_token"]; ok {
		t.Fatalf("settings DTO exposed the Management Token: %#v", settings)
	}
	for _, removed := range []string{"socks_advertise", "policy_base_url"} {
		if _, ok := settings[removed]; ok {
			t.Fatalf("settings DTO retained independent publication field %q: %#v", removed, settings)
		}
	}
	settingsEncoded, _ := json.Marshal(settings)
	if strings.Contains(string(settingsEncoded), application.DataDir()) {
		t.Fatalf("settings DTO exposed the data directory: %s", settingsEncoded)
	}
	nextSettings := application.Config().Settings()
	nextSettings.ProjectionKey = "shared-projection-key-updated-through-api"
	settingsBody, _ := json.Marshal(nextSettings)
	req, _ = http.NewRequest(http.MethodPut, endpoint+"/api/settings", bytes.NewReader(settingsBody))
	req.Header.Set("Authorization", "Bearer management-token-1234567890")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", endpoint)
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		t.Fatalf("projection_key settings update status=%d body=%s", response.StatusCode, body)
	}
	_ = response.Body.Close()
	if application.Config().ProjectionKey != nextSettings.ProjectionKey {
		t.Fatal("projection_key settings update was not applied")
	}
	req, _ = http.NewRequest(http.MethodGet, endpoint+"/api/service", nil)
	req.Header.Set("Authorization", "Bearer management-token-1234567890")
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var service map[string]any
	if err := json.NewDecoder(response.Body).Decode(&service); err != nil {
		_ = response.Body.Close()
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if _, exposed := service["path"]; exposed {
		t.Fatalf("service DTO exposed a local path: %#v", service)
	}
}

func TestConfiguredVirtualHostPassesHTTPHostBoundary(t *testing.T) {
	application, _, endpoint := testManagementServer(t, "")
	defer application.Close()
	settings := application.Config().Settings()
	settings.VirtualHost = "surge.eb"
	if err := application.UpdateSettings(settings); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		host string
		want int
	}{
		{host: "surge.eb:18080", want: http.StatusOK},
		{host: "SURGE.EB.:18080", want: http.StatusOK},
		{host: "127.0.0.1:18080", want: http.StatusOK},
		{host: "other.eb:18080", want: http.StatusMisdirectedRequest},
	}
	for _, test := range tests {
		req, _ := http.NewRequest(http.MethodGet, endpoint+"/health", nil)
		req.Host = test.host
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != test.want {
			t.Fatalf("Host %q status=%d, want %d", test.host, response.StatusCode, test.want)
		}
	}
}

func TestNodeHealthCheckTranslatesPublicPostToMihomoGet(t *testing.T) {
	application, server, endpoint := testManagementServer(t, "management-token-1234567890")
	defer application.Close()
	_, err := application.AddProvider(gateway.Provider{
		Name: "Health Provider", Type: "inline", Enabled: true,
		Payload: []map[string]any{{
			"name": "Health Node / 100%", "type": "vless", "server": "127.0.0.1", "port": 65530,
			"uuid": "11111111-1111-4111-8111-111111111111", "network": "tcp", "tls": false,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := application.Snapshot().Entries()[0]

	var upstream *http.Request
	server.core.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		upstream = request.Clone(request.Context())
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"delay":12}`)),
			Request:    request,
		}, nil
	})

	request, err := http.NewRequest(http.MethodPost, endpoint+"/api/nodes/"+entry.PublicID+"/healthcheck", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer management-token-1234567890")
	request.Header.Set("Origin", endpoint)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("node healthcheck status=%d body=%s", response.StatusCode, body)
	}
	if upstream == nil || upstream.Method != http.MethodGet {
		t.Fatalf("Mihomo healthcheck method=%v, want GET", upstream)
	}
	if got := upstream.URL.Query().Get("url"); got != application.Config().NodeTestURL {
		t.Fatalf("Mihomo healthcheck url=%q", got)
	}
	if got := upstream.URL.Query().Get("timeout"); got != fmt.Sprint(application.Config().NodeTestTimeout*1000) {
		t.Fatalf("Mihomo healthcheck timeout=%q", got)
	}
	if got := upstream.URL.Query().Get("expected"); got != "200-399" {
		t.Fatalf("Mihomo healthcheck expected=%q", got)
	}
}

func TestStructuredLogRedaction(t *testing.T) {
	config := mustDefaultGatewayConfig(t)
	config.ManagementToken = "management-secret-value"
	config.PolicyToken = "policy-secret-value"
	config.Providers = []gateway.Provider{{StableID: "p1", Name: "provider", Type: "http", URL: "https://user:pass@example.com/sub?token=abc", Headers: map[string][]string{"Authorization": {"Bearer upstream-secret"}}}}
	record := map[string]any{"message": "uuid=550e8400-e29b-41d4-a716-446655440000 Authorization=Bearer-secret https://example.com/subscription-secret?token=abc /tmp/private /Applications/Secret.app/Contents/MacOS/client management-secret-value upstream-secret"}
	redactLogRecord(record, config, "/tmp/private")
	encoded, _ := json.Marshal(record)
	text := string(encoded)
	for _, forbidden := range []string{"550e8400", "Bearer-secret", "subscription-secret", "token=abc", "/tmp/private", "/Applications/Secret.app", "management-secret-value", "upstream-secret"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("redacted log retained %q: %s", forbidden, text)
		}
	}
}

func TestPublicDTOsRedactSecretsAndExposeOnlySecurityConclusions(t *testing.T) {
	provider := gateway.Provider{
		StableID: "provider-1", Name: "Provider", Type: "http", Enabled: false,
		URL: "https://user:password@example.com/subscription-secret-123?token=secret", FilePath: "/private/provider.yaml",
		Headers: map[string][]string{"Authorization": {"Bearer upstream-secret"}},
	}
	public := makePublicProvider(provider)
	encoded, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"password", "subscription-secret-123", "token=secret", "/private/provider.yaml", "upstream-secret"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("public Provider DTO retained %q: %s", forbidden, text)
		}
	}
	for _, required := range []string{"https://example.com/…", "Authorization"} {
		if !strings.Contains(text, required) {
			t.Fatalf("public Provider DTO omitted safe field %q: %s", required, text)
		}
	}
}

func TestControllerValueSanitizesNestedSensitiveFields(t *testing.T) {
	config := mustDefaultGatewayConfig(t)
	config.ManagementToken = "management-secret-value"
	value := map[string]any{
		"connections": []any{map[string]any{
			"metadata": map[string]any{"processPath": "/private/bin/client", "host": "example.com"},
			"secret":   "upstream-secret", "message": "management-secret-value",
		}},
	}
	sanitizeControllerValue(value, "", config, "/private")
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"/private", "upstream-secret", "management-secret-value"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("sanitized Controller value retained %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "example.com") {
		t.Fatalf("sanitization removed an expected safe field: %s", text)
	}
}

func TestConnectionDTOProjectsProviderNamesAndStableIDs(t *testing.T) {
	providers := []gateway.Provider{{StableID: "provider-stable-id", Name: "Airport A"}}
	key, err := coreProviderKey(providers[0].StableID)
	if err != nil {
		t.Fatal(err)
	}
	value := map[string]any{"connections": []any{map[string]any{
		"id": "connection-id", "providerChains": []any{key, "unknown-layer"},
	}}}
	shaped := shapeControllerDTO("/connections", value, providers).(map[string]any)
	connection := shaped["connections"].([]any)[0].(map[string]any)
	chain := connection["providerChains"].([]any)
	if len(chain) != 2 || chain[0] != "Airport A" || chain[1] != "unknown-layer" {
		t.Fatalf("unexpected projected Provider chain: %#v", chain)
	}
	ids := connection["providerIDs"].([]any)
	if len(ids) != 1 || ids[0] != "provider-stable-id" {
		t.Fatalf("unexpected stable Provider IDs: %#v", ids)
	}
	untouched := shapeControllerDTO("/traffic", value, providers).(map[string]any)
	untouched["marker"] = "same-value"
	if value["marker"] != "same-value" {
		t.Fatal("non-connection Controller DTO was replaced")
	}
}

func TestSameOriginRequiresMatchingSchemeAndHost(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:18080/api/settings", nil)
	request.Header.Set("Origin", "http://127.0.0.1:18080")
	if !sameOrigin(request) {
		t.Fatal("matching HTTP origin was rejected")
	}
	request.Header.Set("Origin", "https://127.0.0.1:18080")
	if sameOrigin(request) {
		t.Fatal("cross-scheme origin was accepted")
	}
	request.Header.Set("Origin", "http://attacker.example")
	if sameOrigin(request) {
		t.Fatal("cross-host origin was accepted")
	}
}

func testManagementServer(t *testing.T, token string) (*gateway.App, *Server, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "surgeeb-mgt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	config := mustDefaultGatewayConfig(t)
	config.SocksPort = freePort(t)
	config.ManagementToken = token
	if err := gateway.NewStore(dir).Save(config); err != nil {
		t.Fatal(err)
	}
	application, err := gateway.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(application)
	if err != nil {
		application.Close()
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.server.Handler)
	t.Cleanup(httpServer.Close)
	return application, server, httpServer.URL
}

func freePort(t *testing.T) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	if port < 1 || port > 65535 {
		t.Fatal(fmt.Errorf("invalid free port %d", port))
	}
	return uint16(port)
}
