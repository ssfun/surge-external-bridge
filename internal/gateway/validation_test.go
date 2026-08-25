package gateway

import "testing"

func TestDefaultConfigUsesProductUDPDiagnosticTarget(t *testing.T) {
	if got := DefaultConfig().NodeTestUDP; got != "8.8.8.8:53" {
		t.Fatalf("default UDP diagnostic target=%q, want 8.8.8.8:53", got)
	}
}

func TestValidateConfigNetworkBoundaries(t *testing.T) {
	validLocal := DefaultConfig()
	validLinux := DefaultConfig()
	validLinux.Mode = "linux"
	validLinux.HTTPBind = "0.0.0.0:18080"
	validLinux.SocksBind = "0.0.0.0"
	validLinux.SocksAdvertise = "192.168.50.10"
	validLinux.PolicyBaseURL = "http://192.168.50.10:18080"
	validLinux.ManagementToken = "management-token-1234"
	validLinux.PolicyToken = "policy-token-12345678"

	tests := []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{name: "local loopback", config: validLocal},
		{name: "linux private with distinct tokens", config: validLinux},
		{name: "linux Tailscale hostname", config: func() Config {
			c := validLinux
			c.SocksAdvertise = "gateway.tailnet.ts.net"
			c.PolicyBaseURL = "https://gateway.tailnet.ts.net"
			return c
		}()},
		{name: "local public HTTP bind", config: func() Config { c := validLocal; c.HTTPBind = "0.0.0.0:18080"; return c }(), wantErr: true},
		{name: "linux missing tokens", config: func() Config { c := validLinux; c.ManagementToken, c.PolicyToken = "", ""; return c }(), wantErr: true},
		{name: "linux shared token", config: func() Config { c := validLinux; c.PolicyToken = c.ManagementToken; return c }(), wantErr: true},
		{name: "linux public advertise", config: func() Config { c := validLinux; c.SocksAdvertise = "8.8.8.8"; return c }(), wantErr: true},
		{name: "linux public policy host", config: func() Config { c := validLinux; c.PolicyBaseURL = "https://example.com"; return c }(), wantErr: true},
		{name: "unspecified advertise", config: func() Config { c := validLinux; c.SocksAdvertise = "0.0.0.0"; return c }(), wantErr: true},
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

func TestValidateProviderHeaderBoundary(t *testing.T) {
	provider := Provider{
		StableID: "provider-1", Name: "Provider", Type: "http",
		URL: "https://example.com/subscription", Enabled: true, SizeLimit: 16 << 20,
	}
	provider.Headers = map[string][]string{"Cookie": {"session=secret"}}
	if err := validateProvider(provider); err != nil {
		t.Fatalf("HTTPS Cookie Header rejected: %v", err)
	}
	provider.URL = "http://example.com/subscription"
	if err := validateProvider(provider); err == nil {
		t.Fatal("sensitive Provider Header was accepted over HTTP")
	}
	provider.URL = "https://example.com/subscription"
	provider.Headers = map[string][]string{"X-Subscription-Token": {"secret"}}
	if err := validateProvider(provider); err == nil {
		t.Fatal("non-allowlisted Header was accepted")
	}
}
