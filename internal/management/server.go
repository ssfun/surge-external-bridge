package management

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ssfun/surge-external-bridge/internal/gateway"
	M "github.com/ssfun/surge-external-bridge/internal/mihomo"
	serviceManager "github.com/ssfun/surge-external-bridge/internal/service"
	"github.com/ssfun/surge-external-bridge/internal/webassets"
)

func coreProviderKey(id string) (string, error) { return M.ProviderKey(id) }

const maxRequestBody = 8 << 20

type Server struct {
	app          *gateway.App
	server       *http.Server
	core         *controllerFacade
	listenerMu   sync.Mutex
	listener     net.Listener
	fatal        chan error
	shutdown     chan struct{}
	shutdownOnce sync.Once
}

func New(application *gateway.App) (*Server, error) {
	staticFS, err := fs.Sub(webassets.Static, "static")
	if err != nil {
		return nil, err
	}
	controllerSocket, controllerSecret := application.ControllerAccess()
	server := &Server{app: application, core: newControllerFacade(controllerSocket, controllerSecret), fatal: make(chan error, 1), shutdown: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", server.health)
	mux.HandleFunc("GET /proxies", server.proxies)
	mux.HandleFunc("GET /api/session", server.sessionStatus)
	mux.HandleFunc("POST /api/session", server.sessionLogin)
	mux.HandleFunc("DELETE /api/session", server.sessionLogout)
	mux.HandleFunc("GET /api/overview", server.authorize(server.overview))
	mux.HandleFunc("GET /api/providers", server.authorize(server.providers))
	mux.HandleFunc("POST /api/providers", server.authorize(server.addProvider))
	mux.HandleFunc("PUT /api/providers/{id}", server.authorize(server.updateProvider))
	mux.HandleFunc("DELETE /api/providers/{id}", server.authorize(server.deleteProvider))
	mux.HandleFunc("POST /api/providers/{id}/refresh", server.authorize(server.refreshProvider))
	mux.HandleFunc("POST /api/providers/{id}/healthcheck", server.authorize(server.healthCheckProvider))
	mux.HandleFunc("GET /api/providers/{id}/runtime", server.authorize(server.providerRuntime))
	mux.HandleFunc("GET /api/providers/{id}/secrets", server.authorize(server.providerSecrets))
	mux.HandleFunc("GET /api/nodes", server.authorize(server.nodes))
	mux.HandleFunc("GET /api/nodes/{id}/runtime", server.authorize(server.nodeRuntime))
	mux.HandleFunc("POST /api/nodes/{id}/healthcheck", server.authorize(server.nodeHealthCheck))
	mux.HandleFunc("POST /api/nodes/{id}/diagnose", server.authorize(server.nodeDiagnose))
	mux.HandleFunc("GET /api/nodes/{id}/surge-line", server.authorize(server.nodeSurgeLine))
	mux.HandleFunc("GET /api/events", server.authorize(server.events))
	mux.HandleFunc("GET /api/settings", server.authorize(server.settings))
	mux.HandleFunc("PUT /api/settings", server.authorize(server.updateSettings))
	mux.HandleFunc("GET /api/service", server.authorize(server.serviceStatus))
	mux.HandleFunc("POST /api/service/install", server.authorize(server.serviceInstall))
	mux.HandleFunc("DELETE /api/service", server.authorize(server.serviceUninstall))
	mux.HandleFunc("GET /api/mihomo/version", server.authorize(server.coreJSONRoute("/version")))
	mux.HandleFunc("GET /api/mihomo/configs", server.authorize(server.coreJSONRoute("/configs")))
	mux.HandleFunc("GET /api/mihomo/connections", server.authorize(server.coreConnections))
	mux.HandleFunc("DELETE /api/mihomo/connections/{id}", server.authorize(server.closeConnection))
	mux.HandleFunc("DELETE /api/mihomo/connections", server.authorize(server.closeAllConnections))
	mux.HandleFunc("GET /api/mihomo/traffic", server.authorize(server.coreRoute("/traffic")))
	mux.HandleFunc("GET /api/mihomo/memory", server.authorize(server.coreRoute("/memory")))
	mux.HandleFunc("GET /api/mihomo/logs", server.authorize(server.coreLogs))
	for _, pattern := range []string{
		"PUT /api/mihomo/configs", "PATCH /api/mihomo/configs", "POST /api/mihomo/restart",
		"POST /api/mihomo/upgrade", "POST /api/mihomo/upgrade/ui", "GET /api/mihomo/debug/{rest...}",
		"POST /api/mihomo/debug/{rest...}",
	} {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusNotFound, "该 Mihomo 能力不在产品允许列表中")
		})
	}
	mux.Handle("/", http.FileServer(http.FS(staticFS)))
	server.server = &http.Server{
		Addr: application.Config().HTTPBind, Handler: securityHeaders(server.trustedHost(mux)),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 1 << 20,
	}
	return server, nil
}

func (s *Server) ListenAndServe() error {
	listener, err := net.Listen("tcp", s.server.Addr)
	if err != nil {
		return err
	}
	s.listenerMu.Lock()
	s.listener = listener
	s.listenerMu.Unlock()
	s.serve(listener)
	select {
	case err := <-s.fatal:
		return err
	case <-s.shutdown:
		return nil
	}
}

func (s *Server) serve(listener net.Listener) {
	go func() {
		err := s.server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			select {
			case s.fatal <- err:
			default:
			}
		}
	}()
}

func (s *Server) Shutdown(ctx context.Context) error {
	err := s.server.Shutdown(ctx)
	s.shutdownOnce.Do(func() { close(s.shutdown) })
	return err
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	status := s.app.Status()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": status.State == "running", "version": gateway.Version,
		"core_version": status.CoreVersion, "state": status.State,
		"projection_count": status.ProjectionCount, "has_error": status.LastError != "",
	})
}

func (s *Server) proxies(w http.ResponseWriter, r *http.Request) {
	config := s.app.Config()
	if config.PolicyToken != "" && !constantEqual(r.URL.Query().Get("token"), config.PolicyToken) {
		writeError(w, http.StatusUnauthorized, "invalid Policy Token")
		return
	}
	content, revision, err := s.app.Proxies()
	if err != nil {
		w.Header().Set("Cache-Control", "no-store")
		writeError(w, http.StatusServiceUnavailable, s.publicError(err))
		return
	}
	etag := `"` + revision + `"`
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("ETag", etag)
	w.Header().Set("X-SurgeEB-Projection", revision)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, content)
}

type sessionLoginRequest struct {
	Token string `json:"token"`
}

func (s *Server) sessionStatus(w http.ResponseWriter, r *http.Request) {
	token := s.app.Config().ManagementToken
	authenticated := token == "" || s.hasManagementSession(r, token)
	if token != "" && constantEqual(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), token) {
		setManagementCookie(w, r, token, 86400)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"required": token != "", "authenticated": authenticated})
}

func (s *Server) sessionLogin(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin login is not allowed")
		return
	}
	var request sessionLoginRequest
	if err := readJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	token := s.app.Config().ManagementToken
	if token != "" && !constantEqual(request.Token, token) {
		writeError(w, http.StatusUnauthorized, "invalid Management Token")
		return
	}
	if token != "" {
		setManagementCookie(w, r, token, 86400)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) sessionLogout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin logout is not allowed")
		return
	}
	setManagementCookie(w, r, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) overview(w http.ResponseWriter, _ *http.Request) {
	config := s.app.Config()
	status := s.app.Status()
	writeJSON(w, http.StatusOK, map[string]any{
		"version": gateway.Version, "core_version": status.CoreVersion, "gateway": status,
		"provider_count": len(config.Providers), "projection_count": status.ProjectionCount,
		"policy_url": policyURL(config), "process_rule": "PROCESS-NAME,SurgeEB,DIRECT",
		"socks_advertise": net.JoinHostPort(config.SocksHost, fmt.Sprint(config.SocksPort)),
	})
}

type publicProvider struct {
	StableID           string     `json:"stable_id"`
	Name               string     `json:"name"`
	Type               string     `json:"type"`
	URL                string     `json:"url,omitempty"`
	FilePath           string     `json:"file_path,omitempty"`
	Enabled            bool       `json:"enabled"`
	HeaderNames        []string   `json:"header_names,omitempty"`
	RefreshSeconds     int        `json:"refresh_seconds"`
	SizeLimit          int64      `json:"size_limit"`
	DownloadProxy      string     `json:"download_proxy,omitempty"`
	IncludeName        string     `json:"include_name,omitempty"`
	ExcludeName        string     `json:"exclude_name,omitempty"`
	HealthCheck        bool       `json:"health_check"`
	HealthCheckURL     string     `json:"health_check_url,omitempty"`
	HealthCheckSeconds int        `json:"health_check_seconds,omitempty"`
	HealthCheckTimeout int        `json:"health_check_timeout,omitempty"`
	HealthCheckLazy    bool       `json:"health_check_lazy"`
	ExpectedStatus     string     `json:"expected_status,omitempty"`
	NextRefreshAt      *time.Time `json:"next_refresh_at,omitempty"`
	LastError          string     `json:"last_error,omitempty"`
	FilteredCount      int        `json:"filtered_count,omitempty"`
	FilteredNodes      []string   `json:"filtered_nodes,omitempty"`
	HostsCount         int        `json:"hosts_count,omitempty"`
}

func makePublicProvider(provider gateway.Provider) publicProvider {
	names := make([]string, 0, len(provider.Headers))
	for name := range provider.Headers {
		names = append(names, name)
	}
	return publicProvider{
		StableID: provider.StableID, Name: provider.Name, Type: provider.Type, URL: redactURL(provider.URL),
		FilePath: "", Enabled: provider.Enabled, HeaderNames: names, RefreshSeconds: provider.RefreshSeconds,
		SizeLimit: provider.SizeLimit, IncludeName: provider.IncludeName, ExcludeName: provider.ExcludeName,
		DownloadProxy: provider.DownloadProxy,
		HealthCheck:   provider.HealthCheck, HealthCheckURL: redactURL(provider.HealthCheckURL),
		HealthCheckSeconds: provider.HealthCheckSeconds, HealthCheckTimeout: provider.HealthCheckTimeout,
		HealthCheckLazy: provider.HealthCheckLazy, ExpectedStatus: provider.ExpectedStatus,
	}
}

func (s *Server) providers(w http.ResponseWriter, _ *http.Request) {
	providers := s.app.Providers()
	result := make([]publicProvider, 0, len(providers))
	for _, provider := range providers {
		item := makePublicProvider(provider)
		nextRefresh, lastError := s.app.ProviderRuntimeState(provider.StableID)
		if !nextRefresh.IsZero() {
			item.NextRefreshAt = &nextRefresh
		}
		item.LastError = lastError
		filter := s.app.ProviderFilterState(provider.StableID)
		item.HostsCount = s.app.ProviderHostsCount(provider.StableID)
		item.FilteredCount = filter.FilteredCount()
		for _, node := range filter.FilteredNodes {
			item.FilteredNodes = append(item.FilteredNodes, node.Name)
		}
		result = append(result, item)
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) addProvider(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin mutation is not allowed")
		return
	}
	provider, uploadPath, err := s.readProviderMutation(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	created, err := s.app.AddProvider(provider)
	if err != nil {
		_ = s.app.DiscardProviderUpload(uploadPath)
		writeError(w, http.StatusBadRequest, s.publicError(err))
		return
	}
	writeJSON(w, http.StatusCreated, makePublicProvider(created))
}

func (s *Server) updateProvider(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin mutation is not allowed")
		return
	}
	provider, uploadPath, err := s.readProviderMutation(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := s.app.UpdateProvider(r.PathValue("id"), provider)
	if err != nil {
		_ = s.app.DiscardProviderUpload(uploadPath)
		writeError(w, http.StatusBadRequest, s.publicError(err))
		return
	}
	writeJSON(w, http.StatusOK, makePublicProvider(updated))
}

func (s *Server) readProviderMutation(w http.ResponseWriter, r *http.Request) (provider gateway.Provider, uploadPath string, err error) {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		err = readJSON(w, r, &provider)
		return
	}
	defer func() {
		if err != nil && uploadPath != "" {
			_ = s.app.DiscardProviderUpload(uploadPath)
			uploadPath = ""
		}
	}()
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, gateway.MaxProviderUploadSize+(1<<20))
	reader, parseErr := r.MultipartReader()
	if parseErr != nil {
		err = fmt.Errorf("invalid Provider upload: %w", parseErr)
		return
	}
	var metadata []byte
	for {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			err = fmt.Errorf("read Provider upload: %w", nextErr)
			return
		}
		switch part.FormName() {
		case "provider":
			if metadata != nil || part.FileName() != "" {
				_ = part.Close()
				err = errors.New("Provider upload must contain one provider metadata field")
				return
			}
			metadata, err = io.ReadAll(io.LimitReader(part, maxRequestBody+1))
			_ = part.Close()
			if err != nil {
				err = fmt.Errorf("read Provider upload metadata: %w", err)
				return
			}
			if len(metadata) > maxRequestBody {
				err = errors.New("Provider upload metadata is too large")
				return
			}
		case "file":
			if uploadPath != "" || part.FileName() == "" {
				_ = part.Close()
				err = errors.New("Provider upload must contain one file")
				return
			}
			uploadPath, err = s.app.StoreProviderUpload(part)
			_ = part.Close()
			if err != nil {
				return
			}
		default:
			name := part.FormName()
			_ = part.Close()
			err = fmt.Errorf("unexpected Provider upload field %q", name)
			return
		}
	}
	if len(metadata) == 0 || uploadPath == "" {
		err = errors.New("Provider upload requires metadata and a file")
		return
	}
	if err = decodeJSON(strings.NewReader(string(metadata)), &provider); err != nil {
		return
	}
	if provider.Type != "file" {
		err = errors.New("uploaded files can only be used with a file Provider")
		return
	}
	provider.FilePath = uploadPath
	return
}

func (s *Server) deleteProvider(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin mutation is not allowed")
		return
	}
	if err := s.app.DeleteProvider(r.PathValue("id")); err != nil {
		writeError(w, http.StatusBadRequest, s.publicError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) refreshProvider(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin mutation is not allowed")
		return
	}
	if err := s.app.RefreshProvider(r.PathValue("id")); err != nil {
		writeError(w, http.StatusBadGateway, s.publicError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) healthCheckProvider(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin mutation is not allowed")
		return
	}
	if err := s.app.HealthCheckProvider(r.PathValue("id")); err != nil {
		writeError(w, http.StatusBadGateway, s.publicError(err))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

func (s *Server) nodes(w http.ResponseWriter, _ *http.Request) {
	entries := s.app.Snapshot().Entries()
	result := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		runtime := map[string]any{}
		if encoded, err := json.Marshal(entry.Proxy); err == nil {
			_ = json.Unmarshal(encoded, &runtime)
		}
		result = append(result, map[string]any{
			"id": entry.PublicID, "name": entry.DisplayName, "proxy_name": entry.ProxyName,
			"provider_id": entry.ProviderID, "provider_name": entry.ProviderName,
			"type": entry.Proxy.Type().String(), "udp": entry.SupportUDP, "uot": entry.SupportUOT,
			"tfo": entry.Info.TFO, "mptcp": entry.Info.MPTCP, "smux": entry.Info.SMUX, "xudp": entry.Info.XUDP,
			"alive": runtime["alive"], "history": runtime["history"], "extra": runtime["extra"],
		})
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) providerRuntime(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.app.Provider(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "Provider not found")
		return
	}
	key, err := coreProviderKey(provider.StableID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid Provider identity")
		return
	}
	s.serveControllerJSON(w, r, "/providers/proxies/"+url.PathEscape(key), nil)
}

func (s *Server) providerSecrets(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-SurgeEB-Confirm") != "reveal-provider-secrets" {
		writeError(w, http.StatusPreconditionFailed, "查看订阅敏感字段需要显式确认")
		return
	}
	provider, ok := s.app.Provider(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "Provider not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": provider.URL, "headers": provider.Headers})
}

func (s *Server) nodeRuntime(w http.ResponseWriter, r *http.Request) {
	entry, ok := s.app.Node(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "Node not found")
		return
	}
	key, err := coreProviderKey(entry.ProviderID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid Provider identity")
		return
	}
	path := "/providers/proxies/" + url.PathEscape(key) + "/" + url.PathEscape(entry.ProxyName)
	s.serveControllerJSON(w, r, path, nil)
}

func (s *Server) nodeHealthCheck(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin mutation is not allowed")
		return
	}
	entry, ok := s.app.Node(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "Node not found")
		return
	}
	key, err := coreProviderKey(entry.ProviderID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid Provider identity")
		return
	}
	config := s.app.Config()
	query := url.Values{
		"url": {config.NodeTestURL}, "timeout": {fmt.Sprint(config.NodeTestTimeout * 1000)}, "expected": {"200-399"},
	}
	path := "/providers/proxies/" + url.PathEscape(key) + "/" + url.PathEscape(entry.ProxyName) + "/healthcheck"
	// The product API deliberately exposes this as a same-origin POST because it
	// starts an active probe. Mihomo v1.19.30 exposes the corresponding
	// operation as GET, so do not forward the public method unchanged.
	upstreamRequest := r.Clone(r.Context())
	upstreamRequest.Method = http.MethodGet
	s.serveControllerJSON(w, upstreamRequest, path, query)
}

func (s *Server) nodeSurgeLine(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-SurgeEB-Confirm") != "reveal-node-credential" {
		writeError(w, http.StatusPreconditionFailed, "copying a node credential requires explicit confirmation")
		return
	}
	line, err := s.app.SurgeLine(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, s.publicError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"line": line})
}

func (s *Server) nodeDiagnose(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin mutation is not allowed")
		return
	}
	result, err := s.app.TestNode(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadGateway, s.publicError(err))
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) events(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.app.Events())
}

type publicSettings struct {
	gateway.Settings
	ManagementTokenConfigured bool   `json:"management_token_configured"`
	PolicyTokenConfigured     bool   `json:"policy_token_configured"`
	Version                   string `json:"version"`
	CoreVersion               string `json:"core_version"`
	GatewayState              string `json:"gateway_state"`
	ProjectionHash            string `json:"projection_hash,omitempty"`
	ProjectionCount           int    `json:"projection_count"`
	DataDirectoryProtected    bool   `json:"data_directory_protected"`
	ConfigurationProtected    bool   `json:"configuration_protected"`
	ControllerKeyProtected    bool   `json:"controller_key_protected"`
	SuggestedGatewayHost      string `json:"suggested_gateway_host,omitempty"`
}

func (s *Server) settings(w http.ResponseWriter, _ *http.Request) {
	settings := s.app.Config().Settings()
	managementConfigured, policyConfigured := settings.ManagementToken != "", settings.PolicyToken != ""
	settings.ManagementToken = ""
	status := s.app.Status()
	security := s.app.SecurityStatus()
	writeJSON(w, http.StatusOK, publicSettings{
		Settings: settings, ManagementTokenConfigured: managementConfigured, PolicyTokenConfigured: policyConfigured,
		Version: gateway.Version, CoreVersion: status.CoreVersion, GatewayState: status.State,
		ProjectionHash: status.ProjectionHash, ProjectionCount: status.ProjectionCount,
		DataDirectoryProtected: security.DataDirectoryProtected, ConfigurationProtected: security.ConfigurationProtected,
		ControllerKeyProtected: security.ControllerKeyProtected, SuggestedGatewayHost: suggestedGatewayHost(),
	})
}

func suggestedGatewayHost() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	available := make([]gatewayInterface, 0, len(interfaces))
	for _, networkInterface := range interfaces {
		addresses, addressErr := networkInterface.Addrs()
		if addressErr != nil {
			continue
		}
		available = append(available, gatewayInterface{
			Name: networkInterface.Name, Flags: networkInterface.Flags, Addresses: addresses,
		})
	}
	return suggestedGatewayHostFromInterfaces(available, defaultRouteSourceIPv4())
}

func defaultRouteSourceIPv4() net.IP {
	connection, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 9})
	if err != nil {
		return nil
	}
	defer connection.Close()
	local, ok := connection.LocalAddr().(*net.UDPAddr)
	if !ok {
		return nil
	}
	return local.IP.To4()
}

type gatewayInterface struct {
	Name      string
	Flags     net.Flags
	Addresses []net.Addr
}

type gatewayHostCandidate struct {
	address       string
	interfaceName string
	priority      int
}

func suggestedGatewayHostFromInterfaces(interfaces []gatewayInterface, defaultSourceIP net.IP) string {
	candidates := make([]gatewayHostCandidate, 0)
	for _, networkInterface := range interfaces {
		if networkInterface.Flags&net.FlagUp == 0 || networkInterface.Flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 || isVirtualGatewayInterface(networkInterface.Name) {
			continue
		}
		priority := gatewayInterfacePriority(networkInterface.Name)
		for _, address := range networkInterface.Addresses {
			var ip net.IP
			switch value := address.(type) {
			case *net.IPNet:
				ip = value.IP
			case *net.IPAddr:
				ip = value.IP
			}
			if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.To4() == nil || !ip.IsPrivate() {
				continue
			}
			if defaultSourceIP != nil && ip.Equal(defaultSourceIP) {
				return ip.String()
			}
			candidates = append(candidates, gatewayHostCandidate{address: ip.String(), interfaceName: networkInterface.Name, priority: priority})
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	bestPriority := 0
	for _, candidate := range candidates {
		if candidate.priority > bestPriority {
			bestPriority = candidate.priority
		}
	}
	selectedInterface := ""
	selectedAddress := ""
	for _, candidate := range candidates {
		if candidate.priority != bestPriority {
			continue
		}
		if selectedInterface == "" {
			selectedInterface = candidate.interfaceName
			selectedAddress = candidate.address
			continue
		}
		if candidate.interfaceName != selectedInterface {
			return ""
		}
	}
	return selectedAddress
}

func gatewayInterfacePriority(name string) int {
	name = strings.ToLower(name)
	for _, prefix := range []string{"en", "eth", "wl"} {
		if strings.HasPrefix(name, prefix) {
			return 2
		}
	}
	return 1
}

func isVirtualGatewayInterface(name string) bool {
	name = strings.ToLower(name)
	for _, prefix := range []string{"awdl", "br-", "bridge", "docker", "ipsec", "llw", "tailscale", "utun", "vboxnet", "veth", "virbr", "vmnet", "wg", "zt"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

type settingsRequest struct {
	Mode            string   `json:"mode"`
	HTTPBind        string   `json:"http_bind"`
	SocksBind       string   `json:"socks_bind"`
	SocksPort       uint16   `json:"socks_port"`
	SocksHost       string   `json:"socks_host"`
	PolicyHost      string   `json:"policy_host"`
	ProjectionKey   string   `json:"projection_key"`
	ManagementToken *string  `json:"management_token"`
	PolicyToken     *string  `json:"policy_token"`
	PrefixProvider  bool     `json:"prefix_provider"`
	ProjectionTypes []string `json:"projection_types"`
	NodeTestURL     string   `json:"node_test_url"`
	NodeTestUDP     string   `json:"node_test_udp_address"`
	NodeTestTimeout int      `json:"node_test_timeout_seconds"`
}

func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin mutation is not allowed")
		return
	}
	var request settingsRequest
	if err := readJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	current := s.app.Config().Settings()
	next := gateway.Settings{
		Mode: request.Mode, HTTPBind: request.HTTPBind, SocksBind: request.SocksBind, SocksPort: request.SocksPort,
		SocksHost: request.SocksHost, PolicyHost: request.PolicyHost, ProjectionKey: request.ProjectionKey,
		ManagementToken: current.ManagementToken, PolicyToken: current.PolicyToken,
		PrefixProvider: request.PrefixProvider, ProjectionTypes: request.ProjectionTypes,
		NodeTestURL: request.NodeTestURL, NodeTestUDP: request.NodeTestUDP, NodeTestTimeout: request.NodeTestTimeout,
	}
	if request.ManagementToken != nil {
		next.ManagementToken = *request.ManagementToken
	}
	if request.PolicyToken != nil {
		next.PolicyToken = *request.PolicyToken
	}
	prepared, err := s.prepareHTTPRebind(next.HTTPBind)
	if err != nil {
		writeError(w, http.StatusConflict, "新的 HTTP 监听地址不可用："+s.publicError(err))
		return
	}
	if err := s.app.UpdateSettings(next); err != nil {
		if prepared != nil {
			_ = prepared.Close()
		}
		writeError(w, http.StatusBadRequest, s.publicError(err))
		return
	}
	if prepared != nil {
		s.activateHTTPRebind(prepared)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reconnect": prepared != nil, "http_bind": next.HTTPBind})
}

func (s *Server) serviceStatus(w http.ResponseWriter, _ *http.Request) {
	status, err := serviceManager.Status()
	if err != nil {
		writeError(w, http.StatusNotImplemented, s.publicError(err))
		return
	}
	writeJSON(w, http.StatusOK, makePublicServiceInfo(status))
}

type publicServiceInfo struct {
	Platform     string `json:"platform"`
	Installed    bool   `json:"installed"`
	Active       bool   `json:"active"`
	RepairNeeded bool   `json:"repair_needed,omitempty"`
	Scope        string `json:"scope"`
}

func makePublicServiceInfo(info serviceManager.Info) publicServiceInfo {
	return publicServiceInfo{Platform: info.Platform, Installed: info.Installed, Active: info.Active, RepairNeeded: info.RepairNeeded, Scope: info.Scope}
}

func (s *Server) serviceInstall(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) || r.Header.Get("X-SurgeEB-Confirm") != "install-user-service" {
		writeError(w, http.StatusPreconditionFailed, "service installation requires explicit confirmation")
		return
	}
	status, err := serviceManager.Register(s.app.DataDir())
	if err != nil {
		writeError(w, http.StatusInternalServerError, s.publicError(err))
		return
	}
	writeJSON(w, http.StatusOK, makePublicServiceInfo(status))
}

func (s *Server) serviceUninstall(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) || r.Header.Get("X-SurgeEB-Confirm") != "uninstall-user-service" {
		writeError(w, http.StatusPreconditionFailed, "service removal requires explicit confirmation")
		return
	}
	status, err := serviceManager.Uninstall()
	if err != nil {
		writeError(w, http.StatusInternalServerError, s.publicError(err))
		return
	}
	writeJSON(w, http.StatusOK, makePublicServiceInfo(status))
}

func (s *Server) prepareHTTPRebind(address string) (net.Listener, error) {
	s.listenerMu.Lock()
	defer s.listenerMu.Unlock()
	if s.listener == nil || s.listener.Addr().String() == address {
		return nil, nil
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, httpRebindError(address, err)
	}
	return listener, nil
}

func httpRebindError(address string, err error) error {
	if !errors.Is(err, syscall.EADDRNOTAVAIL) {
		return err
	}
	_, port, splitErr := net.SplitHostPort(address)
	if splitErr != nil || port == "" {
		return errors.New("HTTP 监听地址不是本机可用地址；只能绑定本机网卡地址或 0.0.0.0")
	}
	return fmt.Errorf("HTTP 监听地址不是本机可用地址；请使用 0.0.0.0:%s 监听所有网卡，并通过本机的局域网或 Tailscale IP 访问，不能绑定 peer 地址", port)
}

func (s *Server) publicError(err error) string {
	if err == nil {
		return ""
	}
	return redactText(err.Error(), s.app.Config(), s.app.DataDir())
}

func (s *Server) activateHTTPRebind(next net.Listener) {
	s.listenerMu.Lock()
	previous := s.listener
	s.listener = next
	s.listenerMu.Unlock()
	s.serve(next)
	if previous != nil {
		_ = previous.Close()
	}
}

func (s *Server) authorize(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := s.app.Config().ManagementToken
		if token != "" {
			provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !s.hasManagementSession(r, token) {
				writeError(w, http.StatusUnauthorized, "invalid Management Token")
				return
			}
			if constantEqual(provided, token) {
				setManagementCookie(w, r, token, 86400)
			}
		}
		next(w, r)
	}
}

func (s *Server) hasManagementSession(r *http.Request, token string) bool {
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	cookie, _ := r.Cookie("surgeeb_management")
	cookieValue := ""
	if cookie != nil {
		cookieValue = cookie.Value
	}
	return constantEqual(provided, token) || constantEqual(cookieValue, managementCookieValue(token))
}

func setManagementCookie(w http.ResponseWriter, r *http.Request, token string, maxAge int) {
	value := ""
	if token != "" {
		value = managementCookieValue(token)
	}
	http.SetCookie(w, &http.Cookie{
		Name: "surgeeb_management", Value: value, Path: "/api/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: maxAge,
	})
}

func managementCookieValue(token string) string {
	digest := sha256.Sum256([]byte("surge-external-bridge-management-session\x00" + token))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func (s *Server) trustedHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		config := s.app.Config()
		if !gateway.AllowsHTTPHost(config, r.Host) {
			writeError(w, http.StatusMisdirectedRequest, "untrusted Host header")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/proxies" {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return err == nil && strings.EqualFold(parsed.Scheme, scheme) && strings.EqualFold(parsed.Host, r.Host)
}

func readJSON(w http.ResponseWriter, r *http.Request, value any) error {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	return decodeJSON(r.Body, value)
}

func decodeJSON(reader io.Reader, value any) error {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("request must contain one JSON value")
		}
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	code := "REQUEST_FAILED"
	switch status {
	case http.StatusBadRequest:
		code = "INVALID_REQUEST"
	case http.StatusUnauthorized:
		code = "UNAUTHORIZED"
	case http.StatusForbidden:
		code = "FORBIDDEN"
	case http.StatusNotFound:
		code = "NOT_FOUND"
	case http.StatusConflict, http.StatusPreconditionFailed:
		code = "CONFIRMATION_REQUIRED"
	case http.StatusTooManyRequests:
		code = "STREAM_LIMIT_REACHED"
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		code = "MIHOMO_UNAVAILABLE"
	}
	writeJSON(w, status, map[string]string{"code": code, "error": message})
}
func constantEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func requestHost(value string) string {
	if host, _, err := net.SplitHostPort(value); err == nil {
		return host
	}
	return strings.Trim(value, "[]")
}
func policyURL(config gateway.Config) string {
	base := config.PolicyBaseURL() + "/proxies"
	if config.PolicyToken == "" {
		return base
	}
	return base + "?token=" + url.QueryEscape(config.PolicyToken)
}

func redactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	parsed.User = nil
	redacted := parsed.Scheme + "://" + parsed.Host
	if parsed.EscapedPath() != "" && parsed.EscapedPath() != "/" {
		redacted += "/…"
	}
	return redacted
}
