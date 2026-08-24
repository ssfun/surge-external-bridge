package domain

import "time"

const SchemaVersion = 1

type Config struct {
	Generation             uint64         `json:"generation"`
	Mode                   string         `json:"mode"`
	HTTPBind               string         `json:"http_bind"`
	SocksBind              string         `json:"socks_bind"`
	SocksPort              uint16         `json:"socks_port"`
	SocksAdvertise         string         `json:"socks_advertise"`
	PolicyBaseURL          string         `json:"policy_base_url"`
	RefreshSeconds         int            `json:"refresh_seconds"`
	UserAgent              string         `json:"user_agent"`
	NodeTestURL            string         `json:"node_test_url"`
	NodeTestUDPAddress     string         `json:"node_test_udp_address"`
	NodeTestTimeoutSeconds int            `json:"node_test_timeout_seconds"`
	IncludeTypes           []string       `json:"include_types"`
	ExcludeName            string         `json:"exclude_name"`
	PrefixSubscription     bool           `json:"prefix_subscription"`
	AutoApply              bool           `json:"auto_apply"`
	DropThresholdPercent   int            `json:"drop_threshold_percent"`
	ManagementToken        string         `json:"management_token,omitempty"`
	PolicyToken            string         `json:"policy_token,omitempty"`
	Subscriptions          []Subscription `json:"subscriptions"`
}

func DefaultConfig() Config {
	return Config{
		Mode:                   "local",
		HTTPBind:               "127.0.0.1:18080",
		SocksBind:              "127.0.0.1",
		SocksPort:              1080,
		SocksAdvertise:         "127.0.0.1",
		PolicyBaseURL:          "http://127.0.0.1:18080",
		RefreshSeconds:         21600,
		UserAgent:              "clash.meta",
		NodeTestURL:            "https://www.gstatic.com/generate_204",
		NodeTestUDPAddress:     "1.1.1.1:53",
		NodeTestTimeoutSeconds: 15,
		IncludeTypes:           []string{"vless"},
		ExcludeName:            "剩余流量|套餐到期|距离下次重置|过期时间|到期时间|Traffic|Expire",
		PrefixSubscription:     true,
		AutoApply:              true,
		DropThresholdPercent:   50,
	}
}

type Subscription struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	SourceType     string            `json:"source_type,omitempty"`
	URL            string            `json:"url"`
	Filter         string            `json:"filter,omitempty"`
	Enabled        bool              `json:"enabled"`
	Headers        map[string]string `json:"headers,omitempty"`
	RefreshSeconds int               `json:"refresh_seconds,omitempty"`
}

type Node struct {
	Type             string   `json:"type"`
	Name             string   `json:"name"`
	Server           string   `json:"server"`
	Port             uint16   `json:"port"`
	UUID             string   `json:"uuid"`
	Flow             string   `json:"flow,omitempty"`
	Network          string   `json:"network,omitempty"`
	Security         string   `json:"security,omitempty"`
	ServerName       string   `json:"server_name,omitempty"`
	Fingerprint      string   `json:"fingerprint,omitempty"`
	ALPN             []string `json:"alpn,omitempty"`
	RealityPublicKey string   `json:"reality_public_key,omitempty"`
	RealityShortID   string   `json:"reality_short_id,omitempty"`
	Path             string   `json:"path,omitempty"`
	Host             string   `json:"host,omitempty"`
	ServiceName      string   `json:"service_name,omitempty"`
	Insecure         bool     `json:"insecure,omitempty"`
	PacketEncoding   string   `json:"packet_encoding,omitempty"`
	SourceID         string   `json:"source_id,omitempty"`
	SourceName       string   `json:"source_name,omitempty"`
}

type DroppedNode struct {
	Name       string `json:"name"`
	SourceID   string `json:"source_id,omitempty"`
	SourceName string `json:"source_name,omitempty"`
	Type       string `json:"type,omitempty"`
	Reason     string `json:"reason"`
}

type Identity struct {
	NodeID      string    `json:"node_id"`
	Fingerprint string    `json:"fingerprint"`
	AuthUser    string    `json:"auth_user"`
	Password    string    `json:"password"`
	CreatedAt   time.Time `json:"created_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

type RuntimeNode struct {
	Node
	NodeID      string `json:"node_id"`
	DisplayName string `json:"display_name"`
	AuthUser    string `json:"auth_user"`
	Password    string `json:"password"`
	OutboundTag string `json:"outbound_tag"`
}

type Snapshot struct {
	SubscriptionID      string        `json:"subscription_id"`
	FetchedAt           time.Time     `json:"fetched_at"`
	Nodes               []Node        `json:"nodes"`
	Dropped             []DroppedNode `json:"dropped,omitempty"`
	RawCount            int           `json:"raw_count"`
	LastError           string        `json:"last_error,omitempty"`
	LastAttemptAt       time.Time     `json:"last_attempt_at"`
	LastAttemptRawCount int           `json:"last_attempt_raw_count,omitempty"`
	LastAttemptDropped  []DroppedNode `json:"last_attempt_dropped,omitempty"`
}

type Revision struct {
	ID             string        `json:"id"`
	CreatedAt      time.Time     `json:"created_at"`
	Nodes          []RuntimeNode `json:"nodes"`
	Dropped        []DroppedNode `json:"dropped,omitempty"`
	SocksBind      string        `json:"socks_bind"`
	SocksPort      uint16        `json:"socks_port"`
	SocksAdvertise string        `json:"socks_advertise"`
	ConfigHash     string        `json:"config_hash"`
	Risky          bool          `json:"risky,omitempty"`
	RiskReason     string        `json:"risk_reason,omitempty"`
	AppliedAt      *time.Time    `json:"applied_at,omitempty"`
	GeneratedBy    string        `json:"generated_by"`
}

type RuntimeState struct {
	SchemaVersion    int                 `json:"schema_version"`
	ConfigGeneration uint64              `json:"config_generation"`
	Snapshots        map[string]Snapshot `json:"snapshots"`
	Registry         map[string]Identity `json:"registry"`
	Draft            *Revision           `json:"draft,omitempty"`
	Applied          *Revision           `json:"applied,omitempty"`
	AppliedConfig    *Config             `json:"applied_config,omitempty"`
	AppliedSnapshots map[string]Snapshot `json:"applied_snapshots,omitempty"`
	AutoStart        bool                `json:"auto_start"`
	ConsecutiveCrash int                 `json:"consecutive_crash"`
	LastExitClean    bool                `json:"last_exit_clean"`
	SafeMode         bool                `json:"safe_mode"`
	LastError        string              `json:"last_error,omitempty"`
	UpdatedAt        time.Time           `json:"updated_at"`
	Events           []Event             `json:"events,omitempty"`
}

func DefaultRuntimeState() RuntimeState {
	return RuntimeState{
		SchemaVersion: SchemaVersion,
		Snapshots:     map[string]Snapshot{},
		Registry:      map[string]Identity{},
		LastExitClean: true,
		UpdatedAt:     time.Now().UTC(),
	}
}

type Event struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

type EngineStatus struct {
	State       string    `json:"state"`
	Revision    string    `json:"revision,omitempty"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	CoreVersion string    `json:"core_version"`
	Inbound     string    `json:"inbound,omitempty"`
	Users       int       `json:"users"`
	Outbounds   int       `json:"outbounds"`
	SafeMode    bool      `json:"safe_mode"`
}

type DiagnosticCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type Diagnostics struct {
	Time     time.Time         `json:"time"`
	Revision string            `json:"revision,omitempty"`
	Checks   []DiagnosticCheck `json:"checks"`
}

type NodeTestResult struct {
	NodeID    string            `json:"node_id"`
	Name      string            `json:"name"`
	Success   bool              `json:"success"`
	Stage     string            `json:"stage"`
	Detail    string            `json:"detail"`
	Target    string            `json:"target"`
	LatencyMS int64             `json:"latency_ms"`
	TestedAt  time.Time         `json:"tested_at"`
	UDP       NodeUDPTestResult `json:"udp"`
}

type NodeUDPTestResult struct {
	Success   bool   `json:"success"`
	Stage     string `json:"stage"`
	Detail    string `json:"detail"`
	Target    string `json:"target"`
	LatencyMS int64  `json:"latency_ms"`
}
