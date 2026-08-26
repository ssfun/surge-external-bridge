package gateway

import (
	"crypto/rand"
	"encoding/base64"
	"net"
	"strings"
	"time"
)

const SchemaVersion = 1

const (
	ModeLocal   = "local"
	ModeGateway = "gateway"
)

type Config struct {
	SchemaVersion   int        `json:"schema_version"`
	Mode            string     `json:"mode"`
	HTTPBind        string     `json:"http_bind"`
	SocksBind       string     `json:"socks_bind"`
	SocksPort       uint16     `json:"socks_port"`
	VirtualHost     string     `json:"virtual_host"`
	ProjectionKey   string     `json:"projection_key"`
	ManagementToken string     `json:"management_token,omitempty"`
	PolicyToken     string     `json:"policy_token,omitempty"`
	PrefixProvider  bool       `json:"prefix_provider"`
	ProjectionTypes []string   `json:"projection_types"`
	NodeTestURL     string     `json:"node_test_url"`
	NodeTestUDP     string     `json:"node_test_udp_address"`
	NodeTestTimeout int        `json:"node_test_timeout_seconds"`
	Providers       []Provider `json:"providers"`
}

type Provider struct {
	StableID           string              `json:"-"`
	Name               string              `json:"name"`
	Type               string              `json:"type"`
	URL                string              `json:"url,omitempty"`
	FilePath           string              `json:"file_path,omitempty"`
	Payload            []map[string]any    `json:"payload,omitempty"`
	Enabled            bool                `json:"enabled"`
	Headers            map[string][]string `json:"headers,omitempty"`
	RefreshSeconds     int                 `json:"refresh_seconds"`
	DownloadProxy      string              `json:"download_proxy,omitempty"`
	SizeLimit          int64               `json:"size_limit,omitempty"`
	IncludeName        string              `json:"include_name,omitempty"`
	ExcludeName        string              `json:"exclude_name,omitempty"`
	HealthCheck        bool                `json:"health_check"`
	HealthCheckURL     string              `json:"health_check_url,omitempty"`
	HealthCheckSeconds int                 `json:"health_check_seconds,omitempty"`
	HealthCheckTimeout int                 `json:"health_check_timeout,omitempty"`
	HealthCheckLazy    bool                `json:"health_check_lazy"`
	ExpectedStatus     string              `json:"expected_status,omitempty"`
}

type Event struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

func DefaultConfig() (Config, error) {
	projectionKey, err := randomToken()
	if err != nil {
		return Config{}, err
	}
	return Config{
		SchemaVersion: SchemaVersion,
		Mode:          ModeLocal, HTTPBind: "127.0.0.1:18080",
		SocksBind: "127.0.0.1", SocksPort: 1080, VirtualHost: "127.0.0.1",
		ProjectionKey:   projectionKey,
		PrefixProvider:  true,
		ProjectionTypes: []string{"*"}, NodeTestURL: "https://www.gstatic.com/generate_204",
		NodeTestUDP: "8.8.8.8:53", NodeTestTimeout: 15, Providers: []Provider{},
	}, nil
}

func randomToken() (string, error) {
	key := make([]byte, 18)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(key), nil
}

type Settings struct {
	Mode            string   `json:"mode"`
	HTTPBind        string   `json:"http_bind"`
	SocksBind       string   `json:"socks_bind"`
	SocksPort       uint16   `json:"socks_port"`
	VirtualHost     string   `json:"virtual_host"`
	ProjectionKey   string   `json:"projection_key"`
	ManagementToken string   `json:"management_token,omitempty"`
	PolicyToken     string   `json:"policy_token,omitempty"`
	PrefixProvider  bool     `json:"prefix_provider"`
	ProjectionTypes []string `json:"projection_types"`
	NodeTestURL     string   `json:"node_test_url"`
	NodeTestUDP     string   `json:"node_test_udp_address"`
	NodeTestTimeout int      `json:"node_test_timeout_seconds"`
}

func (c Config) Settings() Settings {
	return Settings{
		Mode: c.Mode, HTTPBind: c.HTTPBind, SocksBind: c.SocksBind, SocksPort: c.SocksPort,
		VirtualHost: c.VirtualHost, ProjectionKey: c.ProjectionKey,
		ManagementToken: c.ManagementToken, PolicyToken: c.PolicyToken,
		PrefixProvider: c.PrefixProvider, ProjectionTypes: append([]string(nil), c.ProjectionTypes...),
		NodeTestURL: c.NodeTestURL, NodeTestUDP: c.NodeTestUDP, NodeTestTimeout: c.NodeTestTimeout,
	}
}

func (c *Config) SetSettings(settings Settings) {
	c.Mode, c.HTTPBind = settings.Mode, settings.HTTPBind
	c.SocksBind, c.SocksPort, c.VirtualHost = settings.SocksBind, settings.SocksPort, settings.VirtualHost
	c.ProjectionKey = settings.ProjectionKey
	c.ManagementToken, c.PolicyToken = settings.ManagementToken, settings.PolicyToken
	c.PrefixProvider = settings.PrefixProvider
	c.ProjectionTypes = append([]string(nil), settings.ProjectionTypes...)
	c.NodeTestURL, c.NodeTestUDP, c.NodeTestTimeout = settings.NodeTestURL, settings.NodeTestUDP, settings.NodeTestTimeout
}

// PolicyBaseURL is derived from the single published host declaration and the
// HTTP listener port so Policy Path and projected SOCKS nodes cannot drift.
func (c Config) PolicyBaseURL() string {
	_, port, err := net.SplitHostPort(c.HTTPBind)
	if err != nil || port == "" || strings.TrimSpace(c.VirtualHost) == "" {
		return ""
	}
	return "http://" + net.JoinHostPort(strings.TrimSpace(c.VirtualHost), port)
}
