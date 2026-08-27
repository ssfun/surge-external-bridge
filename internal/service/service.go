package service

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	texttemplate "text/template"
	"time"
)

const (
	label                     = "com.sfun.surgeeb"
	legacyLabel               = "fun.ssfun.surgeeb"
	systemdUnit               = "surgeeb.service"
	systemdSystemPath         = "/etc/systemd/system/surgeeb.service"
	linuxSystemExecutablePath = "/usr/local/bin/SurgeEB"
	darwinExecutableName      = "SurgeEB"
)

type Info struct {
	Platform     string `json:"platform"`
	Installed    bool   `json:"installed"`
	Active       bool   `json:"active"`
	RepairNeeded bool   `json:"repair_needed,omitempty"`
	Path         string `json:"path,omitempty"`
	Scope        string `json:"scope"`
}

func Status() (Info, error) {
	path, scope, err := servicePath()
	if err != nil {
		return Info{}, err
	}
	_, statErr := os.Stat(path)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return Info{}, statErr
	}
	installed := statErr == nil
	repairNeeded := installed && runtime.GOOS == "darwin" && launchAgentNeedsRepair(path, installedExecutablePath())
	return Info{Platform: runtime.GOOS, Installed: installed, Active: installed && serviceActive(), RepairNeeded: repairNeeded, Path: path, Scope: scope}, nil
}

func serviceActive() bool {
	switch runtime.GOOS {
	case "darwin":
		output, err := exec.Command("launchctl", "print", launchdTarget(os.Getuid(), label)).CombinedOutput()
		return err == nil && launchAgentActive(output)
	case "linux":
		return exec.Command("systemctl", systemctlArguments(os.Geteuid(), "is-active", "--quiet", systemdUnit)...).Run() == nil
	default:
		return false
	}
}

func Install(dataDir string) (Info, error) {
	return install(dataDir, true)
}

// Register installs the service definition without starting a second SurgeEB
// process. It is used by the running configuration console, which already owns
// the configured HTTP and SOCKS ports. The service will start when its service
// manager next activates the configured target; CLI installation can still
// activate it immediately via Install.
func Register(dataDir string) (Info, error) {
	return install(dataDir, false)
}

func install(dataDir string, activate bool) (Info, error) {
	if err := validateServiceContext(runtime.GOOS, os.Geteuid()); err != nil {
		return Info{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return Info{}, err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return Info{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Info{}, err
	}
	executable, err = installExecutableFor(runtime.GOOS, os.Geteuid(), executable)
	if err != nil {
		return Info{}, err
	}
	dataDir, err = prepareDataDir(dataDir)
	if err != nil {
		return Info{}, err
	}
	path, _, err := servicePath()
	if err != nil {
		return Info{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Info{}, err
	}
	content, err := render(executable, dataDir)
	if err != nil {
		return Info{}, err
	}
	if err := writeFileAtomic(path, content, 0o600); err != nil {
		return Info{}, err
	}
	if runtime.GOOS == "darwin" {
		if err := configureLaunchAgent(home, path, activate); err != nil {
			return Info{}, err
		}
	} else if runtime.GOOS == "linux" {
		if output, err := exec.Command("systemctl", systemctlArguments(os.Geteuid(), "daemon-reload")...).CombinedOutput(); err != nil {
			return Info{}, fmt.Errorf("systemctl daemon-reload: %w: %s", err, bytes.TrimSpace(output))
		}
		arguments := systemdEnableArguments(os.Geteuid(), activate)
		if output, err := exec.Command("systemctl", arguments...).CombinedOutput(); err != nil {
			return Info{}, fmt.Errorf("systemctl enable: %w: %s", err, bytes.TrimSpace(output))
		}
	}
	return Status()
}

func validateServiceContext(goos string, euid int) error {
	if goos == "darwin" && euid == 0 {
		return errors.New("user service installation must be run as the logged-in user without sudo")
	}
	return nil
}

func installExecutableFor(goos string, euid int, source string) (string, error) {
	target := serviceExecutableTarget(goos, euid)
	if goos == "darwin" && target == "" {
		return "", errors.New("resolve the current user's service executable path")
	}
	if target == "" {
		return source, nil
	}
	installed, err := installExecutableAt(source, target)
	if err != nil {
		return "", fmt.Errorf("prepare service executable %s: %w", target, err)
	}
	return installed, nil
}

func serviceExecutableTarget(goos string, euid int) string {
	switch {
	case goos == "darwin":
		return installedExecutablePath()
	case goos == "linux" && euid == 0:
		return linuxSystemExecutablePath
	default:
		return ""
	}
}

func installExecutableAt(source, target string) (string, error) {
	if filepath.Clean(source) == filepath.Clean(target) {
		info, err := os.Stat(target)
		if err != nil {
			return "", fmt.Errorf("inspect installed executable: %w", err)
		}
		if !info.Mode().IsRegular() {
			return "", errors.New("installed service executable must be a regular file")
		}
		return target, nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", fmt.Errorf("create service executable directory: %w", err)
	}
	input, err := os.Open(source)
	if err != nil {
		return "", fmt.Errorf("open service executable: %w", err)
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect service executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("service executable must be a regular file")
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".SurgeEB-install-*")
	if err != nil {
		return "", fmt.Errorf("create temporary service executable: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o755); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("secure temporary service executable: %w", err)
	}
	if _, err := io.Copy(temporary, input); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("copy service executable: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("sync service executable: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close service executable: %w", err)
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return "", fmt.Errorf("install service executable: %w", err)
	}
	return target, nil
}

func writeFileAtomic(path string, content []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".surgeeb-service-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func configureLaunchAgent(home, path string, activate bool) error {
	uid := os.Getuid()
	domain := launchdDomain(uid)
	target := launchdTarget(uid, label)
	if activate {
		if err := bootoutAndWait(launchdTarget(uid, legacyLabel)); err != nil {
			return err
		}
		if err := bootoutAndWait(target); err != nil {
			return err
		}
	}
	if err := os.Remove(launchAgentPath(home, legacyLabel)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove legacy LaunchAgent: %w", err)
	}
	if output, err := exec.Command("launchctl", "enable", target).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl enable: %w: %s", err, bytes.TrimSpace(output))
	}
	if !activate {
		return nil
	}
	if output, err := exec.Command("launchctl", "bootstrap", domain, path).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w: %s", err, bytes.TrimSpace(output))
	}
	if output, err := exec.Command("launchctl", "kickstart", "-k", target).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl kickstart: %w: %s", err, bytes.TrimSpace(output))
	}
	return nil
}

func bootoutAndWait(target string) error {
	output, err := exec.Command("launchctl", "bootout", target).CombinedOutput()
	if err != nil {
		if launchAgentTargetMissing(output) {
			return nil
		}
		return fmt.Errorf("launchctl bootout %s: %w: %s", target, err, bytes.TrimSpace(output))
	}
	deadline := time.Now().Add(3 * time.Second)
	for exec.Command("launchctl", "print", target).Run() == nil {
		if time.Now().After(deadline) {
			return fmt.Errorf("launchctl bootout did not unload %s", target)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil
}

func launchAgentTargetMissing(output []byte) bool {
	message := strings.ToLower(string(output))
	return strings.Contains(message, "no such process") || strings.Contains(message, "could not find service") || strings.Contains(message, "service not found")
}

func launchAgentActive(output []byte) bool {
	for _, line := range strings.Split(string(output), "\n") {
		field := strings.TrimSpace(line)
		if field == "state = running" || strings.HasPrefix(field, "pid = ") {
			return true
		}
	}
	return false
}

func Start() (Info, error)   { return control("start") }
func Stop() (Info, error)    { return control("stop") }
func Restart() (Info, error) { return control("restart") }

func control(action string) (Info, error) {
	if err := validateServiceContext(runtime.GOOS, os.Geteuid()); err != nil {
		return Info{}, err
	}
	info, err := Status()
	if err != nil {
		return Info{}, err
	}
	if !info.Installed {
		return Info{}, errors.New("service is not installed")
	}
	switch runtime.GOOS {
	case "darwin":
		if err := controlLaunchAgent(action, info.Path); err != nil {
			return Info{}, err
		}
	case "linux":
		if action != "start" && action != "stop" && action != "restart" {
			return Info{}, fmt.Errorf("unsupported service action %q", action)
		}
		if output, err := exec.Command("systemctl", systemctlArguments(os.Geteuid(), action, systemdUnit)...).CombinedOutput(); err != nil {
			return Info{}, fmt.Errorf("systemctl %s: %w: %s", action, err, bytes.TrimSpace(output))
		}
	default:
		return Info{}, fmt.Errorf("service management is unsupported on %s", runtime.GOOS)
	}
	return Status()
}

func controlLaunchAgent(action, path string) error {
	target := launchdTarget(os.Getuid(), label)
	domain := launchdDomain(os.Getuid())
	switch action {
	case "start":
		if exec.Command("launchctl", "print", target).Run() != nil {
			if output, err := exec.Command("launchctl", "bootstrap", domain, path).CombinedOutput(); err != nil {
				return fmt.Errorf("launchctl bootstrap: %w: %s", err, bytes.TrimSpace(output))
			}
		}
		if output, err := exec.Command("launchctl", "kickstart", "-k", target).CombinedOutput(); err != nil {
			return fmt.Errorf("launchctl kickstart: %w: %s", err, bytes.TrimSpace(output))
		}
		return waitForLaunchAgent(target, true)
	case "stop":
		return bootoutAndWait(target)
	case "restart":
		if err := bootoutAndWait(target); err != nil {
			return err
		}
		if output, err := exec.Command("launchctl", "bootstrap", domain, path).CombinedOutput(); err != nil {
			return fmt.Errorf("launchctl bootstrap: %w: %s", err, bytes.TrimSpace(output))
		}
		if output, err := exec.Command("launchctl", "kickstart", "-k", target).CombinedOutput(); err != nil {
			return fmt.Errorf("launchctl kickstart: %w: %s", err, bytes.TrimSpace(output))
		}
		return waitForLaunchAgent(target, true)
	default:
		return fmt.Errorf("unsupported service action %q", action)
	}
}

func waitForLaunchAgent(target string, expected bool) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		output, err := exec.Command("launchctl", "print", target).CombinedOutput()
		active := err == nil && launchAgentActive(output)
		if active == expected {
			return nil
		}
		if time.Now().After(deadline) {
			state := "inactive"
			if expected {
				state = "active"
			}
			return fmt.Errorf("LaunchAgent did not become %s", state)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func launchdDomain(uid int) string {
	return "gui/" + strconv.Itoa(uid)
}

func launchdTarget(uid int, serviceLabel string) string {
	return launchdDomain(uid) + "/" + serviceLabel
}

func systemctlArguments(euid int, arguments ...string) []string {
	if euid == 0 {
		return arguments
	}
	return append([]string{"--user"}, arguments...)
}

func systemdEnableArguments(euid int, activate bool) []string {
	arguments := systemctlArguments(euid, "enable")
	if activate {
		arguments = append(arguments, "--now")
	}
	return append(arguments, systemdUnit)
}

func prepareDataDir(dataDir string) (string, error) {
	if strings.TrimSpace(dataDir) == "" || strings.ContainsAny(dataDir, "\x00\r\n") {
		return "", errors.New("service data directory must be a non-empty path without control characters")
	}
	absolute, err := filepath.Abs(dataDir)
	if err != nil {
		return "", fmt.Errorf("resolve service data directory: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return "", fmt.Errorf("create service data directory: %w", err)
	}
	if err := os.Chmod(absolute, 0o700); err != nil {
		return "", fmt.Errorf("secure service data directory: %w", err)
	}
	return absolute, nil
}

func Uninstall() (Info, error) {
	if err := validateServiceContext(runtime.GOOS, os.Geteuid()); err != nil {
		return Info{}, err
	}
	path, _, err := servicePath()
	if err != nil {
		return Info{}, err
	}
	if runtime.GOOS == "darwin" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return Info{}, homeErr
		}
		if err := stopDarwinServices(os.Getuid(), bootoutAndWait); err != nil {
			return Info{}, err
		}
		if err := os.Remove(launchAgentPath(home, legacyLabel)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Info{}, err
		}
	} else if runtime.GOOS == "linux" {
		if output, err := exec.Command("systemctl", systemctlArguments(os.Geteuid(), "disable", "--now", systemdUnit)...).CombinedOutput(); err != nil {
			return Info{}, fmt.Errorf("systemctl disable: %w: %s", err, bytes.TrimSpace(output))
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Info{}, err
	}
	if runtime.GOOS == "linux" {
		if output, err := exec.Command("systemctl", systemctlArguments(os.Geteuid(), "daemon-reload")...).CombinedOutput(); err != nil {
			return Info{}, fmt.Errorf("systemctl daemon-reload: %w: %s", err, bytes.TrimSpace(output))
		}
	}
	return Status()
}

func stopDarwinServices(uid int, stop func(string) error) error {
	for _, serviceLabel := range []string{label, legacyLabel} {
		if err := stop(launchdTarget(uid, serviceLabel)); err != nil {
			return err
		}
	}
	return nil
}

func servicePath() (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	return servicePathFor(runtime.GOOS, home, os.Geteuid())
}

func servicePathFor(goos, home string, euid int) (string, string, error) {
	switch goos {
	case "darwin":
		return launchAgentPath(home, label), "LaunchAgent", nil
	case "linux":
		if euid == 0 {
			return systemdSystemPath, "systemd system", nil
		}
		return filepath.Join(home, ".config", "systemd", "user", systemdUnit), "systemd user", nil
	default:
		return "", "", fmt.Errorf("service management is unsupported on %s", goos)
	}
}

func launchAgentPath(home, serviceLabel string) string {
	return filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist")
}

func installedExecutablePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return installedExecutablePathFor(home)
}

func installedExecutablePathFor(home string) string {
	return filepath.Join(home, "Library", "Application Support", "SurgeEB", "bin", darwinExecutableName)
}

func launchAgentNeedsRepair(path, expectedExecutable string) bool {
	if expectedExecutable == "" {
		return true
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	configuredExecutable, err := launchAgentExecutable(content)
	if err != nil || filepath.Clean(configuredExecutable) != filepath.Clean(expectedExecutable) {
		return true
	}
	info, err := os.Stat(expectedExecutable)
	return err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0
}

func launchAgentExecutable(content []byte) (string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(content))
	programArguments := false
	lastKey := ""
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("decode LaunchAgent: %w", err)
		}
		switch value := token.(type) {
		case xml.StartElement:
			switch value.Name.Local {
			case "key":
				if err := decoder.DecodeElement(&lastKey, &value); err != nil {
					return "", fmt.Errorf("decode LaunchAgent key: %w", err)
				}
			case "array":
				programArguments = lastKey == "ProgramArguments"
			case "string":
				if !programArguments {
					continue
				}
				var executable string
				if err := decoder.DecodeElement(&executable, &value); err != nil {
					return "", fmt.Errorf("decode LaunchAgent executable: %w", err)
				}
				if executable == "" {
					return "", errors.New("LaunchAgent executable is empty")
				}
				return executable, nil
			}
		case xml.EndElement:
			if value.Name.Local == "array" && programArguments {
				return "", errors.New("LaunchAgent ProgramArguments has no executable")
			}
		}
	}
	return "", errors.New("LaunchAgent ProgramArguments is missing")
}

func render(executable, dataDir string) ([]byte, error) {
	return renderForContext(runtime.GOOS, executable, dataDir, os.Geteuid())
}

func renderFor(goos, executable, dataDir string) ([]byte, error) {
	return renderForContext(goos, executable, dataDir, 1000)
}

func renderForContext(goos, executable, dataDir string, euid int) ([]byte, error) {
	if executable == "" || dataDir == "" || strings.ContainsAny(executable+dataDir, "\x00\r\n") {
		return nil, errors.New("service executable and data directory must be non-empty paths without control characters")
	}
	data := struct{ Executable, DataDir, Label, InstallTarget string }{executable, dataDir, label, "default.target"}
	if goos == "linux" && euid == 0 {
		data.InstallTarget = "multi-user.target"
	}
	if goos == "darwin" {
		const source = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>{{xml .Label}}</string>
  <key>ProgramArguments</key><array><string>{{xml .Executable}}</string><string>serve</string><string>--data-dir</string><string>{{xml .DataDir}}</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>ProcessType</key><string>Interactive</string>
  <key>Umask</key><integer>63</integer>
  <key>StandardOutPath</key><string>{{xml .DataDir}}/service.stdout.log</string>
  <key>StandardErrorPath</key><string>{{xml .DataDir}}/service.stderr.log</string>
</dict></plist>
`
		tmpl, err := texttemplate.New("launchd").Funcs(texttemplate.FuncMap{"xml": xmlText}).Parse(source)
		if err != nil {
			return nil, err
		}
		var output bytes.Buffer
		if err := tmpl.Execute(&output, data); err != nil {
			return nil, err
		}
		return output.Bytes(), nil
	}
	if goos != "linux" {
		return nil, fmt.Errorf("service rendering is unsupported on %s", goos)
	}
	const source = `[Unit]
Description=Surge External Bridge
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart={{systemdArg .Executable}} serve --data-dir {{systemdArg .DataDir}}
Restart=on-failure
RestartSec=3
UMask=0077
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy={{.InstallTarget}}
`
	tmpl, err := texttemplate.New("systemd").Funcs(texttemplate.FuncMap{"systemdArg": systemdArg}).Parse(source)
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	if err := tmpl.Execute(&output, data); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func systemdArg(value string) string {
	value = strings.ReplaceAll(value, "%", "%%")
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}

func xmlText(value string) string {
	var output bytes.Buffer
	_ = xml.EscapeText(&output, []byte(value))
	return output.String()
}

func CurrentUser() string {
	current, err := user.Current()
	if err != nil {
		return ""
	}
	return current.Username
}
