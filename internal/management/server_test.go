package management

import (
	"net"
	"testing"

	serviceManager "github.com/ssfun/surge-external-bridge/internal/service"
)

func TestPublicServiceInfoExposesRepairStateWithoutLocalPath(t *testing.T) {
	public := makePublicServiceInfo(serviceManager.Info{
		Platform: "darwin", Installed: true, Active: true, RepairNeeded: true,
		Path: "/Users/test/Library/LaunchAgents/com.sfun.surgeeb.plist", Scope: "LaunchAgent",
	})
	if !public.RepairNeeded || !public.Installed || !public.Active || public.Platform != "darwin" || public.Scope != "LaunchAgent" {
		t.Fatalf("unexpected public service info: %#v", public)
	}
}

func TestSuggestedGatewayHostPrefersActivePhysicalInterface(t *testing.T) {
	privateAddress := func(value string) net.Addr {
		return &net.IPNet{IP: net.ParseIP(value), Mask: net.CIDRMask(24, 32)}
	}
	interfaces := []gatewayInterface{
		{Name: "en0", Flags: 0, Addresses: []net.Addr{privateAddress("192.168.1.20")}},
		{Name: "docker0", Flags: net.FlagUp, Addresses: []net.Addr{privateAddress("172.17.0.1")}},
		{Name: "lan0", Flags: net.FlagUp, Addresses: []net.Addr{privateAddress("10.0.0.5")}},
		{Name: "en5", Flags: net.FlagUp, Addresses: []net.Addr{privateAddress("192.168.50.10")}},
		{Name: "utun4", Flags: net.FlagUp | net.FlagPointToPoint, Addresses: []net.Addr{privateAddress("10.8.0.2")}},
	}
	if got := suggestedGatewayHostFromInterfaces(interfaces, net.ParseIP("192.168.50.10")); got != "192.168.50.10" {
		t.Fatalf("suggested gateway host=%q, want active physical LAN address", got)
	}
}

func TestSuggestedGatewayHostUsesDefaultRouteAcrossPhysicalInterfaces(t *testing.T) {
	privateAddress := func(value string) net.Addr {
		return &net.IPNet{IP: net.ParseIP(value), Mask: net.CIDRMask(24, 32)}
	}
	interfaces := []gatewayInterface{
		{Name: "en0", Flags: net.FlagUp, Addresses: []net.Addr{privateAddress("192.168.1.20")}},
		{Name: "en5", Flags: net.FlagUp, Addresses: []net.Addr{privateAddress("10.20.30.40")}},
	}
	if got := suggestedGatewayHostFromInterfaces(interfaces, net.ParseIP("10.20.30.40")); got != "10.20.30.40" {
		t.Fatalf("suggested gateway host=%q, want default-route source", got)
	}
	if got := suggestedGatewayHostFromInterfaces(interfaces, nil); got != "" {
		t.Fatalf("ambiguous physical interfaces selected %q without a default route", got)
	}
}

func TestSuggestedGatewayHostReturnsEmptyWithoutReliableLANInterface(t *testing.T) {
	interfaces := []gatewayInterface{
		{Name: "bridge100", Flags: net.FlagUp, Addresses: []net.Addr{&net.IPNet{IP: net.ParseIP("192.168.64.1"), Mask: net.CIDRMask(24, 32)}}},
		{Name: "en0", Flags: net.FlagUp, Addresses: []net.Addr{&net.IPNet{IP: net.ParseIP("203.0.113.10"), Mask: net.CIDRMask(24, 32)}}},
	}
	if got := suggestedGatewayHostFromInterfaces(interfaces, nil); got != "" {
		t.Fatalf("suggested gateway host=%q, want empty fallback", got)
	}
}
