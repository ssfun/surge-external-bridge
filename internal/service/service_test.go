package service

import (
	"encoding/xml"
	"errors"
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
	content, err := renderFor("darwin", "/Applications/Surge & Tools/SurgeEB", "/Users/test/Data & State")
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err := xml.Unmarshal(content, &document); err != nil {
		t.Fatalf("invalid LaunchAgent XML: %v\n%s", err, content)
	}
	text := string(content)
	if !strings.HasPrefix(text, `<?xml version="1.0" encoding="UTF-8"?>`) || strings.HasPrefix(text, "&lt;?xml") {
		t.Fatalf("LaunchAgent XML declaration was escaped: %s", text)
	}
	if !strings.Contains(text, "Surge &amp; Tools") || !strings.Contains(text, "Data &amp; State") {
		t.Fatalf("LaunchAgent paths were not XML escaped: %s", text)
	}
	if !strings.Contains(text, "<key>Umask</key><integer>63</integer>") || !strings.Contains(text, "service.stderr.log") {
		t.Fatalf("LaunchAgent does not protect or retain service logs: %s", text)
	}
	if !strings.Contains(text, "<string>com.sfun.surgeeb</string>") {
		t.Fatalf("LaunchAgent does not use the public service label: %s", text)
	}
	if !strings.Contains(text, "<key>SuccessfulExit</key><false/>") || strings.Contains(text, "<key>KeepAlive</key><true/>") {
		t.Fatalf("LaunchAgent cannot remain stopped after a clean exit: %s", text)
	}
	executable, err := launchAgentExecutable(content)
	if err != nil || executable != "/Applications/Surge & Tools/SurgeEB" {
		t.Fatalf("LaunchAgent executable=%q err=%v", executable, err)
	}
}

func TestRenderSystemdDoesNotHTMLEscapePaths(t *testing.T) {
	content, err := renderFor("linux", `/opt/Surge & Tools/SurgeEB`, `/home/test/Data % State`)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if strings.Contains(text, "&#") || strings.Contains(text, "&amp;") {
		t.Fatalf("systemd unit contains HTML escaping: %s", text)
	}
	if !strings.Contains(text, `ExecStart="/opt/Surge & Tools/SurgeEB" serve --data-dir "/home/test/Data %% State"`) {
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
	if err != nil || scope != "systemd user" || linux != filepath.Join(home, ".config", "systemd", "user", systemdUnit) {
		t.Fatalf("unexpected Linux path: path=%q scope=%q err=%v", linux, scope, err)
	}
	if _, _, err := servicePathFor("windows", home); err == nil {
		t.Fatal("unsupported platform was accepted")
	}
}

func TestDarwinServiceUsesStableExecutable(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "Downloads", "SurgeEB")
	targetPath := installedExecutablePathFor(home)
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("test-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	target, err := installExecutableAt(source, targetPath)
	if err != nil {
		t.Fatal(err)
	}
	want := targetPath
	if target != want {
		t.Fatalf("installed executable path=%q, want %q", target, want)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "test-binary" {
		t.Fatalf("installed executable content=%q", content)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if permission := info.Mode().Perm(); permission != 0o755 {
		t.Fatalf("installed executable permission=%o, want 755", permission)
	}
	if err := os.Chmod(target, 0o555); err != nil {
		t.Fatal(err)
	}
	if sameTarget, err := installExecutableAt(target, target); err != nil || sameTarget != target {
		t.Fatalf("use existing canonical executable path=%q err=%v", sameTarget, err)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o555 {
		t.Fatalf("existing canonical executable permissions changed: info=%v err=%v", info, err)
	}
	if targetPath != filepath.Join(home, "Library", "Application Support", "SurgeEB", "bin", "SurgeEB") {
		t.Fatalf("darwin executable path=%q", targetPath)
	}
	linuxTarget, err := installExecutableFor("linux", source)
	if err != nil || linuxTarget != source {
		t.Fatalf("Linux executable path=%q err=%v, want source path", linuxTarget, err)
	}
}

func TestLaunchAgentRepairDetectionCoversLegacyPathAndMissingCopy(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "com.sfun.surgeeb.plist")
	executable := installedExecutablePathFor(home)
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	content, err := renderFor("darwin", executable, filepath.Join(home, "data"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if launchAgentNeedsRepair(path, executable) {
		t.Fatal("current user LaunchAgent was marked for repair")
	}
	legacy, err := renderFor("darwin", "/usr/local/bin/SurgeEB", filepath.Join(home, "data"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if !launchAgentNeedsRepair(path, executable) {
		t.Fatal("legacy /usr/local/bin LaunchAgent was not marked for repair")
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(executable); err != nil {
		t.Fatal(err)
	}
	if !launchAgentNeedsRepair(path, executable) {
		t.Fatal("missing service executable was not marked for repair")
	}
}

func TestLaunchAgentActiveDistinguishesLoadedFromRunning(t *testing.T) {
	if launchAgentActive([]byte("state = waiting\nlast exit code = 0\n")) {
		t.Fatal("loaded but stopped LaunchAgent was reported active")
	}
	if !launchAgentActive([]byte("state = running\npid = 123\n")) {
		t.Fatal("running LaunchAgent was reported inactive")
	}
}

func TestLaunchAgentTargetsLoggedInUserDomain(t *testing.T) {
	if got := launchdDomain(501); got != "gui/501" {
		t.Fatalf("launchd domain=%q, want gui/501", got)
	}
	if got := launchdTarget(501, label); got != "gui/501/com.sfun.surgeeb" {
		t.Fatalf("launchd target=%q", got)
	}
	if got := launchAgentPath("/Users/test", legacyLabel); got != "/Users/test/Library/LaunchAgents/fun.ssfun.surgeeb.plist" {
		t.Fatalf("legacy LaunchAgent path=%q", got)
	}
}

func TestDarwinUninstallStopsEveryServiceAndPropagatesFailure(t *testing.T) {
	wantErr := errors.New("process still running")
	var targets []string
	err := stopDarwinServices(501, func(target string) error {
		targets = append(targets, target)
		if strings.HasSuffix(target, "/"+legacyLabel) {
			return wantErr
		}
		return nil
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("stopDarwinServices error=%v, want %v", err, wantErr)
	}
	wantTargets := []string{"gui/501/" + label, "gui/501/" + legacyLabel}
	if strings.Join(targets, "|") != strings.Join(wantTargets, "|") {
		t.Fatalf("stop targets=%v, want %v", targets, wantTargets)
	}

	firstErr := errors.New("current service did not stop")
	targets = nil
	err = stopDarwinServices(501, func(target string) error {
		targets = append(targets, target)
		return firstErr
	})
	if !errors.Is(err, firstErr) || len(targets) != 1 {
		t.Fatalf("first stop failure error=%v targets=%v", err, targets)
	}
}

func TestLaunchAgentMissingOutputIsTheOnlyIgnoredBootoutFailure(t *testing.T) {
	for _, output := range []string{"Boot-out failed: 3: No such process", "Could not find service"} {
		if !launchAgentTargetMissing([]byte(output)) {
			t.Fatalf("missing target output was not recognized: %q", output)
		}
	}
	if launchAgentTargetMissing([]byte("Boot-out failed: 1: Operation not permitted")) {
		t.Fatal("permission failure was incorrectly treated as an absent service")
	}
}

func TestRootCannotInstallUserService(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		if err := validateUserServiceContext(goos, 0); err == nil || !strings.Contains(err.Error(), "without sudo") {
			t.Fatalf("%s root context error=%v", goos, err)
		}
		if err := validateUserServiceContext(goos, 501); err != nil {
			t.Fatalf("%s user context error=%v", goos, err)
		}
	}
}

func TestRenderRejectsUnitInjectionInPaths(t *testing.T) {
	if _, err := renderFor("linux", "/usr/bin/SurgeEB", "/tmp/data\nExecStart=/bin/evil"); err == nil {
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
