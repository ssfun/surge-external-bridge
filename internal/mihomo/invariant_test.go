package mihomo

import (
	"os"
	"path/filepath"
	"testing"

	MConfig "github.com/metacubex/mihomo/config"
)

func safeConfig(home string) *MConfig.Config {
	return &MConfig.Config{
		General:    &MConfig.General{},
		Controller: &MConfig.Controller{ExternalControllerUnix: filepath.Join(home, "controller.sock")},
		DNS:        &MConfig.DNS{},
		IPTables:   &MConfig.IPTables{},
		NTP:        &MConfig.NTP{},
	}
}

func TestValidateControlledConfigAcceptsPrivateNoListenerConfig(t *testing.T) {
	home := shortTempDir(t)
	if err := ValidateControlledConfig(safeConfig(home), home); err != nil {
		t.Fatal(err)
	}
}

func TestValidateControlledConfigRejectsEveryDangerousEntrance(t *testing.T) {
	home := shortTempDir(t)
	tests := map[string]func(*MConfig.Config){
		"http":          func(c *MConfig.Config) { c.General.Port = 7890 },
		"socks":         func(c *MConfig.Config) { c.General.SocksPort = 7891 },
		"mixed":         func(c *MConfig.Config) { c.General.MixedPort = 7892 },
		"redir":         func(c *MConfig.Config) { c.General.RedirPort = 7893 },
		"tproxy":        func(c *MConfig.Config) { c.General.TProxyPort = 7894 },
		"tun":           func(c *MConfig.Config) { c.General.Tun.Enable = true },
		"auto-route":    func(c *MConfig.Config) { c.General.Tun.AutoRoute = true },
		"auto-redirect": func(c *MConfig.Config) { c.General.Tun.AutoRedirect = true },
		"iptables":      func(c *MConfig.Config) { c.IPTables.Enable = true },
		"dns-redirect":  func(c *MConfig.Config) { c.IPTables.DnsRedirect = true },
		"dns-enable":    func(c *MConfig.Config) { c.DNS.Enable = true },
		"dns-listen":    func(c *MConfig.Config) { c.DNS.Listen = "127.0.0.1:5353" },
		"system-time":   func(c *MConfig.Config) { c.NTP.WriteToSystem = true },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := safeConfig(home)
			mutate(cfg)
			if err := ValidateControlledConfig(cfg, home); err == nil {
				t.Fatal("dangerous configuration was accepted")
			}
		})
	}
}

func TestValidateControlledConfigRejectsPublicOrEscapedController(t *testing.T) {
	home := shortTempDir(t)
	cfg := safeConfig(home)
	cfg.Controller.ExternalControllerUnix = filepath.Join(home, "..", "controller.sock")
	if err := ValidateControlledConfig(cfg, home); err == nil {
		t.Fatal("escaped Unix Controller path was accepted")
	}
	cfg = safeConfig(home)
	cfg.Controller.ExternalControllerUnix = ""
	cfg.Controller.ExternalController = "0.0.0.0:9090"
	cfg.Controller.Secret = "01234567890123456789012345678901"
	if err := ValidateControlledConfig(cfg, home); err == nil {
		t.Fatal("public TCP Controller was accepted")
	}
}

func TestLoadOrCreatePrivateKeyIsStableAndPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	path := filepath.Join(dir, "controller.key")
	first, err := LoadOrCreatePrivateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreatePrivateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) || len(first) != MasterKeySize {
		t.Fatal("master key was not stable")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("master key mode is %o, want 600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("private directory mode is %o, want 700", dirInfo.Mode().Perm())
	}
}

func TestLoadOrCreatePrivateKeyRejectsSymbolicLink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.key")
	if err := os.WriteFile(target, make([]byte, MasterKeySize), 0o600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(dir, "controller.key")
	if err := os.Symlink(target, linked); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreatePrivateKey(linked); err == nil {
		t.Fatal("symbolic-link master key was accepted")
	}
}

func TestSecurePrivateTreeRepairsMihomoCachePermissions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "mihomo")
	subdir := filepath.Join(root, "proxy_provider")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(subdir, "provider.yaml")
	if err := os.WriteFile(cache, []byte("proxies: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if PrivateTreeProtected(root) {
		t.Fatal("broad Mihomo cache permissions were reported as protected")
	}
	if err := SecurePrivateTree(root); err != nil {
		t.Fatal(err)
	}
	if !PrivateTreeProtected(root) {
		t.Fatal("Mihomo cache permissions were not repaired")
	}
}
