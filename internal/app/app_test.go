package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ssfun/vless2surge/internal/domain"
	"github.com/ssfun/vless2surge/internal/store"
)

type mutableFeed struct {
	mu     sync.RWMutex
	body   string
	status int
}

func (f *mutableFeed) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	status := f.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(f.body))
}

func (f *mutableFeed) set(body string, status int) {
	f.mu.Lock()
	f.body, f.status = body, status
	f.mu.Unlock()
}

func TestRefreshFailureAndEmptyResponsePreserveSnapshot(t *testing.T) {
	feed := &mutableFeed{body: links(2)}
	server := httptest.NewServer(feed)
	defer server.Close()
	application := newTestApp(t)
	defer application.Close()
	sub := addTestSubscription(t, application, server.URL)

	if err := application.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	before := application.State().Snapshots[sub.ID]
	if len(before.Nodes) != 2 || before.LastError != "" {
		t.Fatalf("unexpected initial snapshot: %+v", before)
	}

	feed.set("\n", http.StatusOK)
	if err := application.Refresh(context.Background(), sub.ID); err == nil {
		t.Fatal("empty subscription unexpectedly replaced the snapshot")
	}
	afterEmpty := application.State().Snapshots[sub.ID]
	if len(afterEmpty.Nodes) != 2 || !afterEmpty.FetchedAt.Equal(before.FetchedAt) || afterEmpty.LastError == "" {
		t.Fatalf("empty response damaged the successful snapshot: before=%+v after=%+v", before, afterEmpty)
	}

	feed.set("upstream token=/secret", http.StatusBadGateway)
	if err := application.Refresh(context.Background(), sub.ID); err == nil {
		t.Fatal("HTTP failure unexpectedly succeeded")
	}
	afterFailure := application.State().Snapshots[sub.ID]
	if len(afterFailure.Nodes) != 2 || !afterFailure.FetchedAt.Equal(before.FetchedAt) || !strings.Contains(afterFailure.LastError, "HTTP 502") {
		t.Fatalf("HTTP failure damaged the successful snapshot: %+v", afterFailure)
	}
}

func TestBuildVersionMarkerMatchesRuntimeVersion(t *testing.T) {
	if BuildVersionMarker != "vless2surge-version:"+Version {
		t.Fatalf("inconsistent build metadata: version=%q marker=%q", Version, BuildVersionMarker)
	}
}

func TestRefreshDiscardsResponseWhenSourceChangesInFlight(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	oldServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte(links(1)))
	}))
	defer oldServer.Close()
	newServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(links(1))) }))
	defer newServer.Close()
	application := newTestApp(t)
	defer application.Close()
	sub := addTestSubscription(t, application, oldServer.URL)
	result := make(chan error, 1)
	go func() { result <- application.Refresh(context.Background(), sub.ID) }()
	<-started
	sub.URL = newServer.URL
	if _, err := application.UpdateSubscription(sub.ID, sub); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-result; err == nil || !strings.Contains(err.Error(), "changed while refresh") {
		t.Fatalf("stale refresh response was accepted: %v", err)
	}
	if snapshot := application.State().Snapshots[sub.ID]; len(snapshot.Nodes) != 0 {
		t.Fatalf("stale refresh response overwrote snapshot: %+v", snapshot)
	}
}

func TestRefreshDiscardsFailureWhenSourceChangesInFlight(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	oldServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		http.Error(w, "old source failed", http.StatusBadGateway)
	}))
	defer oldServer.Close()
	newServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(links(1))) }))
	defer newServer.Close()
	application := newTestApp(t)
	defer application.Close()
	sub := addTestSubscription(t, application, oldServer.URL)
	result := make(chan error, 1)
	go func() { result <- application.Refresh(context.Background(), sub.ID) }()
	<-started
	sub.URL = newServer.URL
	if _, err := application.UpdateSubscription(sub.ID, sub); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-result; err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("old refresh failure was not returned to its caller: %v", err)
	}
	if snapshot := application.State().Snapshots[sub.ID]; snapshot.LastError != "" || !snapshot.LastAttemptAt.IsZero() {
		t.Fatalf("stale refresh failure polluted the replacement source: %+v", snapshot)
	}
}

func TestSourceChangeKeepsSnapshotButMarksItStaleAndImmediatelyDue(t *testing.T) {
	feed := &mutableFeed{}
	feed.set(links(1), http.StatusOK)
	server := httptest.NewServer(feed)
	defer server.Close()
	application := newTestApp(t)
	sub := addTestSubscription(t, application, server.URL+"/old")
	if err := application.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	before := application.State().Snapshots[sub.ID]
	if len(before.Nodes) != 1 || before.LastError != "" {
		t.Fatalf("unexpected initial snapshot: %+v", before)
	}
	sub.URL = server.URL + "/new"
	updated, err := application.UpdateSubscription(sub.ID, sub)
	if err != nil {
		t.Fatal(err)
	}
	after := application.State().Snapshots[sub.ID]
	if updated.URL != sub.URL || len(after.Nodes) != 1 || after.LastError == "" || !after.LastAttemptAt.IsZero() {
		t.Fatalf("source change did not preserve and mark the prior snapshot: updated=%+v snapshot=%+v", updated, after)
	}
	if application.State().Draft == nil || len(application.State().Draft.Nodes) != 1 {
		t.Fatalf("stale snapshot should remain available until refresh: %+v", application.State().Draft)
	}
}

func TestNoOpRefreshKeepsEffectiveRevisionAndIdentity(t *testing.T) {
	feed := &mutableFeed{body: links(1)}
	server := httptest.NewServer(feed)
	defer server.Close()
	application := newTestApp(t)
	defer application.Close()
	sub := addTestSubscription(t, application, server.URL)

	if err := application.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	first := application.State().Draft
	time.Sleep(time.Millisecond)
	if err := application.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	second := application.State().Draft
	if first == nil || second == nil || first.ConfigHash != second.ConfigHash || first.ID != second.ID {
		t.Fatalf("no-op refresh changed effective revision: first=%+v second=%+v", first, second)
	}
	if first.Nodes[0].AuthUser != second.Nodes[0].AuthUser || first.Nodes[0].Password != second.Nodes[0].Password {
		t.Fatalf("no-op refresh changed identity: first=%+v second=%+v", first.Nodes[0], second.Nodes[0])
	}
}

func TestManualImportPersistsAndCannotBeScheduled(t *testing.T) {
	dir := t.TempDir()
	application := newTestAppAt(t, dir)
	manual, err := application.AddManualSubscription("Pasted", []byte(links(2)))
	if err != nil {
		t.Fatal(err)
	}
	if manual.SourceType != "manual" || len(application.State().Snapshots[manual.ID].Nodes) != 2 || len(application.State().Draft.Nodes) != 2 {
		t.Fatalf("manual import was not converted into a persistent snapshot: sub=%+v state=%+v", manual, application.State())
	}
	if err := application.Refresh(context.Background(), manual.ID); err == nil {
		t.Fatal("manual source unexpectedly attempted an HTTP refresh")
	}
	if results := application.RefreshAll(context.Background()); len(results) != 0 {
		t.Fatalf("manual source was included in RefreshAll: %+v", results)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := reopened.Config().Subscriptions[0]; got.SourceType != "manual" || len(reopened.State().Snapshots[got.ID].Nodes) != 2 {
		t.Fatalf("manual source did not survive restart: sub=%+v state=%+v", got, reopened.State())
	}
}

func TestCredentialRotationCreatesDraftWithoutChangingApplied(t *testing.T) {
	application := newTestApp(t)
	defer application.Close()
	if _, err := application.AddManualSubscription("Pasted", []byte(links(2))); err != nil {
		t.Fatal(err)
	}
	if err := application.Apply(false); err != nil {
		t.Fatal(err)
	}
	before := application.State().Applied
	target := before.Nodes[0]
	other := before.Nodes[1]
	proxiesBefore, _, err := application.Proxies()
	if err != nil {
		t.Fatal(err)
	}
	count, err := application.RotateCredentials(target.NodeID)
	if err != nil || count != 1 {
		t.Fatalf("single credential rotation failed: count=%d err=%v", count, err)
	}
	state := application.State()
	if state.Applied.Nodes[0].AuthUser != target.AuthUser || state.Applied.Nodes[0].Password != target.Password {
		t.Fatal("credential rotation mutated the applied revision")
	}
	var rotated, unchanged domain.RuntimeNode
	for _, node := range state.Draft.Nodes {
		if node.NodeID == target.NodeID {
			rotated = node
		}
		if node.NodeID == other.NodeID {
			unchanged = node
		}
	}
	if rotated.AuthUser == target.AuthUser || rotated.Password == target.Password {
		t.Fatalf("target identity was not rotated: before=%+v after=%+v", target, rotated)
	}
	if unchanged.AuthUser != other.AuthUser || unchanged.Password != other.Password {
		t.Fatalf("non-target identity changed: before=%+v after=%+v", other, unchanged)
	}
	if proxiesAfter, _, _ := application.Proxies(); proxiesAfter != proxiesBefore {
		t.Fatalf("unapplied rotation leaked into /proxies: before=%q after=%q", proxiesBefore, proxiesAfter)
	}
	if err := application.Apply(false); err != nil {
		t.Fatal(err)
	}
	proxiesAfter, _, err := application.Proxies()
	if err != nil || !strings.Contains(proxiesAfter, rotated.Password) || strings.Contains(proxiesAfter, target.Password) {
		t.Fatalf("applied rotation was not published: proxies=%q err=%v", proxiesAfter, err)
	}
}

func TestRotateAllSkipsRetiredRegistryIdentities(t *testing.T) {
	application := newTestApp(t)
	defer application.Close()
	if _, err := application.AddManualSubscription("Pasted", []byte(links(1))); err != nil {
		t.Fatal(err)
	}
	application.mu.Lock()
	application.state.Registry["retired"] = domain.Identity{NodeID: "retired", AuthUser: "old-user", Password: "old-password"}
	application.mu.Unlock()
	count, err := application.RotateCredentials("")
	if err != nil || count != 1 {
		t.Fatalf("rotate-all included retired identity: count=%d err=%v", count, err)
	}
	if retired := application.State().Registry["retired"]; retired.AuthUser != "old-user" || retired.Password != "old-password" {
		t.Fatalf("retired identity was rotated: %+v", retired)
	}
}

func TestDiscardDraftRestoresAppliedInputsAndKeepsControlSettings(t *testing.T) {
	application := newTestApp(t)
	defer application.Close()
	first, err := application.AddManualSubscription("Applied source", []byte(links(1)))
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Apply(false); err != nil {
		t.Fatal(err)
	}
	appliedID := application.State().Applied.ID
	config := application.Config()
	config.ManagementToken = "new-management-token"
	config.PolicyToken = "new-policy-token"
	config.UserAgent = "new-agent"
	if err := application.UpdateConfig(config); err != nil {
		t.Fatal(err)
	}
	second, err := application.AddManualSubscription("Pending source", []byte(links(1)))
	if err != nil {
		t.Fatal(err)
	}
	if len(application.State().Draft.Nodes) != 2 {
		t.Fatalf("pending source did not change draft: %+v", application.State().Draft)
	}
	if err := application.DiscardDraft(); err != nil {
		t.Fatal(err)
	}
	state, restoredConfig := application.State(), application.Config()
	if state.Applied.ID != appliedID || state.Draft.ConfigHash != state.Applied.ConfigHash || len(state.Draft.Nodes) != 1 {
		t.Fatalf("discard did not restore applied revision: %+v", state)
	}
	if len(restoredConfig.Subscriptions) != 1 || restoredConfig.Subscriptions[0].ID != first.ID {
		t.Fatalf("discard did not restore applied sources: %+v", restoredConfig.Subscriptions)
	}
	if _, found := state.Snapshots[second.ID]; found || len(state.Snapshots[first.ID].Nodes) != 1 {
		t.Fatalf("discard did not restore applied snapshots: %+v", state.Snapshots)
	}
	if restoredConfig.ManagementToken != "new-management-token" || restoredConfig.PolicyToken != "new-policy-token" || restoredConfig.UserAgent != "new-agent" {
		t.Fatalf("discard rolled back immediate control settings: %+v", restoredConfig)
	}
}

func TestProxiesAndRestartUseOnlyAppliedEndpoint(t *testing.T) {
	feed := &mutableFeed{body: links(1)}
	server := httptest.NewServer(feed)
	defer server.Close()
	dir := t.TempDir()
	application := newTestAppAt(t, dir)
	sub := addTestSubscription(t, application, server.URL)
	if err := application.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	if err := application.Apply(false); err != nil {
		t.Fatal(err)
	}
	applied := application.State().Applied
	oldContent, oldRevision, err := application.Proxies()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(oldContent, fmt.Sprintf(", %s, %d,", applied.SocksAdvertise, applied.SocksPort)) {
		t.Fatalf("initial proxy output does not use applied endpoint: %s", oldContent)
	}

	config := application.Config()
	config.SocksPort = freeAppPort(t)
	config.SocksAdvertise = "10.10.10.10"
	if err := application.UpdateConfig(config); err != nil {
		t.Fatal(err)
	}
	content, revision, err := application.Proxies()
	if err != nil {
		t.Fatal(err)
	}
	if content != oldContent || revision != oldRevision {
		t.Fatalf("unapplied endpoint leaked into /proxies: before=%q after=%q", oldContent, content)
	}
	if application.State().Draft.ConfigHash == application.State().Applied.ConfigHash {
		t.Fatal("endpoint change did not mark the draft dirty")
	}

	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	status := reopened.EngineStatus()
	if status.State != "running" || status.Inbound != net.JoinHostPort(applied.SocksBind, fmt.Sprint(applied.SocksPort)) {
		t.Fatalf("restart did not restore the applied endpoint: %+v", status)
	}
}

func TestRiskyNodeDropRequiresForceAndKeepsAppliedRevision(t *testing.T) {
	feed := &mutableFeed{body: links(4)}
	server := httptest.NewServer(feed)
	defer server.Close()
	application := newTestApp(t)
	defer application.Close()
	sub := addTestSubscription(t, application, server.URL)
	if err := application.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	if err := application.Apply(false); err != nil {
		t.Fatal(err)
	}
	oldApplied := application.State().Applied.ID

	feed.set(links(1), http.StatusOK)
	if err := application.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	state := application.State()
	if state.Draft == nil || !state.Draft.Risky || state.Applied.ID != oldApplied {
		t.Fatalf("node drop protection was not preserved: %+v", state)
	}
	if err := application.Apply(false); err == nil || application.State().Applied.ID != oldApplied {
		t.Fatalf("non-forced risky apply changed the running revision: err=%v", err)
	}
	if err := application.Apply(true); err != nil {
		t.Fatal(err)
	}
	if len(application.State().Applied.Nodes) != 1 {
		t.Fatalf("forced apply did not commit the confirmed draft: %+v", application.State().Applied)
	}
}

func TestInvalidRegexIsRejectedWithoutMutatingConfiguration(t *testing.T) {
	application := newTestApp(t)
	defer application.Close()
	before := application.Config()
	invalid := before
	invalid.ExcludeName = "["
	if err := application.UpdateConfig(invalid); err == nil {
		t.Fatal("invalid regex was accepted")
	}
	if application.Config().ExcludeName != before.ExcludeName {
		t.Fatal("rejected configuration mutated live state")
	}
}

func TestRemoteHTTPBindRequiresBothTokens(t *testing.T) {
	for _, bind := range []string{"192.168.1.2:18080", "0.0.0.0:18080", "[::]:18080"} {
		config := domain.DefaultConfig()
		config.HTTPBind = bind
		if err := ValidateConfig(config); err == nil {
			t.Fatalf("remote bind %q was accepted without tokens", bind)
		}
		config.ManagementToken = "management-token-strong"
		config.PolicyToken = "policy-token-independent"
		if err := ValidateConfig(config); err != nil {
			t.Fatalf("remote bind %q was rejected with both tokens: %v", bind, err)
		}
	}
	for _, bind := range []string{"127.0.0.1:18080", "[::1]:18080", "localhost:18080"} {
		config := domain.DefaultConfig()
		config.HTTPBind = bind
		if err := ValidateConfig(config); err != nil {
			t.Fatalf("loopback bind %q unexpectedly requires tokens: %v", bind, err)
		}
	}
}

func TestManagementAndPolicyTokensMustBeIndependentAndNonTrivial(t *testing.T) {
	config := domain.DefaultConfig()
	config.ManagementToken = "short"
	if err := ValidateConfig(config); err == nil || !strings.Contains(err.Error(), "at least 16") {
		t.Fatalf("short management token was accepted: %v", err)
	}
	config.ManagementToken = "0123456789abcdef"
	config.PolicyToken = "short"
	if err := ValidateConfig(config); err == nil || !strings.Contains(err.Error(), "at least 16") {
		t.Fatalf("short policy token was accepted: %v", err)
	}
	config.PolicyToken = config.ManagementToken
	if err := ValidateConfig(config); err == nil || !strings.Contains(err.Error(), "must be distinct") {
		t.Fatalf("shared management/policy token was accepted: %v", err)
	}
	config.PolicyToken = "fedcba9876543210"
	if err := ValidateConfig(config); err != nil {
		t.Fatalf("independent tokens were rejected: %v", err)
	}
}

func TestAdvertisedEndpointsCannotUseWildcardListeners(t *testing.T) {
	for _, advertise := range []string{"0.0.0.0", "::", "[::]"} {
		config := domain.DefaultConfig()
		config.SocksAdvertise = advertise
		if err := ValidateConfig(config); err == nil || !strings.Contains(err.Error(), "client-reachable") {
			t.Fatalf("wildcard SOCKS advertise %q was accepted: %v", advertise, err)
		}
	}
	for _, baseURL := range []string{"http://0.0.0.0:18080", "http://[::]:18080"} {
		config := domain.DefaultConfig()
		config.PolicyBaseURL = baseURL
		if err := ValidateConfig(config); err == nil || !strings.Contains(err.Error(), "client-reachable") {
			t.Fatalf("wildcard policy URL %q was accepted: %v", baseURL, err)
		}
	}
}

func TestLinuxEndpointDiagnosticWarnsAboutLoopbackAdvertise(t *testing.T) {
	application := newTestApp(t)
	defer application.Close()
	config := application.Config()
	config.Mode = "linux"
	if err := application.UpdateConfig(config); err != nil {
		t.Fatal(err)
	}
	diagnostics := application.Diagnostics()
	for _, check := range diagnostics.Checks {
		if check.Name == "endpoint" {
			if check.Status != "warn" || !strings.Contains(check.Detail, "远端 Surge 无法访问") {
				t.Fatalf("unexpected endpoint diagnostic: %+v", check)
			}
			return
		}
	}
	t.Fatal("endpoint diagnostic is missing")
}

func TestSubscriptionHeadersAreValidated(t *testing.T) {
	application := newTestApp(t)
	defer application.Close()
	for _, headers := range []map[string]string{
		{"Host": "override.example"},
		{"Content-Length": "1"},
		{"X-Bad\nHeader": "value"},
		{"X-Test": "value\r\ninjected: yes"},
	} {
		if _, err := application.AddSubscription(domain.Subscription{Name: "Bad", URL: "https://example.com/sub", Enabled: true, Headers: headers}); err == nil {
			t.Fatalf("invalid or managed headers were accepted: %+v", headers)
		}
	}
	if _, err := application.AddSubscription(domain.Subscription{Name: "Good", URL: "https://example.com/sub", Enabled: true, Headers: map[string]string{"Authorization": "Bearer secret", "X-Airport-Token": "secret"}}); err != nil {
		t.Fatalf("valid subscription headers were rejected: %v", err)
	}
}

func TestSOCKSEndpointCannotInjectSurgeConfiguration(t *testing.T) {
	for _, advertise := range []string{"host, direct", "host\nInjected = direct", "host name", "https://host"} {
		config := domain.DefaultConfig()
		config.SocksAdvertise = advertise
		if err := ValidateConfig(config); err == nil {
			t.Fatalf("unsafe SOCKS advertise value was accepted: %q", advertise)
		}
	}
	for _, advertise := range []string{"127.0.0.1", "::1", "gateway.tailnet.ts.net", "vless-gateway"} {
		config := domain.DefaultConfig()
		config.SocksAdvertise = advertise
		if err := ValidateConfig(config); err != nil {
			t.Fatalf("valid SOCKS advertise value %q was rejected: %v", advertise, err)
		}
	}
}

func TestSurgeNamesAreSafeAndUniqueAfterSanitization(t *testing.T) {
	cases := map[string]string{
		"A=B":       "A-B",
		"# comment": "Node # comment",
		"; comment": "Node ; comment",
		"\n\r\t":    "Unnamed VLESS node",
	}
	for input, expected := range cases {
		if got := sanitizeSurgeName(input); got != expected {
			t.Errorf("sanitizeSurgeName(%q)=%q, want %q", input, got, expected)
		}
	}
	application := newTestApp(t)
	defer application.Close()
	application.mu.Lock()
	application.state.Applied = &domain.Revision{ID: "names", SocksAdvertise: "127.0.0.1", SocksPort: 1080, Nodes: []domain.RuntimeNode{
		{DisplayName: "A=B", AuthUser: "one", Password: "one"},
		{DisplayName: "A-B", AuthUser: "two", Password: "two"},
		{DisplayName: "A-B [2]", AuthUser: "three", Password: "three"},
	}}
	application.mu.Unlock()
	content, _, err := application.Proxies()
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"A-B = socks5", "A-B [2] = socks5", "A-B [2] [2] = socks5"} {
		if !strings.Contains(content, expected) {
			t.Fatalf("sanitized Surge names are not unique: %s", content)
		}
	}
}

func TestThirdUncleanStartEntersSafeMode(t *testing.T) {
	dir := t.TempDir()
	persistence := store.New(dir)
	config, state, err := persistence.Load()
	if err != nil {
		t.Fatal(err)
	}
	state.LastExitClean = false
	state.ConsecutiveCrash = 2
	state.AutoStart = true
	if err := persistence.SaveConfig(config); err != nil {
		t.Fatal(err)
	}
	if err := persistence.SaveState(&state); err != nil {
		t.Fatal(err)
	}
	application, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	if !application.State().SafeMode || application.EngineStatus().State != "stopped" {
		t.Fatalf("third unclean start did not enter control-plane-only safe mode: state=%+v engine=%+v", application.State(), application.EngineStatus())
	}
}

func TestLegacyRevisionEndpointIsHydratedOnce(t *testing.T) {
	dir := t.TempDir()
	persistence := store.New(dir)
	config, state, err := persistence.Load()
	if err != nil {
		t.Fatal(err)
	}
	config.SocksPort = 11080
	config.SocksAdvertise = "10.0.0.1"
	state.Applied = &domain.Revision{ID: "legacy", Nodes: []domain.RuntimeNode{{DisplayName: "Legacy", AuthUser: "user", Password: "pass"}}}
	if err := persistence.SaveConfig(config); err != nil {
		t.Fatal(err)
	}
	if err := persistence.SaveState(&state); err != nil {
		t.Fatal(err)
	}
	application, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	hydrated := application.State().Applied
	if hydrated.SocksBind != config.SocksBind || hydrated.SocksPort != 11080 || hydrated.SocksAdvertise != "10.0.0.1" {
		t.Fatalf("legacy revision endpoint was not hydrated: %+v", hydrated)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	config.SocksPort = 12080
	config.SocksAdvertise = "10.0.0.2"
	if err := persistence.SaveConfig(config); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	stable := reopened.State().Applied
	if stable.SocksPort != 11080 || stable.SocksAdvertise != "10.0.0.1" {
		t.Fatalf("hydrated applied endpoint followed later draft config: %+v", stable)
	}
}

func newTestApp(t *testing.T) *App {
	t.Helper()
	return newTestAppAt(t, t.TempDir())
}

func newTestAppAt(t *testing.T, dir string) *App {
	t.Helper()
	application, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	config := application.Config()
	config.SocksPort = freeAppPort(t)
	if err := application.UpdateConfig(config); err != nil {
		_ = application.Close()
		t.Fatal(err)
	}
	return application
}

func addTestSubscription(t *testing.T, application *App, rawURL string) domain.Subscription {
	t.Helper()
	sub, err := application.AddSubscription(domain.Subscription{Name: "Test", URL: rawURL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

func links(count int) string {
	var result strings.Builder
	for index := 1; index <= count; index++ {
		fmt.Fprintf(&result, "vless://11111111-1111-4111-8111-%012d@example.com:%d?type=tcp#Node%%20%d\n", index, 4000+index, index)
	}
	return result.String()
}

func freeAppPort(t *testing.T) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return uint16(listener.Addr().(*net.TCPAddr).Port)
}
