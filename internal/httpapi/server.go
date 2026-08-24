package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ssfun/vless2surge/internal/app"
	"github.com/ssfun/vless2surge/internal/domain"
	serviceManager "github.com/ssfun/vless2surge/internal/service"
	"github.com/ssfun/vless2surge/internal/webassets"
)

const maxRequestBody = 8 << 20

type Server struct {
	app    *app.App
	server *http.Server
}

func New(application *app.App) (*Server, error) {
	staticFS, err := fs.Sub(webassets.Static, "static")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	server := &Server{app: application}
	mux.HandleFunc("GET /health", server.health)
	mux.HandleFunc("GET /proxies", server.proxies)
	mux.HandleFunc("GET /api/overview", server.authorize(server.overview))
	mux.HandleFunc("GET /api/settings", server.authorize(server.settings))
	mux.HandleFunc("PUT /api/settings", server.authorize(server.updateSettings))
	mux.HandleFunc("GET /api/subscriptions", server.authorize(server.subscriptions))
	mux.HandleFunc("POST /api/subscriptions", server.authorize(server.addSubscription))
	mux.HandleFunc("POST /api/subscriptions/paste", server.authorize(server.pasteSubscription))
	mux.HandleFunc("POST /api/subscriptions/import", server.authorize(server.importProviders))
	mux.HandleFunc("PUT /api/subscriptions/{id}", server.authorize(server.updateSubscription))
	mux.HandleFunc("DELETE /api/subscriptions/{id}", server.authorize(server.deleteSubscription))
	mux.HandleFunc("POST /api/subscriptions/{id}/refresh", server.authorize(server.refreshSubscription))
	mux.HandleFunc("POST /api/refresh", server.authorize(server.refreshAll))
	mux.HandleFunc("POST /api/draft/rebuild", server.authorize(server.rebuildDraft))
	mux.HandleFunc("POST /api/draft/discard", server.authorize(server.discardDraft))
	mux.HandleFunc("GET /api/draft/config", server.authorize(server.draftConfig))
	mux.HandleFunc("GET /api/proxies-preview", server.authorize(server.proxiesPreview))
	mux.HandleFunc("POST /api/apply", server.authorize(server.apply))
	mux.HandleFunc("POST /api/engine/start", server.authorize(server.startEngine))
	mux.HandleFunc("POST /api/engine/stop", server.authorize(server.stopEngine))
	mux.HandleFunc("POST /api/engine/restart", server.authorize(server.restartEngine))
	mux.HandleFunc("POST /api/nodes/{id}/test", server.authorize(server.testNode))
	mux.HandleFunc("GET /api/events", server.authorize(server.events))
	mux.HandleFunc("GET /api/diagnostics", server.authorize(server.diagnostics))
	mux.HandleFunc("POST /api/credentials/rotate", server.authorize(server.rotateCredentials))
	mux.HandleFunc("GET /api/service", server.authorize(server.serviceStatus))
	mux.HandleFunc("POST /api/service/install", server.authorize(server.serviceInstall))
	mux.HandleFunc("DELETE /api/service", server.authorize(server.serviceUninstall))
	mux.Handle("/", http.FileServer(http.FS(staticFS)))
	config := application.Config()
	server.server = &http.Server{
		Addr:              config.HTTPBind,
		Handler:           securityHeaders(server.trustedHost(mux)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	return server, nil
}

func (s *Server) trustedHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		config := s.app.Config()
		bindHost, _, err := net.SplitHostPort(config.HTTPBind)
		if err == nil && config.ManagementToken == "" && isLoopbackHTTPHost(bindHost) && !isLoopbackHTTPHost(requestHostname(r.Host)) {
			writeError(w, http.StatusMisdirectedRequest, "untrusted Host header for loopback configuration console")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requestHostname(hostport string) string {
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return host
	}
	return strings.Trim(hostport, "[]")
}

func isLoopbackHTTPHost(host string) bool {
	host = strings.TrimSuffix(strings.TrimSpace(strings.Trim(host, "[]")), ".")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) ListenAndServe() error {
	err := s.server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Shutdown(ctx context.Context) error { return s.server.Shutdown(ctx) }

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	state := s.app.State()
	status := s.app.EngineStatus()
	response := map[string]any{
		"ok":               status.State == "running",
		"version":          app.Version,
		"core_version":     status.CoreVersion,
		"engine_state":     status.State,
		"applied_revision": "",
		"draft_dirty":      false,
		"safe_mode":        state.SafeMode,
		"updated_at":       state.UpdatedAt,
	}
	if state.Applied != nil {
		response["applied_revision"] = state.Applied.ID
	}
	if state.Draft != nil {
		response["draft_dirty"] = state.Applied == nil || state.Draft.ConfigHash != state.Applied.ConfigHash
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) proxies(w http.ResponseWriter, r *http.Request) {
	config := s.app.Config()
	if config.PolicyToken != "" && !constantEqual(r.URL.Query().Get("token"), config.PolicyToken) {
		writeError(w, http.StatusUnauthorized, "invalid policy token")
		return
	}
	content, revision, err := s.app.Proxies()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	etag := `"` + revision + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=60")
	w.Header().Set("ETag", etag)
	w.Header().Set("X-Vless2Surge-Revision", revision)
	_, _ = io.WriteString(w, content)
}

func (s *Server) overview(w http.ResponseWriter, _ *http.Request) {
	config := s.app.Config()
	state := s.app.State()
	status := s.app.EngineStatus()
	serviceInfo, _ := serviceManager.Status()
	subscriptions := make([]publicSubscription, 0, len(config.Subscriptions))
	for _, sub := range config.Subscriptions {
		snapshot := state.Snapshots[sub.ID]
		subscriptions = append(subscriptions, makePublicSubscription(sub, snapshot, config.RefreshSeconds))
	}
	draftSOCKS := socksEndpoint(config, state.Draft)
	appliedSOCKS := socksEndpoint(config, state.Applied)
	runtimeSOCKS := appliedSOCKS
	if state.Applied == nil {
		runtimeSOCKS = draftSOCKS
	}
	response := map[string]any{
		"version":                app.Version,
		"core_version":           status.CoreVersion,
		"engine":                 status,
		"mode":                   config.Mode,
		"socks_endpoint":         runtimeSOCKS,
		"draft_socks_endpoint":   draftSOCKS,
		"applied_socks_endpoint": appliedSOCKS,
		"policy_url":             policyURL(config),
		"subscriptions":          subscriptions,
		"draft":                  publicRevisionValue(state.Draft),
		"applied":                publicRevisionValue(state.Applied),
		"dirty":                  state.Draft != nil && (state.Applied == nil || state.Draft.ConfigHash != state.Applied.ConfigHash),
		"diff":                   revisionDiff(state.Draft, state.Applied),
		"events":                 state.Events,
		"last_error":             state.LastError,
		"safe_mode":              state.SafeMode,
		"service":                serviceInfo,
	}
	writeJSON(w, http.StatusOK, response)
}

func socksEndpoint(config domain.Config, revision *domain.Revision) string {
	host, port := config.SocksAdvertise, config.SocksPort
	if revision != nil {
		if revision.SocksAdvertise != "" {
			host = revision.SocksAdvertise
		}
		if revision.SocksPort != 0 {
			port = revision.SocksPort
		}
	}
	return net.JoinHostPort(host, strconv.Itoa(int(port)))
}

func (s *Server) settings(w http.ResponseWriter, _ *http.Request) {
	config := s.app.Config()
	writeJSON(w, http.StatusOK, publicSettingsFrom(config))
}

func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin mutation is not allowed")
		return
	}
	var request publicSettings
	if err := readJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	config := s.app.Config()
	request.apply(&config)
	if err := s.app.UpdateConfig(config); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, publicSettingsFrom(config))
}

func (s *Server) subscriptions(w http.ResponseWriter, _ *http.Request) {
	config := s.app.Config()
	state := s.app.State()
	result := make([]publicSubscription, 0, len(config.Subscriptions))
	for _, sub := range config.Subscriptions {
		result = append(result, makePublicSubscription(sub, state.Snapshots[sub.ID], config.RefreshSeconds))
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) addSubscription(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin mutation is not allowed")
		return
	}
	var sub domain.Subscription
	if err := readJSON(r, &sub); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	created, err := s.app.AddSubscription(sub)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, makePublicSubscription(created, domain.Snapshot{}, s.app.Config().RefreshSeconds))
}

type pasteSubscriptionRequest struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

func (s *Server) pasteSubscription(w http.ResponseWriter, r *http.Request) {
	var request pasteSubscriptionRequest
	if err := readJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	created, err := s.app.AddManualSubscription(request.Name, []byte(request.Content))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, makePublicSubscription(created, s.app.State().Snapshots[created.ID], s.app.Config().RefreshSeconds))
}

func (s *Server) updateSubscription(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin mutation is not allowed")
		return
	}
	id := r.PathValue("id")
	var update domain.Subscription
	if err := readJSON(r, &update); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(update.URL) == "" {
		config := s.app.Config()
		for _, existing := range config.Subscriptions {
			if existing.ID == id {
				if update.SourceType == "" {
					update.SourceType = existing.SourceType
				}
				update.URL = existing.URL
				if update.Headers == nil {
					update.Headers = existing.Headers
				}
				break
			}
		}
	}
	updated, err := s.app.UpdateSubscription(id, update)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, makePublicSubscription(updated, s.app.State().Snapshots[id], s.app.Config().RefreshSeconds))
}

func (s *Server) deleteSubscription(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin mutation is not allowed")
		return
	}
	if err := s.app.DeleteSubscription(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) importProviders(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin mutation is not allowed")
		return
	}
	content, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
	if err != nil || len(content) > maxRequestBody {
		writeError(w, http.StatusBadRequest, "invalid or oversized Clash YAML")
		return
	}
	added, err := s.app.ImportProviders(content)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result := make([]publicSubscription, 0, len(added))
	for _, sub := range added {
		result = append(result, makePublicSubscription(sub, domain.Snapshot{}, s.app.Config().RefreshSeconds))
	}
	writeJSON(w, http.StatusCreated, result)
}

func (s *Server) refreshSubscription(w http.ResponseWriter, r *http.Request) {
	if err := s.app.Refresh(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) refreshAll(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.app.RefreshAll(r.Context()))
}

func (s *Server) rebuildDraft(w http.ResponseWriter, _ *http.Request) {
	revision, err := s.app.RebuildDraft("manual")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, publicRevisionValue(revision))
}

func (s *Server) discardDraft(w http.ResponseWriter, _ *http.Request) {
	if err := s.app.DiscardDraft(); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "discarded"})
}

func (s *Server) draftConfig(w http.ResponseWriter, _ *http.Request) {
	content, err := s.app.RedactedDraftConfig()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(content)
}

func (s *Server) proxiesPreview(w http.ResponseWriter, _ *http.Request) {
	content, revision, err := s.app.Proxies()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Vless2Surge-Revision", revision)
	_, _ = io.WriteString(w, content)
}

func (s *Server) apply(w http.ResponseWriter, r *http.Request) {
	force, _ := strconv.ParseBool(r.URL.Query().Get("force"))
	if err := s.app.Apply(force); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "applied"})
}

func (s *Server) startEngine(w http.ResponseWriter, _ *http.Request) {
	if err := s.app.StartEngine(); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "running"})
}

func (s *Server) stopEngine(w http.ResponseWriter, _ *http.Request) {
	if err := s.app.StopEngine(); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

func (s *Server) restartEngine(w http.ResponseWriter, _ *http.Request) {
	if err := s.app.RestartEngine(); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "running"})
}

func (s *Server) testNode(w http.ResponseWriter, r *http.Request) {
	result, err := s.app.TestNode(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) events(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.app.State().Events)
}

func (s *Server) diagnostics(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.app.Diagnostics())
}

type rotateCredentialsRequest struct {
	NodeID string `json:"node_id,omitempty"`
}

func (s *Server) rotateCredentials(w http.ResponseWriter, r *http.Request) {
	var request rotateCredentialsRequest
	if err := readJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	count, err := s.app.RotateCredentials(request.NodeID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "rotated", "count": count})
}

func (s *Server) serviceStatus(w http.ResponseWriter, _ *http.Request) {
	info, err := serviceManager.Status()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) serviceInstall(w http.ResponseWriter, _ *http.Request) {
	info, err := serviceManager.Install(s.app.DataDir())
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) serviceUninstall(w http.ResponseWriter, _ *http.Request) {
	info, err := serviceManager.Uninstall()
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) authorize(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(r) {
			writeError(w, http.StatusForbidden, "cross-origin mutation is not allowed")
			return
		}
		config := s.app.Config()
		if config.ManagementToken == "" {
			next(w, r)
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if !constantEqual(token, config.ManagementToken) {
			writeError(w, http.StatusUnauthorized, "management token required")
			return
		}
		next(w, r)
	}
}

type publicSettings struct {
	Mode                   string   `json:"mode"`
	HTTPBind               string   `json:"http_bind"`
	SocksBind              string   `json:"socks_bind"`
	SocksPort              uint16   `json:"socks_port"`
	SocksAdvertise         string   `json:"socks_advertise"`
	PolicyBaseURL          string   `json:"policy_base_url"`
	RefreshSeconds         int      `json:"refresh_seconds"`
	UserAgent              string   `json:"user_agent"`
	NodeTestURL            *string  `json:"node_test_url"`
	NodeTestUDPAddress     *string  `json:"node_test_udp_address"`
	NodeTestTimeoutSeconds *int     `json:"node_test_timeout_seconds"`
	IncludeTypes           []string `json:"include_types"`
	ExcludeName            string   `json:"exclude_name"`
	PrefixSubscription     bool     `json:"prefix_subscription"`
	AutoApply              bool     `json:"auto_apply"`
	DropThresholdPercent   int      `json:"drop_threshold_percent"`
	ManagementProtected    bool     `json:"management_protected"`
	PolicyProtected        bool     `json:"policy_protected"`
	ManagementToken        *string  `json:"management_token,omitempty"`
	PolicyToken            *string  `json:"policy_token,omitempty"`
}

func publicSettingsFrom(config domain.Config) publicSettings {
	return publicSettings{
		Mode: config.Mode, HTTPBind: config.HTTPBind, SocksBind: config.SocksBind, SocksPort: config.SocksPort,
		SocksAdvertise: config.SocksAdvertise, PolicyBaseURL: config.PolicyBaseURL, RefreshSeconds: config.RefreshSeconds,
		UserAgent: config.UserAgent, NodeTestURL: &config.NodeTestURL, NodeTestUDPAddress: &config.NodeTestUDPAddress,
		NodeTestTimeoutSeconds: &config.NodeTestTimeoutSeconds, IncludeTypes: config.IncludeTypes, ExcludeName: config.ExcludeName,
		PrefixSubscription: config.PrefixSubscription, AutoApply: config.AutoApply,
		DropThresholdPercent: config.DropThresholdPercent, ManagementProtected: config.ManagementToken != "", PolicyProtected: config.PolicyToken != "",
	}
}

func (settings publicSettings) apply(config *domain.Config) {
	config.Mode = settings.Mode
	config.HTTPBind = settings.HTTPBind
	config.SocksBind = settings.SocksBind
	config.SocksPort = settings.SocksPort
	config.SocksAdvertise = settings.SocksAdvertise
	config.PolicyBaseURL = settings.PolicyBaseURL
	config.RefreshSeconds = settings.RefreshSeconds
	config.UserAgent = settings.UserAgent
	if settings.NodeTestURL != nil {
		config.NodeTestURL = *settings.NodeTestURL
	}
	if settings.NodeTestUDPAddress != nil {
		config.NodeTestUDPAddress = *settings.NodeTestUDPAddress
	}
	if settings.NodeTestTimeoutSeconds != nil {
		config.NodeTestTimeoutSeconds = *settings.NodeTestTimeoutSeconds
	}
	config.IncludeTypes = settings.IncludeTypes
	config.ExcludeName = settings.ExcludeName
	config.PrefixSubscription = settings.PrefixSubscription
	config.AutoApply = settings.AutoApply
	config.DropThresholdPercent = settings.DropThresholdPercent
	if settings.ManagementToken != nil {
		config.ManagementToken = *settings.ManagementToken
	}
	if settings.PolicyToken != nil {
		config.PolicyToken = *settings.PolicyToken
	}
}

type publicSubscription struct {
	ID                  string               `json:"id"`
	Name                string               `json:"name"`
	SourceType          string               `json:"source_type"`
	URLMasked           string               `json:"url_masked"`
	Filter              string               `json:"filter,omitempty"`
	Enabled             bool                 `json:"enabled"`
	HeaderNames         []string             `json:"header_names,omitempty"`
	RefreshSeconds      int                  `json:"refresh_seconds,omitempty"`
	FetchedAt           time.Time            `json:"fetched_at,omitempty"`
	LastAttemptAt       time.Time            `json:"last_attempt_at,omitempty"`
	NextRefreshAt       time.Time            `json:"next_refresh_at,omitempty"`
	RawCount            int                  `json:"raw_count"`
	UsableCount         int                  `json:"usable_count"`
	DroppedCount        int                  `json:"dropped_count"`
	LastAttemptRawCount int                  `json:"last_attempt_raw_count,omitempty"`
	LastAttemptDropped  []domain.DroppedNode `json:"last_attempt_dropped,omitempty"`
	UsingCache          bool                 `json:"using_cache"`
	LastError           string               `json:"last_error,omitempty"`
}

func makePublicSubscription(sub domain.Subscription, snapshot domain.Snapshot, defaultRefreshSeconds int) publicSubscription {
	sourceType := sub.SourceType
	if sourceType == "" {
		sourceType = "url"
	}
	masked := maskURL(sub.URL)
	if sourceType == "manual" {
		masked = "手动导入"
	}
	headerNames := make([]string, 0, len(sub.Headers))
	for key := range sub.Headers {
		headerNames = append(headerNames, key)
	}
	sort.Strings(headerNames)
	return publicSubscription{
		ID: sub.ID, Name: sub.Name, SourceType: sourceType, URLMasked: masked, Filter: sub.Filter, Enabled: sub.Enabled,
		HeaderNames:    headerNames,
		RefreshSeconds: sub.RefreshSeconds, FetchedAt: snapshot.FetchedAt, LastAttemptAt: snapshot.LastAttemptAt,
		NextRefreshAt: nextRefreshAt(sub, snapshot, defaultRefreshSeconds), RawCount: snapshot.RawCount,
		UsableCount: len(snapshot.Nodes), DroppedCount: len(snapshot.Dropped), LastAttemptRawCount: snapshot.LastAttemptRawCount,
		LastAttemptDropped: cloneDropped(snapshot.LastAttemptDropped), UsingCache: snapshot.LastError != "" && !snapshot.FetchedAt.IsZero(), LastError: snapshot.LastError,
	}
}

func cloneDropped(value []domain.DroppedNode) []domain.DroppedNode {
	if len(value) == 0 {
		return nil
	}
	return append([]domain.DroppedNode(nil), value...)
}

func nextRefreshAt(sub domain.Subscription, snapshot domain.Snapshot, defaultRefreshSeconds int) time.Time {
	if !sub.Enabled || strings.EqualFold(sub.SourceType, "manual") || snapshot.LastAttemptAt.IsZero() {
		return time.Time{}
	}
	interval := sub.RefreshSeconds
	if interval <= 0 {
		interval = defaultRefreshSeconds
	}
	if interval <= 0 {
		return time.Time{}
	}
	return snapshot.LastAttemptAt.Add(time.Duration(interval) * time.Second)
}

type publicNode struct {
	NodeID      string `json:"node_id"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
	Network     string `json:"network"`
	Security    string `json:"security"`
	Flow        string `json:"flow,omitempty"`
	AuthUser    string `json:"auth_user"`
	SourceName  string `json:"source_name"`
	TCPCapable  bool   `json:"tcp_capable"`
	UDPCapable  bool   `json:"udp_capable"`
	UDPStatus   string `json:"udp_status"`
}

type publicRevision struct {
	ID         string               `json:"id"`
	CreatedAt  time.Time            `json:"created_at"`
	Nodes      []publicNode         `json:"nodes"`
	Dropped    []domain.DroppedNode `json:"dropped"`
	Risky      bool                 `json:"risky"`
	RiskReason string               `json:"risk_reason,omitempty"`
	AppliedAt  *time.Time           `json:"applied_at,omitempty"`
}

type publicDiff struct {
	Added            []string `json:"added"`
	Removed          []string `json:"removed"`
	ConnectionChange []string `json:"connection_changed"`
	Renamed          []string `json:"renamed"`
	Credentials      []string `json:"credentials_changed"`
}

func revisionDiff(draft, applied *domain.Revision) publicDiff {
	result := publicDiff{Added: []string{}, Removed: []string{}, ConnectionChange: []string{}, Renamed: []string{}, Credentials: []string{}}
	draftByID, appliedByID := map[string]domain.RuntimeNode{}, map[string]domain.RuntimeNode{}
	if draft != nil {
		for _, node := range draft.Nodes {
			draftByID[node.NodeID] = node
		}
	}
	if applied != nil {
		for _, node := range applied.Nodes {
			appliedByID[node.NodeID] = node
		}
	}
	for id, node := range draftByID {
		previous, found := appliedByID[id]
		if !found {
			continue
		}
		if node.DisplayName != previous.DisplayName {
			result.Renamed = append(result.Renamed, previous.DisplayName+" → "+node.DisplayName)
		}
		if node.AuthUser != previous.AuthUser || node.Password != previous.Password {
			result.Credentials = append(result.Credentials, node.DisplayName)
		}
		delete(draftByID, id)
		delete(appliedByID, id)
	}

	// A connection change creates a new NodeID. Pair remaining nodes only when
	// their source and source-local name are identical; otherwise report add/remove.
	appliedBySourceName := map[string]string{}
	for id, node := range appliedByID {
		appliedBySourceName[node.SourceID+"\x00"+node.Name] = id
	}
	for id, node := range draftByID {
		key := node.SourceID + "\x00" + node.Name
		oldID, found := appliedBySourceName[key]
		if !found {
			continue
		}
		result.ConnectionChange = append(result.ConnectionChange, node.DisplayName)
		delete(draftByID, id)
		delete(appliedByID, oldID)
		delete(appliedBySourceName, key)
	}
	for _, node := range draftByID {
		result.Added = append(result.Added, node.DisplayName)
	}
	for _, node := range appliedByID {
		result.Removed = append(result.Removed, node.DisplayName)
	}
	for _, values := range [][]string{result.Added, result.Removed, result.ConnectionChange, result.Renamed, result.Credentials} {
		sort.Strings(values)
	}
	return result
}

func publicRevisionValue(revision *domain.Revision) any {
	if revision == nil {
		return nil
	}
	nodes := make([]publicNode, 0, len(revision.Nodes))
	for _, node := range revision.Nodes {
		nodes = append(nodes, publicNode{
			NodeID: node.NodeID, DisplayName: node.DisplayName, Type: node.Type, Network: node.Network,
			Security: node.Security, Flow: node.Flow, AuthUser: node.AuthUser, SourceName: node.SourceName,
			TCPCapable: true, UDPCapable: true, UDPStatus: "支持通过节点测试验证 UDP Relay 与服务端出站",
		})
	}
	return publicRevision{ID: revision.ID, CreatedAt: revision.CreatedAt, Nodes: nodes, Dropped: revision.Dropped, Risky: revision.Risky, RiskReason: revision.RiskReason, AppliedAt: revision.AppliedAt}
}

func policyURL(config domain.Config) string {
	base, err := url.Parse(config.PolicyBaseURL)
	if err != nil {
		return ""
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/proxies"
	base.Fragment = ""
	if config.PolicyToken != "" {
		query := base.Query()
		query.Set("token", config.PolicyToken)
		base.RawQuery = query.Encode()
	}
	return base.String()
}

func maskURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "••••••"
	}
	return u.Scheme + "://" + u.Host + "/••••••"
}

func readJSON(r *http.Request, target any) error {
	if r.Body == nil {
		return errors.New("request body is required")
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
	if err != nil {
		return errors.New("read request body")
	}
	if len(data) > maxRequestBody {
		return errors.New("request body is too large")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("invalid JSON: request body must contain exactly one value")
	}
	return nil
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || !strings.EqualFold(u.Host, r.Host) {
		return false
	}
	requestScheme := "http"
	if r.TLS != nil {
		requestScheme = "https"
	}
	return strings.EqualFold(u.Scheme, requestScheme)
}

func constantEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/health" {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}
