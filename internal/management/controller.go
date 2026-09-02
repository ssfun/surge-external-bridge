package management

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/ssfun/surge-external-bridge/internal/gateway"
)

const maxControllerStreams = 8
const controllerStreamWriteTimeout = 5 * time.Second

type controllerFacade struct {
	proxy   *httputil.ReverseProxy
	client  *http.Client
	secret  string
	streams atomic.Int32
}

func newControllerFacade(socket, secret string) *controllerFacade {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
		},
		ForceAttemptHTTP2: false,
		IdleConnTimeout:   30 * time.Second,
	}
	target := &url.URL{Scheme: "http", Host: "mihomo.internal"}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = transport
	previousDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		previousDirector(request)
		request.Header.Set("Authorization", "Bearer "+secret)
		request.Header.Del("Cookie")
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		writeError(w, http.StatusBadGateway, "Mihomo management backend unavailable")
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		response.Header.Del("Server")
		response.Header.Set("Cache-Control", "no-store")
		return nil
	}
	return &controllerFacade{proxy: proxy, client: &http.Client{Transport: transport}, secret: secret}
}

func (s *Server) coreRoute(upstreamPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.serveController(w, r, upstreamPath, nil)
	}
}

func (s *Server) coreJSONRoute(upstreamPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.serveControllerJSON(w, r, upstreamPath, nil)
	}
}

func (s *Server) coreConnections(w http.ResponseWriter, r *http.Request) {
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		s.serveSanitizedJSONWebSocket(w, r, "/connections", nil)
		return
	}
	s.serveControllerJSON(w, r, "/connections", nil)
}

func (s *Server) serveSanitizedJSONWebSocket(w http.ResponseWriter, r *http.Request, upstreamPath string, query url.Values) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin WebSocket is not allowed")
		return
	}
	if s.core.streams.Add(1) > maxControllerStreams {
		s.core.streams.Add(-1)
		writeError(w, http.StatusTooManyRequests, "too many Mihomo streams")
		return
	}
	defer s.core.streams.Add(-1)
	client, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{requestHost(r.Host)}})
	if err != nil {
		return
	}
	defer client.CloseNow()
	upstreamURL := &url.URL{Scheme: "ws", Host: "mihomo.internal"}
	if err := setControllerPath(upstreamURL, upstreamPath); err != nil {
		_ = client.Close(websocket.StatusPolicyViolation, "invalid Mihomo management path")
		return
	}
	if query != nil {
		upstreamURL.RawQuery = query.Encode()
	}
	header := http.Header{"Authorization": []string{"Bearer " + s.core.secret}}
	upstream, _, err := websocket.Dial(r.Context(), upstreamURL.String(), &websocket.DialOptions{HTTPClient: s.core.client, HTTPHeader: header})
	if err != nil {
		_ = client.Close(websocket.StatusInternalError, "Mihomo stream unavailable")
		return
	}
	defer upstream.CloseNow()
	for {
		messageType, payload, err := upstream.Read(r.Context())
		if err != nil {
			return
		}
		var value any
		if json.Unmarshal(payload, &value) == nil {
			sanitizeControllerValue(value, "", s.app.Config(), s.app.DataDir())
			value = shapeControllerDTO(upstreamPath, value, s.app.Config().Providers)
			payload, _ = json.Marshal(value)
		} else {
			payload = []byte(redactText(string(payload), s.app.Config(), s.app.DataDir()))
		}
		writeContext, cancel := context.WithTimeout(r.Context(), controllerStreamWriteTimeout)
		err = client.Write(writeContext, messageType, payload)
		cancel()
		if err != nil {
			return
		}
	}
}

func (s *Server) serveControllerJSON(w http.ResponseWriter, r *http.Request, upstreamPath string, query url.Values) {
	upstreamURL := &url.URL{Scheme: "http", Host: "mihomo.internal"}
	if err := setControllerPath(upstreamURL, upstreamPath); err != nil {
		writeError(w, http.StatusBadRequest, "invalid Mihomo management path")
		return
	}
	if query != nil {
		upstreamURL.RawQuery = query.Encode()
	}
	request, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL.String(), nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, "无法构造 Mihomo 管理请求")
		return
	}
	request.Header.Set("Authorization", "Bearer "+s.core.secret)
	response, err := s.core.client.Do(request)
	if err != nil {
		writeError(w, http.StatusBadGateway, "Mihomo 管理后端暂时不可用；现有 SOCKS 数据面不受影响")
		return
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 16<<20+1))
	if err != nil || len(data) > 16<<20 {
		writeError(w, http.StatusBadGateway, "Mihomo response exceeded the product limit")
		return
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := "Mihomo 操作失败"
		var upstreamError map[string]any
		if json.Unmarshal(data, &upstreamError) == nil {
			if value, ok := upstreamError["message"].(string); ok {
				message = redactText(value, s.app.Config(), s.app.DataDir())
			}
		}
		writeError(w, http.StatusBadGateway, message)
		return
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		writeError(w, http.StatusBadGateway, "Mihomo 返回了无法识别的管理响应")
		return
	}
	sanitizeControllerValue(value, "", s.app.Config(), s.app.DataDir())
	value = shapeControllerDTO(upstreamPath, value, s.app.Config().Providers)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, value)
}

// shapeControllerDTO removes Mihomo-internal Provider keys from the product
// connection view. Keep the stable public ID alongside the display name so
// the UI can filter reliably even when Provider names are edited or collide.
// Unknown chain entries are retained because they can describe a non-Provider
// layer in the real proxy chain.
func shapeControllerDTO(upstreamPath string, value any, providers []gateway.Provider) any {
	if upstreamPath != "/connections" {
		return value
	}
	root, ok := value.(map[string]any)
	if !ok {
		return value
	}
	connections, ok := root["connections"].([]any)
	if !ok {
		return value
	}
	type providerIdentity struct{ id, name string }
	identities := make(map[string]providerIdentity, len(providers))
	for _, provider := range providers {
		key, err := coreProviderKey(provider.StableID)
		if err == nil {
			identities[key] = providerIdentity{id: provider.StableID, name: provider.Name}
		}
	}
	for _, item := range connections {
		connection, ok := item.(map[string]any)
		if !ok {
			continue
		}
		chain, ok := connection["providerChains"].([]any)
		if !ok {
			continue
		}
		publicIDs := make([]any, 0, len(chain))
		for index, raw := range chain {
			key, ok := raw.(string)
			if !ok {
				continue
			}
			identity, known := identities[key]
			if !known {
				continue
			}
			chain[index] = identity.name
			publicIDs = append(publicIDs, identity.id)
		}
		if len(publicIDs) != 0 {
			connection["providerIDs"] = publicIDs
		}
	}
	return root
}

func sanitizeControllerValue(value any, key string, config gateway.Config, dataDir string) {
	sensitiveKey := regexp.MustCompile(`(?i)(authorization|cookie|secret|password|header|path|url|uuid|token)`)
	switch item := value.(type) {
	case map[string]any:
		for childKey, child := range item {
			if sensitiveKey.MatchString(childKey) {
				if child != nil {
					item[childKey] = "<redacted>"
				}
				continue
			}
			if text, ok := child.(string); ok {
				item[childKey] = redactText(text, config, dataDir)
				continue
			}
			sanitizeControllerValue(child, childKey, config, dataDir)
		}
	case []any:
		for index, child := range item {
			if text, ok := child.(string); ok {
				item[index] = redactText(text, config, dataDir)
				continue
			}
			sanitizeControllerValue(child, key, config, dataDir)
		}
	}
}

func (s *Server) coreLogs(w http.ResponseWriter, r *http.Request) {
	query := url.Values{"format": []string{"structured"}}
	if level := r.URL.Query().Get("level"); level != "" {
		switch level {
		case "debug", "info", "warning", "error", "silent":
			query.Set("level", level)
		default:
			writeError(w, http.StatusBadRequest, "invalid log level")
			return
		}
	}
	s.serveRedactedLogStream(w, r, query)
}

func (s *Server) closeConnection(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin mutation is not allowed")
		return
	}
	id := r.PathValue("id")
	if id == "" || strings.ContainsAny(id, "/\\") {
		writeError(w, http.StatusBadRequest, "invalid connection ID")
		return
	}
	s.serveController(w, r, "/connections/"+url.PathEscape(id), nil)
}

func (s *Server) closeAllConnections(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) || r.Header.Get("X-SurgeEB-Confirm") != "close-all-connections" {
		writeError(w, http.StatusPreconditionFailed, "closing all connections requires explicit confirmation")
		return
	}
	s.serveController(w, r, "/connections", nil)
}

func (s *Server) serveController(w http.ResponseWriter, r *http.Request, upstreamPath string, query url.Values) {
	isStream := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
	if isStream && !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin WebSocket is not allowed")
		return
	}
	if isStream && s.core.streams.Add(1) > maxControllerStreams {
		s.core.streams.Add(-1)
		writeError(w, http.StatusTooManyRequests, "too many Mihomo streams")
		return
	}
	if isStream {
		defer s.core.streams.Add(-1)
	}
	clone := r.Clone(r.Context())
	if err := setControllerPath(clone.URL, upstreamPath); err != nil {
		writeError(w, http.StatusBadRequest, "invalid Mihomo management path")
		return
	}
	if query == nil {
		clone.URL.RawQuery = ""
	} else {
		clone.URL.RawQuery = query.Encode()
	}
	clone.RequestURI = ""
	clone.Host = "mihomo.internal"
	clone.Header.Del("Authorization")
	clone.Header.Del("Cookie")
	s.core.proxy.ServeHTTP(w, clone)
}

// Callers construct upstreamPath from fixed allowlisted prefixes and
// url.PathEscape'd public identifiers. Preserve RawPath so proxy names that
// contain a slash or percent sign remain one Mihomo route segment.
func setControllerPath(target *url.URL, escapedPath string) error {
	decoded, err := url.PathUnescape(escapedPath)
	if err != nil {
		return err
	}
	target.Path = decoded
	if decoded != escapedPath {
		target.RawPath = escapedPath
	} else {
		target.RawPath = ""
	}
	return nil
}

func (s *Server) serveRedactedLogStream(w http.ResponseWriter, r *http.Request, query url.Values) {
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		s.serveRedactedLogWebSocket(w, r, query)
		return
	}
	if s.core.streams.Add(1) > maxControllerStreams {
		s.core.streams.Add(-1)
		writeError(w, http.StatusTooManyRequests, "too many Mihomo streams")
		return
	}
	defer s.core.streams.Add(-1)
	upstreamURL := &url.URL{Scheme: "http", Host: "mihomo.internal", Path: "/logs", RawQuery: query.Encode()}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstreamURL.String(), nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, "Mihomo management request failed")
		return
	}
	request.Header.Set("Authorization", "Bearer "+s.core.secret)
	response, err := s.core.client.Do(request)
	if err != nil {
		writeError(w, http.StatusBadGateway, "Mihomo management backend unavailable")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		writeError(w, http.StatusBadGateway, "Mihomo log stream unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	decoder := json.NewDecoder(response.Body)
	for {
		var record map[string]any
		if err := decoder.Decode(&record); err != nil {
			if !errorsIsStreamEnd(err, r.Context()) {
				return
			}
			return
		}
		redactLogRecord(record, s.app.Config(), s.app.DataDir())
		if err := json.NewEncoder(w).Encode(record); err != nil {
			return
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

func (s *Server) serveRedactedLogWebSocket(w http.ResponseWriter, r *http.Request, query url.Values) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin WebSocket is not allowed")
		return
	}
	if s.core.streams.Add(1) > maxControllerStreams {
		s.core.streams.Add(-1)
		writeError(w, http.StatusTooManyRequests, "too many Mihomo streams")
		return
	}
	defer s.core.streams.Add(-1)
	client, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{requestHost(r.Host)}})
	if err != nil {
		return
	}
	defer client.CloseNow()
	header := http.Header{"Authorization": []string{"Bearer " + s.core.secret}}
	upstream, _, err := websocket.Dial(r.Context(), "ws://mihomo.internal/logs?"+query.Encode(), &websocket.DialOptions{HTTPClient: s.core.client, HTTPHeader: header})
	if err != nil {
		_ = client.Close(websocket.StatusInternalError, "Mihomo log stream unavailable")
		return
	}
	defer upstream.CloseNow()
	for {
		messageType, payload, err := upstream.Read(r.Context())
		if err != nil {
			return
		}
		var record map[string]any
		if json.Unmarshal(payload, &record) == nil {
			redactLogRecord(record, s.app.Config(), s.app.DataDir())
			payload, _ = json.Marshal(record)
		} else {
			payload = []byte(redactText(string(payload), s.app.Config(), s.app.DataDir()))
		}
		writeContext, cancel := context.WithTimeout(r.Context(), controllerStreamWriteTimeout)
		err = client.Write(writeContext, messageType, payload)
		cancel()
		if err != nil {
			return
		}
	}
}

func errorsIsStreamEnd(err error, ctx context.Context) bool {
	return err == io.EOF || ctx.Err() != nil
}

func redactLogRecord(record map[string]any, config gateway.Config, dataDir string) {
	for key, value := range record {
		switch item := value.(type) {
		case string:
			record[key] = redactText(item, config, dataDir)
		case []any:
			for index := range item {
				if field, ok := item[index].(map[string]any); ok {
					redactLogRecord(field, config, dataDir)
				}
			}
		case map[string]any:
			redactLogRecord(item, config, dataDir)
		}
	}
}

var (
	uuidPattern       = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\b`)
	secretPairPattern = regexp.MustCompile(`(?i)\b(authorization|cookie|token|password|passwd|secret|uuid|header)\s*[:=]\s*([^\s,;]+)`)
	urlPattern        = regexp.MustCompile(`https?://[^\s"'<>]+`)
	localPathPattern  = regexp.MustCompile(`(?:/Users|/home|/tmp|/private/tmp|/var/folders|/Applications)/[^\s"'<>;,]+`)
)

func redactText(value string, config gateway.Config, dataDir string) string {
	secrets := []string{config.ManagementToken, dataDir}
	for _, path := range config.PolicyPaths {
		secrets = append(secrets, path.Token)
	}
	for _, provider := range config.Providers {
		secrets = append(secrets, provider.URL, provider.FilePath)
		for _, values := range provider.Headers {
			secrets = append(secrets, values...)
		}
	}
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "<redacted>")
			for _, fragment := range strings.Fields(secret) {
				if len(fragment) >= 8 && !strings.EqualFold(fragment, "bearer") {
					value = strings.ReplaceAll(value, fragment, "<redacted>")
				}
			}
		}
	}
	value = uuidPattern.ReplaceAllString(value, "<uuid>")
	value = secretPairPattern.ReplaceAllString(value, "$1=<redacted>")
	value = localPathPattern.ReplaceAllString(value, "<path>")
	value = urlPattern.ReplaceAllStringFunc(value, func(raw string) string {
		parsed, err := url.Parse(strings.TrimRight(raw, ".,;)"))
		if err != nil {
			return "<url>"
		}
		redacted := parsed.Scheme + "://" + parsed.Host
		if parsed.EscapedPath() != "" && parsed.EscapedPath() != "/" {
			redacted += "/…"
		}
		return redacted
	})
	return value
}
