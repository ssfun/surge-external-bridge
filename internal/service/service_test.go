package service

import (
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeExitError int

func (errorCode fakeExitError) Error() string { return "command exited" }
func (errorCode fakeExitError) ExitCode() int { return int(errorCode) }

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
	darwin, scope, err := servicePathFor("darwin", home, 501)
	if err != nil || scope != "LaunchAgent" || darwin != filepath.Join(home, "Library", "LaunchAgents", label+".plist") {
		t.Fatalf("unexpected Darwin path: path=%q scope=%q err=%v", darwin, scope, err)
	}
	linux, scope, err := servicePathFor("linux", home, 1000)
	if err != nil || scope != "systemd user" || linux != filepath.Join(home, ".config", "systemd", "user", systemdUnit) {
		t.Fatalf("unexpected Linux path: path=%q scope=%q err=%v", linux, scope, err)
	}
	linuxRoot, scope, err := servicePathFor("linux", home, 0)
	if err != nil || scope != "systemd system" || linuxRoot != systemdSystemPath {
		t.Fatalf("unexpected root Linux path: path=%q scope=%q err=%v", linuxRoot, scope, err)
	}
	if _, _, err := servicePathFor("windows", home, 1000); err == nil {
		t.Fatal("unsupported platform was accepted")
	}
}

func TestRootLinuxServicePathDoesNotRequireHome(t *testing.T) {
	resolvedHome := false
	path, scope, err := servicePathWithHome("linux", 0, func() (string, error) {
		resolvedHome = true
		return "", errors.New("HOME is not defined")
	})
	if err != nil || path != systemdSystemPath || scope != "systemd system" {
		t.Fatalf("root Linux path=%q scope=%q err=%v", path, scope, err)
	}
	if resolvedHome {
		t.Fatal("root Linux service path unnecessarily resolved HOME")
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
	linuxTarget, err := installExecutableFor("linux", 1000, source)
	if err != nil || linuxTarget != source {
		t.Fatalf("Linux executable path=%q err=%v, want source path", linuxTarget, err)
	}
	if target := serviceExecutableTarget("linux", 0); target != linuxSystemExecutablePath {
		t.Fatalf("root Linux executable target=%q, want %q", target, linuxSystemExecutablePath)
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

func TestServiceActiveDistinguishesInactiveFromDetectionErrors(t *testing.T) {
	tests := []struct {
		name     string
		goos     string
		euid     int
		uid      int
		output   string
		runErr   error
		want     bool
		wantErr  bool
		wantName string
		wantArgs string
	}{
		{name: "Linux active", goos: "linux", euid: 0, runErr: nil, want: true, wantName: "systemctl", wantArgs: "is-active --quiet surgeeb.service"},
		{name: "Linux user inactive", goos: "linux", euid: 1000, runErr: fakeExitError(3), wantName: "systemctl", wantArgs: "--user is-active --quiet surgeeb.service"},
		{name: "Linux unknown unit", goos: "linux", euid: 0, runErr: fakeExitError(4), wantName: "systemctl", wantArgs: "is-active --quiet surgeeb.service"},
		{name: "Linux DBus failure", goos: "linux", euid: 0, output: "Failed to connect to bus", runErr: fakeExitError(1), wantErr: true, wantName: "systemctl", wantArgs: "is-active --quiet surgeeb.service"},
		{name: "Darwin running", goos: "darwin", uid: 501, output: "state = running\npid = 123\n", want: true, wantName: "launchctl", wantArgs: "print gui/501/com.sfun.surgeeb"},
		{name: "Darwin waiting", goos: "darwin", uid: 501, output: "state = waiting\n", wantName: "launchctl", wantArgs: "print gui/501/com.sfun.surgeeb"},
		{name: "Darwin missing", goos: "darwin", uid: 501, output: "Could not find service", runErr: fakeExitError(113), wantName: "launchctl", wantArgs: "print gui/501/com.sfun.surgeeb"},
		{name: "Darwin permission failure", goos: "darwin", uid: 501, output: "Operation not permitted", runErr: fakeExitError(1), wantErr: true, wantName: "launchctl", wantArgs: "print gui/501/com.sfun.surgeeb"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			active, err := serviceActiveFor(test.goos, test.euid, test.uid, func(name string, arguments ...string) ([]byte, error) {
				called = true
				if name != test.wantName || strings.Join(arguments, " ") != test.wantArgs {
					t.Fatalf("command=%s %s, want %s %s", name, strings.Join(arguments, " "), test.wantName, test.wantArgs)
				}
				return []byte(test.output), test.runErr
			})
			if !called || active != test.want || (err != nil) != test.wantErr {
				t.Fatalf("called=%v active=%v err=%v, want active=%v wantErr=%v", called, active, err, test.want, test.wantErr)
			}
		})
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

func TestRootServiceContextPolicy(t *testing.T) {
	if err := validateServiceContext("darwin", 0); err == nil || !strings.Contains(err.Error(), "without sudo") {
		t.Fatalf("darwin root context error=%v", err)
	}
	if err := validateServiceContext("darwin", 501); err != nil {
		t.Fatalf("darwin user context error=%v", err)
	}
	for _, euid := range []int{0, 1000} {
		if err := validateServiceContext("linux", euid); err != nil {
			t.Fatalf("linux euid %d context error=%v", euid, err)
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
	deferred := systemdInstallCommands(1000, false)
	if len(deferred) != 1 || deferred[0].action != "enable" || strings.Join(deferred[0].arguments, " ") != "--user enable "+systemdUnit {
		t.Fatalf("deferred service registration would start a competing process: %#v", deferred)
	}
	immediate := systemdInstallCommands(1000, true)
	if len(immediate) != 2 || immediate[0].action != "enable" || immediate[1].action != "restart" {
		t.Fatalf("CLI service installation does not enable then restart: %#v", immediate)
	}
	if strings.Join(immediate[1].arguments, " ") != "--user restart "+systemdUnit {
		t.Fatalf("CLI service reinstall does not restart the user service: %#v", immediate[1])
	}
}

func TestRootUsesSystemSystemdManager(t *testing.T) {
	root := systemdInstallCommands(0, true)
	if len(root) != 2 || strings.Join(root[0].arguments, " ") != "enable "+systemdUnit || strings.Join(root[1].arguments, " ") != "restart "+systemdUnit {
		t.Fatalf("root systemd commands=%#v", root)
	}
	user := systemdInstallCommands(1000, true)
	if len(user) != 2 || !strings.HasPrefix(strings.Join(user[0].arguments, " "), "--user ") || !strings.HasPrefix(strings.Join(user[1].arguments, " "), "--user ") {
		t.Fatalf("user systemd commands=%#v", user)
	}

	content, err := renderForContext("linux", "/usr/local/bin/SurgeEB", "/root/.surge-external-bridge", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "WantedBy=multi-user.target") {
		t.Fatalf("root system service has wrong install target: %s", content)
	}
}
