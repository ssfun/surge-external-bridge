package update

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/ssfun/surge-external-bridge/internal/service"
)

type Preferences struct {
	ProxyURL    string `json:"proxy_url"`
	AutoCheck   bool   `json:"auto_check"`
	AutoInstall bool   `json:"auto_install"`
	InstallHour int    `json:"install_hour"`
}
type Status struct {
	CurrentVersion string      `json:"current_version"`
	Latest         *Release    `json:"latest,omitempty"`
	CheckedAt      time.Time   `json:"checked_at,omitempty"`
	CheckError     string      `json:"check_error,omitempty"`
	Phase          string      `json:"phase"`
	Error          string      `json:"error,omitempty"`
	TargetVersion  string      `json:"target_version,omitempty"`
	Target         string      `json:"target,omitempty"`
	Service        bool        `json:"service"`
	CanInstall     bool        `json:"can_install"`
	InstallReason  string      `json:"install_reason,omitempty"`
	Preferences    Preferences `json:"preferences"`
}
type Runtime struct {
	PID        int
	InstanceID string
	HTTPBind   string
	PolicyHost string
}
type Manager struct {
	Freeze       func(func() error) error
	RuntimeInfo  func() Runtime
	mu           sync.Mutex
	dir, version string
	client       *Client
	runtime      Runtime
	prefs        Preferences
	latest       *Release
	checked      time.Time
	checkError   string
	busy         bool
	operation    string
}

func NewManager(dir, version string, rt Runtime) (*Manager, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	m := &Manager{dir: dir, version: version, client: NewClient(), runtime: rt, prefs: Preferences{AutoCheck: true, InstallHour: 4}}
	data, err := os.ReadFile(filepath.Join(dir, "update-settings.json"))
	if err == nil {
		err = json.Unmarshal(data, &m.prefs)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("load update settings: %w", err)
	}
	if m.prefs.InstallHour < 0 || m.prefs.InstallHour > 23 {
		return nil, errors.New("invalid update install hour")
	}
	m.client, err = NewClientWithProxy(m.prefs.ProxyURL)
	if err != nil {
		return nil, err
	}
	return m, nil
}
func (m *Manager) runtimeNow() Runtime {
	if m.RuntimeInfo != nil {
		return m.RuntimeInfo()
	}
	return m.runtime
}

func (m *Manager) Preferences(p Preferences) error {
	if p.InstallHour < 0 || p.InstallHour > 23 {
		return errors.New("安装时间必须为 0–23 点")
	}
	if p.AutoInstall && !p.AutoCheck {
		return errors.New("自动安装需要开启自动检查")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busy || Busy(m.dir) {
		return errors.New("更新正在进行，请稍后修改")
	}
	if p.AutoInstall {
		target, err := service.ResolveUpdateTarget(m.dir)
		if err != nil {
			return err
		}
		if !target.Service {
			return errors.New("自动安装需要当前实例已安装系统自启服务")
		}
		if !StableVersion(m.version) {
			return errors.New("开发版本不能开启自动安装")
		}
	}
	var replacement *Client
	if p.ProxyURL != m.prefs.ProxyURL {
		var err error
		replacement, err = NewClientWithProxy(p.ProxyURL)
		if err != nil {
			return err
		}
	}
	if err := writeJSON(filepath.Join(m.dir, "update-settings.json"), p); err != nil {
		return err
	}
	if replacement != nil {
		m.client.HTTP.CloseIdleConnections()
		m.client = replacement
	}
	m.prefs = p
	return nil
}
func ReadJournal(dir string) (Journal, error) {
	var j Journal
	data, err := os.ReadFile(filepath.Join(dir, "update-transaction.json"))
	if err != nil {
		return j, err
	}
	err = json.Unmarshal(data, &j)
	return j, err
}
func Busy(dir string) bool {
	j, err := ReadJournal(dir)
	return (err != nil && !errors.Is(err, os.ErrNotExist)) || (err == nil && activePhase(j.Phase))
}
func (m *Manager) Status() Status {
	m.mu.Lock()
	s := Status{CurrentVersion: m.version, Latest: m.latest, CheckedAt: m.checked, CheckError: m.checkError, Preferences: m.prefs, Phase: "idle"}
	busy := m.busy
	operation := m.operation
	m.mu.Unlock()
	if busy {
		s.Phase = operation
	}
	j, err := ReadJournal(m.dir)
	if err == nil {
		s.TargetVersion = j.Version
		s.Error = j.Error
		if !busy {
			s.Phase = j.Phase
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		s.Error = "更新记录无法读取"
		s.Phase = "recovery_failed"
	}
	target, err := service.ResolveUpdateTarget(m.dir)
	if err != nil {
		s.InstallReason = err.Error()
	} else {
		s.Target = target.Executable
		s.Service = target.Service
		switch {
		case !StableVersion(m.version):
			s.InstallReason = "开发或未知版本，请手动安装正式 Release"
		case !target.Service && m.runtimeNow().PID != 0:
			s.InstallReason = "手动运行实例：停止程序后执行 SurgeEB update install，或先开启自动启动"
		default:
			s.CanInstall = true
		}
	}
	if busy || Busy(m.dir) {
		s.CanInstall = false
		if s.InstallReason == "" {
			s.InstallReason = "更新正在执行或等待恢复"
		}
	}
	return s
}
func (m *Manager) Check(ctx context.Context) error {
	m.mu.Lock()
	if m.busy {
		m.mu.Unlock()
		return errors.New("更新操作正在进行")
	}
	m.busy = true
	m.operation = "checking"
	m.mu.Unlock()
	release, err := m.client.Check(ctx, m.version)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.busy = false
	m.checked = time.Now().UTC()
	if err != nil {
		m.checkError = err.Error()
		return err
	}
	m.latest = release
	m.checkError = ""
	return nil
}
func (m *Manager) StartInstall(ctx context.Context, version string, async bool) error {
	return m.startInstall(ctx, version, async, false)
}
func (m *Manager) startInstall(ctx context.Context, version string, async, automatic bool) error {
	m.mu.Lock()
	if automatic && (!m.prefs.AutoInstall || !m.prefs.AutoCheck || time.Now().Hour() != m.prefs.InstallHour) {
		m.mu.Unlock()
		return errors.New("自动安装已关闭或不在安装时段")
	}
	if m.busy || Busy(m.dir) {
		m.mu.Unlock()
		return errors.New("更新操作正在进行或需要恢复")
	}
	if m.latest == nil || m.latest.Tag != version {
		m.mu.Unlock()
		return errors.New("目标版本已改变，请重新检查更新")
	}
	r := *m.latest
	m.checkError = ""
	m.operation = "downloading"
	m.busy = true
	m.mu.Unlock()
	work := func() error {
		defer func() { m.mu.Lock(); m.busy = false; m.mu.Unlock() }()
		err := m.install(ctx, r)
		if err != nil {
			m.mu.Lock()
			m.checkError = err.Error()
			m.mu.Unlock()
		}
		return err
	}
	if !async {
		return work()
	}
	// An accepted operation outlives the HTTP request but has a bounded download.
	go func() { _ = work() }()
	return nil
}
func (m *Manager) install(ctx context.Context, r Release) error {
	lockFile, err := lock(filepath.Join(m.dir, "update.lock"))
	if err != nil {
		return err
	}
	defer unlock(lockFile)
	if Busy(m.dir) {
		return errors.New("已有更新等待完成或恢复")
	}
	// A CLI manager has no in-process write gate or runtime identity. It may
	// prepare directly only while the instance is stopped, and must keep it
	// stopped through preparation and worker dispatch (or offline installation).
	if m.runtimeNow().PID == 0 {
		instance, err := LockInstance(m.dir)
		if err != nil {
			return fmt.Errorf("实例已启动，请重新执行 update install 通过运行实例更新：%w", err)
		}
		defer UnlockInstance(instance)
	}
	target, err := service.ResolveUpdateTarget(m.dir)
	if err != nil {
		return err
	}
	if !target.Service && m.runtimeNow().PID != 0 {
		return errors.New("手动运行实例请停止后通过 CLI 更新，或先开启自动启动")
	}
	if err = regularFile(target.Executable); err != nil {
		return err
	}
	targetLock, err := lock(filepath.Join(filepath.Dir(target.Executable), ".SurgeEB-update.lock"))
	if err != nil {
		return fmt.Errorf("更新目录不可写或另一个实例正在更新：%w", err)
	}
	defer unlock(targetLock)
	if err = checkProtocol(target.Executable); err != nil {
		return err
	}
	previous, err := binaryVersion(target.Executable)
	if err != nil {
		return err
	}
	if !StableVersion(previous) {
		return errors.New("已安装副本不是正式版本，请手动安装 Release")
	}
	if newer, err := selectRelease([]Release{r}, previous); err != nil || newer == nil {
		return errors.New("服务副本已是相同或更高版本，请重启服务后重新检查")
	}
	previousDigest, err := fileDigest(target.Executable)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	staged, digest, err := m.client.Download(ctx, r, runtime.GOOS, runtime.GOARCH, target.Executable)
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			os.Remove(staged)
		}
	}()
	if err = ValidateBinary(staged, r.Tag, runtime.GOOS, runtime.GOARCH); err != nil {
		return err
	}
	currentDigest, err := fileDigest(target.Executable)
	if err != nil {
		return err
	}
	if currentDigest != previousDigest {
		return errors.New("安装目标在下载期间发生变化")
	}
	freshTarget, err := service.ResolveUpdateTarget(m.dir)
	if err != nil {
		return err
	}
	if freshTarget != target {
		return errors.New("服务配置在下载期间发生变化，请重新检查")
	}

	idBytes := make([]byte, 12)
	if _, err = rand.Read(idBytes); err != nil {
		return err
	}
	var j Journal
	prepare := func() error {
		rt := m.runtimeNow()
		j = Journal{ID: hex.EncodeToString(idBytes), DataDir: m.dir, Target: target.Executable, Staged: staged, Digest: digest, PreviousDigest: previousDigest, Version: r.Tag, PreviousVersion: previous, Service: target.Service, PID: rt.PID, InstanceID: rt.InstanceID}
		fresh, err := service.ResolveUpdateTarget(m.dir)
		if err != nil {
			return err
		}
		if fresh != target {
			return errors.New("服务配置在下载期间发生变化")
		}
		if target.Service {
			j.DefinitionDigest, err = fileDigest(target.Definition)
			if err != nil {
				return err
			}
		}
		if rt.PID != 0 {
			j.HealthURL = healthURL(rt.HTTPBind)
			j.HealthHost = net.JoinHostPort(rt.PolicyHost, portOf(rt.HTTPBind))
		} else {
			var config struct {
				HTTPBind   string `json:"http_bind"`
				PolicyHost string `json:"policy_host"`
			}
			data, err := os.ReadFile(filepath.Join(m.dir, "gateway.json"))
			if err != nil {
				return err
			}
			if err = json.Unmarshal(data, &config); err != nil {
				return err
			}
			j.HealthURL = healthURL(config.HTTPBind)
			j.HealthHost = net.JoinHostPort(config.PolicyHost, portOf(config.HTTPBind))
		}
		return j.save("prepared")
	}
	if m.Freeze != nil {
		err = m.Freeze(prepare)
	} else {
		err = prepare()
	}
	if err != nil {
		return err
	}

	if target.Service {
		worker := filepath.Join(m.dir, "update-worker")
		running, err := os.Executable()
		if err != nil {
			j.Error = err.Error()
			_ = j.save("failed")
			return err
		}
		if err = copyFile(running, worker, 0o700); err != nil {
			j.Error = err.Error()
			_ = j.save("failed")
			return err
		}
		if err = service.LaunchUpdateWorker(worker, m.dir, j.ID); err != nil {
			j.Error = err.Error()
			_ = j.save("failed")
			return err
		}
		keep = true
		return nil
	}
	err = runTransaction(&j, transactionOps{stop: func() error { return nil }})
	return err
}
func portOf(bind string) string { _, port, _ := net.SplitHostPort(bind); return port }
func healthURL(bind string) string {
	host, port, err := net.SplitHostPort(bind)
	if err != nil {
		return ""
	}
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if host == "::" {
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/health"
}
func (m *Manager) Run(ctx context.Context) {
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			m.mu.Lock()
			p, checked, busy := m.prefs, m.checked, m.busy
			m.mu.Unlock()
			if p.AutoCheck && StableVersion(m.version) && !busy && !Busy(m.dir) {
				if checked.IsZero() || time.Since(checked) >= 24*time.Hour {
					_ = m.Check(ctx)
				}
				m.mu.Lock()
				latest, checkError := m.latest, m.checkError
				m.mu.Unlock()
				if p.AutoInstall && latest != nil && checkError == "" && time.Now().Hour() == p.InstallHour {
					j, err := ReadJournal(m.dir)
					// A failed release requires explicit retry, never a restart loop.
					if errors.Is(err, os.ErrNotExist) || (err == nil && j.Version != latest.Tag) {
						_ = m.startInstall(ctx, latest.Tag, false, true)
					}
				}
			}
			timer.Reset(time.Minute)
		}
	}
}
