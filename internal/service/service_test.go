package service

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareDataDirCreatesPrivateAbsoluteDirectory(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)
	if err := os.Mkdir("service-data", 0o755); err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareDataDir("service-data")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(prepared) {
		t.Fatalf("service data directory is not absolute: %q", prepared)
	}
	info, err := os.Stat(prepared)
	if err != nil {
		t.Fatal(err)
	}
	if permission := info.Mode().Perm(); permission != 0o700 {
		t.Fatalf("service data directory permission = %o, want 700", permission)
	}
	if _, err := prepareDataDir("\n"); err == nil {
		t.Fatal("control-only service data directory was accepted")
	}
}

func TestRenderLaunchAgentEscapesPaths(t *testing.T) {
	content, err := renderFor("darwin", "/Applications/VLESS & Tools/vless2surge", "/Users/test/Data & State")
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err := xml.Unmarshal(content, &document); err != nil {
		t.Fatalf("invalid LaunchAgent XML: %v\n%s", err, content)
	}
	text := string(content)
	if !strings.Contains(text, "VLESS &amp; Tools") || !strings.Contains(text, "Data &amp; State") {
		t.Fatalf("LaunchAgent paths were not XML escaped: %s", text)
	}
	if !strings.Contains(text, "<key>Umask</key><integer>63</integer>") || !strings.Contains(text, "service.stderr.log") {
		t.Fatalf("LaunchAgent does not protect or retain service logs: %s", text)
	}
}

func TestRenderSystemdDoesNotHTMLEscapePaths(t *testing.T) {
	content, err := renderFor("linux", `/opt/VLESS & Tools/vless2surge`, `/home/test/Data % State`)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if strings.Contains(text, "&#") || strings.Contains(text, "&amp;") {
		t.Fatalf("systemd unit contains HTML escaping: %s", text)
	}
	if !strings.Contains(text, `ExecStart="/opt/VLESS & Tools/vless2surge" serve --data-dir "/home/test/Data %% State"`) {
		t.Fatalf("systemd ExecStart was not safely quoted: %s", text)
	}
	if !strings.Contains(text, "UMask=0077") {
		t.Fatalf("systemd service does not protect generated files: %s", text)
	}
}

func TestServicePaths(t *testing.T) {
	home := filepath.Join("", "home", "tester")
	darwin, scope, err := servicePathFor("darwin", home)
	if err != nil || scope != "LaunchAgent" || darwin != filepath.Join(home, "Library", "LaunchAgents", label+".plist") {
		t.Fatalf("unexpected Darwin path: path=%q scope=%q err=%v", darwin, scope, err)
	}
	linux, scope, err := servicePathFor("linux", home)
	if err != nil || scope != "systemd user" || linux != filepath.Join(home, ".config", "systemd", "user", "vless2surge.service") {
		t.Fatalf("unexpected Linux path: path=%q scope=%q err=%v", linux, scope, err)
	}
	if _, _, err := servicePathFor("windows", home); err == nil {
		t.Fatal("unsupported platform was accepted")
	}
}

func TestRenderRejectsUnitInjectionInPaths(t *testing.T) {
	if _, err := renderFor("linux", "/usr/bin/vless2surge", "/tmp/data\nExecStart=/bin/evil"); err == nil {
		t.Fatal("newline injection in systemd path was accepted")
	}
	if _, err := renderFor("darwin", "/tmp/app\x00evil", "/tmp/data"); err == nil {
		t.Fatal("NUL injection in LaunchAgent path was accepted")
	}
}

func TestConfigurationConsoleRegistrationDoesNotStartSecondProcess(t *testing.T) {
	deferred := strings.Join(systemdEnableArguments(false), " ")
	immediate := strings.Join(systemdEnableArguments(true), " ")
	if strings.Contains(deferred, "--now") {
		t.Fatalf("deferred service registration would start a competing process: %s", deferred)
	}
	if !strings.Contains(immediate, "--now") {
		t.Fatalf("CLI service installation no longer activates the service: %s", immediate)
	}
}
