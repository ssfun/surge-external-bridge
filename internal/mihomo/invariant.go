package mihomo

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"

	MConfig "github.com/metacubex/mihomo/config"
)

func ValidateControlledConfig(cfg *MConfig.Config, homeDir string) error {
	if cfg == nil || cfg.General == nil || cfg.Controller == nil || cfg.DNS == nil || cfg.IPTables == nil || cfg.NTP == nil {
		return errors.New("incomplete Mihomo configuration")
	}
	general := cfg.General
	if general.Port != 0 || general.SocksPort != 0 || general.MixedPort != 0 || general.RedirPort != 0 || general.TProxyPort != 0 {
		return errors.New("Mihomo top-level proxy ports must all be disabled")
	}
	if general.Tun.Enable || general.Tun.AutoRoute || general.Tun.AutoRedirect {
		return errors.New("Mihomo TUN and automatic routing must be disabled")
	}
	if general.TuicServer.Enable || general.ShadowSocksConfig != "" || general.VmessConfig != "" {
		return errors.New("Mihomo protocol listeners must be disabled")
	}
	if len(cfg.Listeners) != 0 || len(cfg.Tunnels) != 0 {
		return errors.New("Mihomo named listeners and tunnels are forbidden")
	}
	if cfg.IPTables.Enable || cfg.IPTables.DnsRedirect {
		return errors.New("Mihomo iptables integration must be disabled")
	}
	if cfg.NTP.WriteToSystem {
		return errors.New("Mihomo must not write time to the system")
	}
	if cfg.DNS.Enable || strings.TrimSpace(cfg.DNS.Listen) != "" {
		return errors.New("Mihomo DNS listener must be disabled")
	}
	if err := validateController(cfg.Controller, homeDir); err != nil {
		return err
	}
	return nil
}

func validateController(controller *MConfig.Controller, homeDir string) error {
	unix := strings.TrimSpace(controller.ExternalControllerUnix)
	tcp := strings.TrimSpace(controller.ExternalController)
	tls := strings.TrimSpace(controller.ExternalControllerTLS)
	pipe := strings.TrimSpace(controller.ExternalControllerPipe)
	count := 0
	for _, value := range []string{unix, tcp, tls, pipe} {
		if value != "" {
			count++
		}
	}
	if count != 1 {
		return errors.New("exactly one private Mihomo Controller endpoint is required")
	}
	if pipe != "" {
		return errors.New("named-pipe Mihomo Controller is not supported")
	}
	if unix != "" {
		if homeDir == "" {
			return errors.New("Mihomo HomeDir is required for a Unix Controller")
		}
		if !pathWithin(homeDir, unix) {
			return errors.New("Mihomo Unix Controller must stay inside its private HomeDir")
		}
		if len([]byte(unix)) > 100 {
			return errors.New("Mihomo Unix Controller path is too long for portable Unix socket limits")
		}
		return nil
	}
	endpoint := tcp
	if endpoint == "" {
		endpoint = tls
	}
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		return fmt.Errorf("invalid Mihomo Controller endpoint: %w", err)
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return errors.New("Mihomo TCP Controller must use a loopback IP literal")
	}
	if len(controller.Secret) < 32 {
		return errors.New("Mihomo TCP Controller requires a secret of at least 32 characters")
	}
	return nil
}

func pathWithin(root, candidate string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(rootAbs, candidateAbs)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
