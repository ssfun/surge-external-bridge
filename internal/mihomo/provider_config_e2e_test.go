package mihomo

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Exercise the real manager startup, file provider and publication lifecycle.
// Failure: a relative home joined to a relative file is resolved a second time
// by the native provider on reload, so a valid startup cannot be reapplied.
func TestRelativeHomeFileReloadEndToEnd(t *testing.T) {
	if os.Getenv("SURGEEB_RELATIVE_HOME_HELPER") == "" {
		command := exec.Command(os.Args[0], "-test.run=^TestRelativeHomeFileReloadEndToEnd$", "-test.v")
		command.Env = append(os.Environ(), "SURGEEB_RELATIVE_HOME_HELPER=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("relative home helper: %v\n%s", err, output)
		}
		return
	}
	home, err := os.MkdirTemp("/tmp", "surgeeb-relative-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	if err := os.Chdir(filepath.Dir(home)); err != nil {
		t.Fatal(err)
	}
	relativeHome := filepath.Base(home)
	definition := ProviderDefinition{StableID: "relative", Name: "Relative", Type: "file", FilePath: "nodes.yaml"}
	write := func(name string) {
		t.Helper()
		data := "proxies:\n  - name: " + name + "\n    type: socks5\n    server: 127.0.0.1\n    port: 65501\n"
		if err := os.WriteFile(filepath.Join(home, definition.FilePath), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("before")
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(ManagerOptions{HomeDir: relativeHome, ControllerSocket: filepath.Join(relativeHome, "controller.sock"), ControllerSecret: "controller-secret", SocksBind: "127.0.0.1", SocksAdvertise: "127.0.0.1", SocksPort: port, MasterKey: []byte("01234567890123456789012345678901"), Providers: []ProviderDefinition{definition}})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()
	write("after")
	if err := manager.ApplyProviders([]ProviderDefinition{definition}); err != nil {
		t.Fatal(err)
	}
	entries := manager.store.Load().Entries()
	if len(entries) != 1 || entries[0].ProxyName != "after" {
		t.Fatalf("reload projection: %#v", entries)
	}
	artifact := os.Getenv("SURGEEB_RELATIVE_HOME_ARTIFACT")
	if artifact != "" {
		data, err := json.Marshal(map[string]any{"test": t.Name(), "passed": true, "relative_home": relativeHome, "relative_file": definition.FilePath, "published_node": entries[0].ProxyName})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(artifact, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
