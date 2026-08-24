package service

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	texttemplate "text/template"
)

const label = "fun.ssfun.vless2surge"

type Info struct {
	Platform  string `json:"platform"`
	Installed bool   `json:"installed"`
	Active    bool   `json:"active"`
	Path      string `json:"path,omitempty"`
	Scope     string `json:"scope"`
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
	return Info{Platform: runtime.GOOS, Installed: installed, Active: installed && serviceActive(), Path: path, Scope: scope}, nil
}

func serviceActive() bool {
	switch runtime.GOOS {
	case "darwin":
		target := "gui/" + strconv.Itoa(os.Getuid()) + "/" + label
		return exec.Command("launchctl", "print", target).Run() == nil
	case "linux":
		return exec.Command("systemctl", "--user", "is-active", "--quiet", "vless2surge.service").Run() == nil
	default:
		return false
	}
}

func Install(dataDir string) (Info, error) {
	return install(dataDir, true)
}

// Register installs the user service without starting a second vless2surge
// process. It is used by the running configuration console, which already owns
// the configured HTTP and SOCKS ports. The service will start at the next user
// login; CLI installation can still activate it immediately via Install.
func Register(dataDir string) (Info, error) {
	return install(dataDir, false)
}

func install(dataDir string, activate bool) (Info, error) {
	executable, err := os.Executable()
	if err != nil {
		return Info{}, err
	}
	executable, err = filepath.Abs(executable)
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
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return Info{}, err
	}
	if runtime.GOOS == "darwin" && activate {
		uid := os.Getuid()
		_ = exec.Command("launchctl", "bootout", "gui/"+strconv.Itoa(uid), path).Run()
		if output, err := exec.Command("launchctl", "bootstrap", "gui/"+strconv.Itoa(uid), path).CombinedOutput(); err != nil {
			return Info{}, fmt.Errorf("launchctl bootstrap: %w: %s", err, bytes.TrimSpace(output))
		}
	} else if runtime.GOOS == "linux" {
		if output, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
			return Info{}, fmt.Errorf("systemctl daemon-reload: %w: %s", err, bytes.TrimSpace(output))
		}
		arguments := systemdEnableArguments(activate)
		if output, err := exec.Command("systemctl", arguments...).CombinedOutput(); err != nil {
			return Info{}, fmt.Errorf("systemctl enable: %w: %s", err, bytes.TrimSpace(output))
		}
	}
	return Status()
}

func systemdEnableArguments(activate bool) []string {
	arguments := []string{"--user", "enable"}
	if activate {
		arguments = append(arguments, "--now")
	}
	return append(arguments, "vless2surge.service")
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
	path, _, err := servicePath()
	if err != nil {
		return Info{}, err
	}
	if runtime.GOOS == "darwin" {
		_ = exec.Command("launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid()), path).Run()
	} else if runtime.GOOS == "linux" {
		_ = exec.Command("systemctl", "--user", "disable", "--now", "vless2surge.service").Run()
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Info{}, err
	}
	if runtime.GOOS == "linux" {
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	}
	return Status()
}

func servicePath() (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	return servicePathFor(runtime.GOOS, home)
}

func servicePathFor(goos, home string) (string, string, error) {
	switch goos {
	case "darwin":
		return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), "LaunchAgent", nil
	case "linux":
		return filepath.Join(home, ".config", "systemd", "user", "vless2surge.service"), "systemd user", nil
	default:
		return "", "", fmt.Errorf("service management is unsupported on %s", goos)
	}
}

func render(executable, dataDir string) ([]byte, error) {
	return renderFor(runtime.GOOS, executable, dataDir)
}

func renderFor(goos, executable, dataDir string) ([]byte, error) {
	if executable == "" || dataDir == "" || strings.ContainsAny(executable+dataDir, "\x00\r\n") {
		return nil, errors.New("service executable and data directory must be non-empty paths without control characters")
	}
	data := struct{ Executable, DataDir, Label string }{executable, dataDir, label}
	if goos == "darwin" {
		const source = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>{{.Label}}</string>
  <key>ProgramArguments</key><array><string>{{.Executable}}</string><string>serve</string><string>--data-dir</string><string>{{.DataDir}}</string></array>
  <key>RunAtLoad</key><true/><key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Interactive</string>
  <key>Umask</key><integer>63</integer>
  <key>StandardOutPath</key><string>{{.DataDir}}/service.stdout.log</string>
  <key>StandardErrorPath</key><string>{{.DataDir}}/service.stderr.log</string>
</dict></plist>
`
		tmpl, err := template.New("launchd").Parse(source)
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
Description=vless2surge Embedded VLESS gateway
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
WantedBy=default.target
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

func CurrentUser() string {
	current, err := user.Current()
	if err != nil {
		return ""
	}
	return current.Username
}
