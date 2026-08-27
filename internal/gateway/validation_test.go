package gateway

import "testing"

func mustDefaultConfig(t *testing.T) Config {
	t.Helper()
	config, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func TestDefaultConfigUsesProductUDPDiagnosticTarget(t *testing.T) {
	if got := mustDefaultConfig(t).NodeTestUDP; got != "8.8.8.8:53" {
		t.Fatalf("default UDP diagnostic target=%q, want 8.8.8.8:53", got)
	}
}

func TestDefaultConfigProjectsAllMihomoProviderProtocols(t *testing.T) {
	config := mustDefaultConfig(t)
	if len(config.ProjectionTypes) != 1 || config.ProjectionTypes[0] != "*" {
		t.Fatalf("default projection types=%v, want all protocols", config.ProjectionTypes)
	}
}

func TestValidateConfigNetworkBoundaries(t *testing.T) {
	validLocal := mustDefaultConfig(t)
	validGateway := mustDefaultConfig(t)
	validGateway.Mode = ModeGateway
	validGateway.HTTPBind = "0.0.0.0:18080"
	validGateway.SocksBind = "0.0.0.0"
	validGateway.SocksHost = "192.168.50.10"
	validGateway.PolicyHost = "policy.surge.eb"
	validGateway.ManagementToken = "management-token-1234"
	validGateway.PolicyToken = "policy-token-12345678"

	tests := []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{name: "local loopback", config: validLocal},
		{name: "local distinct published hostnames", config: func() Config {
			c := validLocal
			c.SocksHost, c.PolicyHost = "socks.surge.eb", "policy.surge.eb"
			return c
		}()},
		{name: "gateway private with distinct tokens", config: validGateway},
		{name: "gateway distinct published hostnames", config: func() Config {
			c := validGateway
			c.SocksHost, c.PolicyHost = "socks.surge.eb", "policy.surge.eb"
			return c
		}()},
		{name: "gateway Tailscale hostname", config: func() Config {
			c := validGateway
			c.SocksHost = "gateway.tailnet.ts.net"
			return c
		}()},
		{name: "local public HTTP bind", config: func() Config { c := validLocal; c.HTTPBind = "0.0.0.0:18080"; return c }(), wantErr: true},
		{name: "local public SOCKS IP", config: func() Config { c := validLocal; c.SocksHost = "8.8.8.8"; return c }(), wantErr: true},
		{name: "local public Policy IP", config: func() Config { c := validLocal; c.PolicyHost = "8.8.8.8"; return c }(), wantErr: true},
		{name: "gateway missing tokens", config: func() Config { c := validGateway; c.ManagementToken, c.PolicyToken = "", ""; return c }(), wantErr: true},
		{name: "gateway unsafe policy token", config: func() Config { c := validGateway; c.PolicyToken = "unsafe"; return c }(), wantErr: true},
		{name: "gateway short custom policy token", config: func() Config { c := validGateway; c.PolicyToken = "short"; return c }(), wantErr: true},
		{name: "gateway shared token", config: func() Config { c := validGateway; c.PolicyToken = c.ManagementToken; return c }(), wantErr: true},
		{name: "gateway public SOCKS IP", config: func() Config { c := validGateway; c.SocksHost = "8.8.8.8"; return c }(), wantErr: true},
		{name: "gateway public Policy IP", config: func() Config { c := validGateway; c.PolicyHost = "8.8.8.8"; return c }(), wantErr: true},
		{name: "unspecified SOCKS IP", config: func() Config { c := validGateway; c.SocksHost = "0.0.0.0"; return c }(), wantErr: true},
		{name: "Policy host with port", config: func() Config { c := validGateway; c.PolicyHost = "surge.eb:18080"; return c }(), wantErr: true},
		{name: "SOCKS host with wildcard", config: func() Config { c := validGateway; c.SocksHost = "*.eb"; return c }(), wantErr: true},
		{name: "bracketed IPv6 Policy host", config: func() Config { c := validLocal; c.PolicyHost = "[::1]"; return c }(), wantErr: true},
		{name: "legacy linux enum", config: func() Config { c := validGateway; c.Mode = "linux"; return c }(), wantErr: true},
		{name: "missing projection key", config: func() Config { c := validLocal; c.ProjectionKey = ""; return c }(), wantErr: true},
		{name: "short projection key", config: func() Config { c := validLocal; c.ProjectionKey = "too-short"; return c }(), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateConfig(test.config)
			if (err != nil) != test.wantErr {
				t.Fatalf("ValidateConfig() error=%v, wantErr=%v", err, test.wantErr)
			}
		})
	}
}

func TestPolicyHostIndependentlyDerivesPolicyBaseURL(t *testing.T) {
	config := mustDefaultConfig(t)
	config.HTTPBind = "0.0.0.0:18080"
	config.SocksHost = "socks.surge.eb"
	config.PolicyHost = "policy.surge.eb"
	if got := config.PolicyBaseURL(); got != "http://policy.surge.eb:18080" {
		t.Fatalf("PolicyBaseURL()=%q, want http://policy.surge.eb:18080", got)
	}
}

func TestAllowsHTTPHost(t *testing.T) {
	local := mustDefaultConfig(t)
	local.SocksHost = "socks.surge.eb"
	local.PolicyHost = "policy.surge.eb"
	gateway := local
	gateway.Mode = ModeGateway
	gateway.HTTPBind = "0.0.0.0:18080"
	gateway.SocksBind = "0.0.0.0"
	gateway.ManagementToken = "management-token-1234"
	gateway.PolicyToken = "policy-token-12345678"

	tests := []struct {
		name, host string
		config     Config
		want       bool
	}{
		{name: "local Policy host", config: local, host: "policy.surge.eb:18080", want: true},
		{name: "local Policy host case and trailing dot", config: local, host: "POLICY.SURGE.EB.:18080", want: true},
		{name: "local SOCKS host is not HTTP host", config: local, host: "socks.surge.eb:18080", want: false},
		{name: "local loopback remains available", config: local, host: "127.0.0.1:18080", want: true},
		{name: "local unrelated hostname", config: local, host: "attacker.example", want: false},
		{name: "gateway Policy host", config: gateway, host: "policy.surge.eb:18080", want: true},
		{name: "gateway SOCKS hostname is not HTTP host", config: gateway, host: "socks.surge.eb:18080", want: false},
		{name: "gateway private IP remains available", config: gateway, host: "192.168.50.10:18080", want: true},
		{name: "gateway unrelated eb hostname", config: gateway, host: "other.eb:18080", want: false},
		{name: "gateway public hostname", config: gateway, host: "attacker.example", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := AllowsHTTPHost(test.config, test.host); got != test.want {
				t.Fatalf("AllowsHTTPHost(%q)=%v, want %v", test.host, got, test.want)
			}
		})
	}
}

func TestValidateProviderHeaderBoundary(t *testing.T) {
	provider := Provider{
		StableID: "provider-1", Name: "Provider", Type: "http",
		URL: "https://example.com/subscription", Enabled: true, SizeLimit: 16 << 20,
	}
	provider.Headers = map[string][]string{"User-Agent": {"SurgeEB-test"}}
	if err := validateProvider(provider); err != nil {
		t.Fatalf("safe Provider Header rejected: %v", err)
	}
	provider.Headers = map[string][]string{"Cookie": {"session=secret"}}
	if err := validateProvider(provider); err == nil {
		t.Fatal("Cookie Header was accepted despite redirect leakage risk")
	}
	provider.Enabled = false
	if err := validateProvider(provider); err != nil {
		t.Fatalf("disabled legacy Provider cannot be opened for repair: %v", err)
	}
	provider.Enabled = true
	provider.Headers = map[string][]string{"X-Subscription-Token": {"secret"}}
	if err := validateProvider(provider); err == nil {
		t.Fatal("non-allowlisted Header was accepted")
	}
	provider.Headers = nil
	provider.URL = "https://user:password@example.com/subscription"
	if err := validateProvider(provider); err == nil {
		t.Fatal("URL userinfo was accepted despite redirect leakage risk")
	}
}
