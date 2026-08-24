package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ssfun/vless2surge/internal/app"
	"github.com/ssfun/vless2surge/internal/domain"
	"github.com/ssfun/vless2surge/internal/store"
)

const (
	managementSecret = "management-super-secret"
	policySecret     = "policy-super-secret"
	passwordSecret   = "socks-super-secret"
	uuidSecret       = "11111111-1111-4111-8111-111111111111"
	realitySecret    = "reality-public-key-secret"
)

func TestManagementAuthenticationAndPublicProjections(t *testing.T) {
	application, handler := httpFixture(t)
	defer application.Close()

	unauthorized := request(handler, http.MethodGet, "/api/settings", "", "", "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("management API accepted a missing token: %d", unauthorized.Code)
	}
	settings := request(handler, http.MethodGet, "/api/settings", managementSecret, "", "")
	if settings.Code != http.StatusOK {
		t.Fatalf("authorized settings request failed: %d %s", settings.Code, settings.Body.String())
	}
	if settings.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("sensitive management response is cacheable: %+v", settings.Header())
	}
	assertDoesNotContain(t, settings.Body.String(), managementSecret, policySecret, passwordSecret, uuidSecret, realitySecret)

	subscriptions := request(handler, http.MethodGet, "/api/subscriptions", managementSecret, "", "")
	if subscriptions.Code != http.StatusOK {
		t.Fatalf("subscriptions request failed: %d %s", subscriptions.Code, subscriptions.Body.String())
	}
	assertDoesNotContain(t, subscriptions.Body.String(), "subscription-token", "header-secret", uuidSecret, passwordSecret)
	if !strings.Contains(subscriptions.Body.String(), `"header_names":["Authorization"]`) {
		t.Fatalf("subscription projection omitted safe header names: %s", subscriptions.Body.String())
	}
	if !strings.Contains(subscriptions.Body.String(), "https://feed.example/••••••") {
		t.Fatalf("subscription URL was not safely projected: %s", subscriptions.Body.String())
	}

	configPreview := request(handler, http.MethodGet, "/api/draft/config", managementSecret, "", "")
	if configPreview.Code != http.StatusOK {
		t.Fatalf("redacted config request failed: %d %s", configPreview.Code, configPreview.Body.String())
	}
	assertDoesNotContain(t, configPreview.Body.String(), uuidSecret, passwordSecret, realitySecret)
	if !strings.Contains(configPreview.Body.String(), `"password": "***"`) || !strings.Contains(configPreview.Body.String(), `"public_key": "***"`) {
		t.Fatalf("config preview was not visibly redacted: %s", configPreview.Body.String())
	}
}

func TestProxiesPolicyTokenAppliedRevisionAndETag(t *testing.T) {
	application, handler := httpFixture(t)
	defer application.Close()

	missing := request(handler, http.MethodGet, "/proxies", "", "", "")
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("policy endpoint accepted missing token: %d", missing.Code)
	}
	response := request(handler, http.MethodGet, "/proxies?token="+policySecret, "", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("policy request failed: %d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "Applied Node") || !strings.Contains(response.Body.String(), passwordSecret) || strings.Contains(response.Body.String(), "Draft Node") {
		t.Fatalf("/proxies did not reflect only the applied revision: %s", response.Body.String())
	}
	if response.Header().Get("X-Vless2Surge-Revision") != "applied-revision" || response.Header().Get("ETag") != `"applied-revision"` {
		t.Fatalf("missing revision headers: %+v", response.Header())
	}

	notModifiedRequest := httptest.NewRequest(http.MethodGet, "/proxies?token="+policySecret, nil)
	notModifiedRequest.Header.Set("If-None-Match", response.Header().Get("ETag"))
	notModified := httptest.NewRecorder()
	handler.ServeHTTP(notModified, notModifiedRequest)
	if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 {
		t.Fatalf("ETag revalidation failed: %d %q", notModified.Code, notModified.Body.String())
	}
	preview := request(handler, http.MethodGet, "/api/proxies-preview", managementSecret, "", "")
	if preview.Code != http.StatusOK || preview.Header().Get("Cache-Control") != "no-store" || preview.Header().Get("X-Vless2Surge-Revision") != "applied-revision" {
		t.Fatalf("management proxy preview failed: %d headers=%+v body=%s", preview.Code, preview.Header(), preview.Body.String())
	}
	if preview.Body.String() != response.Body.String() {
		t.Fatalf("management preview and policy endpoint diverged: preview=%q policy=%q", preview.Body.String(), response.Body.String())
	}
}

func TestEveryManagementMutationRejectsCrossOrigin(t *testing.T) {
	application, handler := httpFixture(t)
	defer application.Close()
	paths := []string{
		"/api/refresh",
		"/api/draft/rebuild",
		"/api/draft/discard",
		"/api/apply",
		"/api/engine/start",
		"/api/engine/stop",
		"/api/engine/restart",
		"/api/nodes/applied-node/test",
		"/api/service/install",
	}
	for _, path := range paths {
		response := request(handler, http.MethodPost, path, managementSecret, "http://evil.example", "")
		if response.Code != http.StatusForbidden {
			t.Errorf("cross-origin mutation %s returned %d: %s", path, response.Code, response.Body.String())
		}
	}

	crossScheme := request(handler, http.MethodPost, "/api/engine/stop", managementSecret, "https://vless2surge.local", "")
	if crossScheme.Code != http.StatusForbidden {
		t.Fatalf("cross-scheme mutation was accepted: %d", crossScheme.Code)
	}
	sameOrigin := request(handler, http.MethodPost, "/api/engine/stop", managementSecret, "http://vless2surge.local", "")
	if sameOrigin.Code != http.StatusOK {
		t.Fatalf("same-origin mutation was rejected: %d %s", sameOrigin.Code, sameOrigin.Body.String())
	}
}

func TestSecurityHeadersAndStrictJSONBody(t *testing.T) {
	application, handler := httpFixture(t)
	defer application.Close()
	page := request(handler, http.MethodGet, "/", "", "", "")
	if page.Code != http.StatusOK || page.Header().Get("Content-Security-Policy") == "" || page.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("static UI security headers are incomplete: status=%d headers=%+v", page.Code, page.Header())
	}

	payload := `{"mode":"local"} {"mode":"linux"}`
	invalid := request(handler, http.MethodPut, "/api/settings", managementSecret, "http://vless2surge.local", payload)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("multiple JSON values were accepted: %d %s", invalid.Code, invalid.Body.String())
	}
}

func TestLoopbackConsoleRejectsDNSRebindingHostWithoutToken(t *testing.T) {
	application, err := app.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	server, err := New(application)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	request.Host = "attacker.example"
	response := httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusMisdirectedRequest {
		t.Fatalf("loopback console accepted a rebinding Host: %d %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/health", nil)
	request.Host = "127.0.0.1:18080"
	response = httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("loopback console rejected its normal Host: %d %s", response.Code, response.Body.String())
	}
}

func TestManualPasteEndpointCreatesDraft(t *testing.T) {
	application, handler := httpFixture(t)
	defer application.Close()
	payload, _ := json.Marshal(map[string]string{
		"name":    "Pasted source",
		"content": "vless://22222222-2222-4222-8222-222222222222@example.com:443?type=tcp#Pasted",
	})
	response := request(handler, http.MethodPost, "/api/subscriptions/paste", managementSecret, "http://vless2surge.local", string(payload))
	if response.Code != http.StatusCreated {
		t.Fatalf("manual paste failed: %d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"source_type":"manual"`) || !strings.Contains(response.Body.String(), `"usable_count":1`) {
		t.Fatalf("manual source projection is incomplete: %s", response.Body.String())
	}
	if application.State().Draft == nil || len(application.State().Draft.Nodes) != 2 {
		t.Fatalf("manual paste did not rebuild the combined draft: %+v", application.State().Draft)
	}
}

func TestDiagnosticsAreAuthenticatedAndRedacted(t *testing.T) {
	application, handler := httpFixture(t)
	defer application.Close()
	response := request(handler, http.MethodGet, "/api/diagnostics", managementSecret, "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("diagnostics request failed: %d %s", response.Code, response.Body.String())
	}
	assertDoesNotContain(t, response.Body.String(), uuidSecret, realitySecret, passwordSecret, managementSecret, policySecret)
	if !strings.Contains(response.Body.String(), `"name":"draft"`) || !strings.Contains(response.Body.String(), `"name":"applied"`) || !strings.Contains(response.Body.String(), `"name":"engine"`) {
		t.Fatalf("diagnostic layers are incomplete: %s", response.Body.String())
	}
}

func TestNodeTestAndRestartExplainUnavailableRuntime(t *testing.T) {
	application, handler := httpFixture(t)
	defer application.Close()
	nodeResponse := request(handler, http.MethodPost, "/api/nodes/applied-node/test", managementSecret, "http://vless2surge.local", "")
	if nodeResponse.Code != http.StatusConflict || !strings.Contains(nodeResponse.Body.String(), "网关未运行") {
		t.Fatalf("node test did not explain the stopped gateway: %d %s", nodeResponse.Code, nodeResponse.Body.String())
	}
	restartResponse := request(handler, http.MethodPost, "/api/engine/restart", managementSecret, "http://vless2surge.local", "")
	if restartResponse.Code != http.StatusConflict || !strings.Contains(restartResponse.Body.String(), "not running") {
		t.Fatalf("restart did not explain the stopped Engine: %d %s", restartResponse.Code, restartResponse.Body.String())
	}
}

func TestPolicyURLPreservesBasePathAndQuery(t *testing.T) {
	config := domain.DefaultConfig()
	config.PolicyBaseURL = "https://gateway.example/private/base/?existing=1#ignored"
	config.PolicyToken = "token with spaces"
	got := policyURL(config)
	if got != "https://gateway.example/private/base/proxies?existing=1&token=token+with+spaces" {
		t.Fatalf("unexpected policy URL: %s", got)
	}
}

func TestOverviewUsesAppliedSOCKSEndpointAsRuntimeFact(t *testing.T) {
	application, handler := httpFixture(t)
	defer application.Close()
	config := application.Config()
	config.SocksAdvertise = "draft.example"
	config.SocksPort = 2090
	if err := application.UpdateConfig(config); err != nil {
		t.Fatal(err)
	}
	response := request(handler, http.MethodGet, "/api/overview", managementSecret, "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("overview failed: %d %s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["socks_endpoint"] != "127.0.0.1:1080" || body["applied_socks_endpoint"] != "127.0.0.1:1080" || body["draft_socks_endpoint"] != "draft.example:2090" {
		t.Fatalf("overview mixed draft and applied endpoints: %s", response.Body.String())
	}
	if got := socksEndpoint(domain.Config{SocksAdvertise: "::1", SocksPort: 1080}, nil); got != "[::1]:1080" {
		t.Fatalf("IPv6 endpoint is ambiguous: %q", got)
	}
}

func TestRevisionDiffClassifiesChangesWithoutSecrets(t *testing.T) {
	applied := &domain.Revision{Nodes: []domain.RuntimeNode{
		{Node: domain.Node{Name: "Rename", SourceID: "s"}, NodeID: "same-rename", DisplayName: "Old name", AuthUser: "u1", Password: "p1"},
		{Node: domain.Node{Name: "Cred", SourceID: "s"}, NodeID: "same-cred", DisplayName: "Cred", AuthUser: "u2", Password: "old-secret"},
		{Node: domain.Node{Name: "Connection", SourceID: "s"}, NodeID: "old-connection", DisplayName: "Connection", AuthUser: "u3", Password: "p3"},
		{Node: domain.Node{Name: "Removed", SourceID: "s"}, NodeID: "removed", DisplayName: "Removed"},
	}}
	draft := &domain.Revision{Nodes: []domain.RuntimeNode{
		{Node: domain.Node{Name: "Rename", SourceID: "s"}, NodeID: "same-rename", DisplayName: "New name", AuthUser: "u1", Password: "p1"},
		{Node: domain.Node{Name: "Cred", SourceID: "s"}, NodeID: "same-cred", DisplayName: "Cred", AuthUser: "new-user", Password: "new-secret"},
		{Node: domain.Node{Name: "Connection", SourceID: "s"}, NodeID: "new-connection", DisplayName: "Connection", AuthUser: "u4", Password: "p4"},
		{Node: domain.Node{Name: "Added", SourceID: "s"}, NodeID: "added", DisplayName: "Added"},
	}}
	diff := revisionDiff(draft, applied)
	if len(diff.Added) != 1 || diff.Added[0] != "Added" || len(diff.Removed) != 1 || diff.Removed[0] != "Removed" || len(diff.ConnectionChange) != 1 || len(diff.Renamed) != 1 || len(diff.Credentials) != 1 {
		t.Fatalf("unexpected revision diff: %+v", diff)
	}
	encoded, _ := json.Marshal(diff)
	if strings.Contains(string(encoded), "old-secret") || strings.Contains(string(encoded), "new-secret") {
		t.Fatalf("revision diff leaked credentials: %s", encoded)
	}
}

func TestPublicSubscriptionIncludesNextRefreshSchedule(t *testing.T) {
	attempt := time.Date(2026, 8, 24, 1, 2, 3, 0, time.UTC)
	sub := domain.Subscription{ID: "one", Name: "One", SourceType: "url", URL: "https://example.com/sub", Enabled: true}
	value := makePublicSubscription(sub, domain.Snapshot{LastAttemptAt: attempt}, 21600)
	if !value.NextRefreshAt.Equal(attempt.Add(6 * time.Hour)) {
		t.Fatalf("unexpected next refresh: %s", value.NextRefreshAt)
	}
	sub.RefreshSeconds = 3600
	value = makePublicSubscription(sub, domain.Snapshot{
		FetchedAt: attempt.Add(-time.Hour), LastAttemptAt: attempt, LastError: "no usable nodes", LastAttemptRawCount: 2,
		LastAttemptDropped: []domain.DroppedNode{{Name: "Native", Reason: "Surge 原生支持"}, {Name: "Bad", Reason: "解析错误"}},
	}, 21600)
	if !value.NextRefreshAt.Equal(attempt.Add(time.Hour)) {
		t.Fatalf("per-subscription refresh interval was ignored: %s", value.NextRefreshAt)
	}
	if !value.UsingCache || value.LastAttemptRawCount != 2 || len(value.LastAttemptDropped) != 2 {
		t.Fatalf("failed-attempt observability was not projected: %+v", value)
	}
	sub.SourceType = "manual"
	if value := makePublicSubscription(sub, domain.Snapshot{LastAttemptAt: attempt}, 21600); !value.NextRefreshAt.IsZero() {
		t.Fatalf("manual source should not have a next refresh: %s", value.NextRefreshAt)
	}
}

func TestPublicRevisionSeparatesTCPAndUDPCapability(t *testing.T) {
	value := publicRevisionValue(&domain.Revision{Nodes: []domain.RuntimeNode{{
		Node: domain.Node{Type: "vless", Name: "A"}, NodeID: "node", DisplayName: "A", AuthUser: "user",
	}}})
	revision, ok := value.(publicRevision)
	if !ok || len(revision.Nodes) != 1 {
		t.Fatalf("unexpected public revision: %#v", value)
	}
	node := revision.Nodes[0]
	if !node.TCPCapable || !node.UDPCapable || !strings.Contains(node.UDPStatus, "待实测") {
		t.Fatalf("TCP/UDP capability is not explicit: %+v", node)
	}
}

func httpFixture(t *testing.T) (*app.App, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	persistence := store.New(dir)
	config, state, err := persistence.Load()
	if err != nil {
		t.Fatal(err)
	}
	config.ManagementToken = managementSecret
	config.PolicyToken = policySecret
	config.Subscriptions = []domain.Subscription{{
		ID: "secret-sub", Name: "Secret feed", URL: "https://feed.example/path/subscription-token?access=subscription-token", Enabled: true,
		Headers: map[string]string{"Authorization": "header-secret"},
	}}
	now := time.Now().UTC()
	applied := &domain.Revision{
		ID: "applied-revision", CreatedAt: now, ConfigHash: "applied-hash", SocksBind: "127.0.0.1", SocksPort: 1080, SocksAdvertise: "127.0.0.1",
		Nodes: []domain.RuntimeNode{{
			Node:   domain.Node{Type: "vless", Name: "Applied Node", Server: "example.com", Port: 443, UUID: uuidSecret, Network: "tcp", Security: "reality", ServerName: "example.com", RealityPublicKey: realitySecret},
			NodeID: "applied-node", DisplayName: "Applied Node", AuthUser: "applied-user", Password: passwordSecret, OutboundTag: "vless-applied",
		}}, AppliedAt: &now,
	}
	draft := *applied
	draft.ID = "draft-revision"
	draft.ConfigHash = "draft-hash"
	draft.AppliedAt = nil
	draft.Nodes = append([]domain.RuntimeNode(nil), applied.Nodes...)
	draft.Nodes[0].DisplayName = "Draft Node"
	state.Applied = applied
	state.Draft = &draft
	state.Snapshots["secret-sub"] = domain.Snapshot{SubscriptionID: "secret-sub", Nodes: []domain.Node{applied.Nodes[0].Node}, RawCount: 1, FetchedAt: now}
	if err := persistence.SaveConfig(config); err != nil {
		t.Fatal(err)
	}
	if err := persistence.SaveState(&state); err != nil {
		t.Fatal(err)
	}
	application, err := app.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(application)
	if err != nil {
		_ = application.Close()
		t.Fatal(err)
	}
	return application, server.server.Handler
}

func request(handler http.Handler, method, target, bearer, origin, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Host = "vless2surge.local"
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertDoesNotContain(t *testing.T, value string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(value, secret) {
			t.Fatalf("response leaked %q: %s", secret, value)
		}
	}
	var decoded any
	_ = json.Unmarshal([]byte(value), &decoded)
}
