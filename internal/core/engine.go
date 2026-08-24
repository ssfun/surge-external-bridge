package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ssfun/vless2surge/internal/domain"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	boxoption "github.com/sagernet/sing-box/option"
)

type Manager struct {
	mu       sync.Mutex
	instance *box.Box
	cancel   context.CancelFunc
	status   domain.EngineStatus
	compiled []byte
	revision *domain.Revision
	onEvent  func(level, message string)
}

func NewManager(onEvent func(level, message string)) *Manager {
	return &Manager{
		status:  domain.EngineStatus{State: "stopped", CoreVersion: CoreVersion},
		onEvent: onEvent,
	}
}

func (m *Manager) Status() domain.EngineStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *Manager) Validate(compiled []byte) error {
	instance, cancel, err := createInstance(compiled)
	if err != nil {
		return err
	}
	_ = instance.Close()
	cancel()
	return nil
}

func (m *Manager) Start(revision *domain.Revision, compiled []byte, inbound string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.instance != nil {
		return errors.New("engine is already running")
	}
	m.status.State = "starting"
	m.status.LastError = ""
	instance, cancel, err := createAndStart(compiled)
	if err != nil {
		m.failLocked(err)
		return err
	}
	m.instance = instance
	m.cancel = cancel
	m.compiled = append([]byte(nil), compiled...)
	m.revision = revision
	m.status = statusForRevision("running", revision, inbound)
	m.emit("info", fmt.Sprintf("Embedded sing-box %s started revision=%s users=%d", CoreVersion, revision.ID, len(revision.Nodes)))
	return nil
}

func (m *Manager) Apply(revision *domain.Revision, compiled []byte, inbound string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if revision == nil {
		return errors.New("revision is required")
	}
	m.status.State = "applying"
	m.status.LastError = ""
	candidate, candidateCancel, err := createInstance(compiled)
	if err != nil {
		m.failLocked(err)
		return err
	}
	previousBytes := append([]byte(nil), m.compiled...)
	previousRevision := m.revision
	previousRunning := m.instance != nil
	previousInbound := m.status.Inbound
	if m.instance != nil {
		m.closeLocked()
	}
	if err := candidate.Start(); err != nil {
		_ = candidate.Close()
		candidateCancel()
		rollbackErr := error(nil)
		if previousRunning && len(previousBytes) > 0 && previousRevision != nil {
			rollbackErr = m.startLocked(previousRevision, previousBytes, previousInbound)
		}
		if rollbackErr != nil {
			err = fmt.Errorf("start candidate: %v; rollback failed: %w", err, rollbackErr)
			m.failLocked(err)
			return err
		}
		if previousRunning {
			m.status.LastError = "candidate apply failed; previous revision restored: " + err.Error()
			m.emit("error", m.status.LastError)
		} else {
			m.failLocked(err)
		}
		return err
	}
	m.instance = candidate
	m.cancel = candidateCancel
	m.compiled = append([]byte(nil), compiled...)
	m.revision = revision
	m.status = statusForRevision("running", revision, inbound)
	m.emit("info", fmt.Sprintf("applied revision=%s users=%d", revision.ID, len(revision.Nodes)))
	return nil
}

func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.instance == nil {
		m.status.State = "stopped"
		return nil
	}
	m.status.State = "stopping"
	err := m.closeLocked()
	m.status.State = "stopped"
	m.status.StartedAt = time.Time{}
	m.emit("info", "Embedded engine stopped")
	return err
}

func (m *Manager) closeLocked() error {
	if m.instance == nil {
		return nil
	}
	if m.cancel != nil {
		m.cancel()
	}
	err := m.instance.Close()
	m.instance = nil
	m.cancel = nil
	return err
}

func (m *Manager) startLocked(revision *domain.Revision, compiled []byte, inbound string) error {
	instance, cancel, err := createAndStart(compiled)
	if err != nil {
		return err
	}
	m.instance = instance
	m.cancel = cancel
	m.compiled = append([]byte(nil), compiled...)
	m.revision = revision
	m.status = statusForRevision("running", revision, inbound)
	return nil
}

func (m *Manager) failLocked(err error) {
	m.status.State = "error"
	m.status.LastError = err.Error()
	m.emit("error", "Embedded engine: "+err.Error())
}

func (m *Manager) emit(level, message string) {
	if m.onEvent != nil {
		m.onEvent(level, message)
	}
}

func statusForRevision(state string, revision *domain.Revision, inbound string) domain.EngineStatus {
	return domain.EngineStatus{
		State:       state,
		Revision:    revision.ID,
		StartedAt:   time.Now().UTC(),
		CoreVersion: CoreVersion,
		Inbound:     inbound,
		Users:       len(revision.Nodes),
		Outbounds:   len(revision.Nodes),
	}
}

func createAndStart(compiled []byte) (*box.Box, context.CancelFunc, error) {
	instance, cancel, err := createInstance(compiled)
	if err != nil {
		return nil, nil, err
	}
	if err := instance.Start(); err != nil {
		_ = instance.Close()
		cancel()
		return nil, nil, fmt.Errorf("start embedded sing-box: %w", err)
	}
	return instance, cancel, nil
}

func createInstance(compiled []byte) (*box.Box, context.CancelFunc, error) {
	base := include.Context(context.Background())
	ctx, cancel := context.WithCancel(base)
	var options boxoption.Options
	if err := options.UnmarshalJSONContext(ctx, compiled); err != nil {
		cancel()
		return nil, nil, fmt.Errorf("decode sing-box config: %w", err)
	}
	instance, err := box.New(box.Options{Context: ctx, Options: options})
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("create embedded sing-box: %w", err)
	}
	return instance, cancel, nil
}
