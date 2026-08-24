package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ssfun/vless2surge/internal/domain"
)

type Store struct {
	dir             string
	configPath      string
	statePath       string
	transactionPath string
	mu              sync.Mutex
}

type transaction struct {
	Config domain.Config       `json:"config"`
	State  domain.RuntimeState `json:"state"`
}

func New(dir string) *Store {
	return &Store{
		dir:             dir,
		configPath:      filepath.Join(dir, "config.json"),
		statePath:       filepath.Join(dir, "state.json"),
		transactionPath: filepath.Join(dir, ".config-state-transaction.json"),
	}
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) Load() (domain.Config, domain.RuntimeState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return domain.Config{}, domain.RuntimeState{}, fmt.Errorf("create data directory: %w", err)
	}
	if err := os.Chmod(s.dir, 0o700); err != nil {
		return domain.Config{}, domain.RuntimeState{}, fmt.Errorf("secure data directory: %w", err)
	}

	config, state := domain.DefaultConfig(), domain.DefaultRuntimeState()
	recoveringTransaction := false
	var pending transaction
	if err := readJSON(s.transactionPath, &pending); err == nil {
		config, state = pending.Config, pending.State
		recoveringTransaction = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return domain.Config{}, domain.RuntimeState{}, fmt.Errorf("load interrupted config/state transaction: %w", err)
	} else {
		if err := readJSON(s.configPath, &config); err != nil && !errors.Is(err, os.ErrNotExist) {
			return domain.Config{}, domain.RuntimeState{}, fmt.Errorf("load config: %w", err)
		}
		if err := readJSON(s.statePath, &state); err != nil && !errors.Is(err, os.ErrNotExist) {
			return domain.Config{}, domain.RuntimeState{}, fmt.Errorf("load state: %w", err)
		}
	}
	if err := migrateState(&state); err != nil {
		return domain.Config{}, domain.RuntimeState{}, err
	}
	if state.Snapshots == nil {
		state.Snapshots = map[string]domain.Snapshot{}
	}
	if state.Registry == nil {
		state.Registry = map[string]domain.Identity{}
	}
	if recoveringTransaction {
		if err := writeJSONAtomic(s.configPath, config); err != nil {
			return domain.Config{}, domain.RuntimeState{}, fmt.Errorf("recover config transaction: %w", err)
		}
		if err := writeJSONAtomic(s.statePath, state); err != nil {
			return domain.Config{}, domain.RuntimeState{}, fmt.Errorf("recover state transaction: %w", err)
		}
		if err := os.Remove(s.transactionPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return domain.Config{}, domain.RuntimeState{}, fmt.Errorf("finish config/state transaction recovery: %w", err)
		}
		if err := syncDirectory(s.dir); err != nil {
			return domain.Config{}, domain.RuntimeState{}, fmt.Errorf("sync recovered config/state transaction: %w", err)
		}
	}
	return config, state, nil
}

func migrateState(state *domain.RuntimeState) error {
	if state.SchemaVersion < 0 || state.SchemaVersion > domain.SchemaVersion {
		return fmt.Errorf("unsupported state schema %d (current %d); restore a compatible backup or use the matching vless2surge version", state.SchemaVersion, domain.SchemaVersion)
	}
	for state.SchemaVersion < domain.SchemaVersion {
		switch state.SchemaVersion {
		case 0:
			// Early development states had no explicit schema marker. Their JSON
			// fields already match schema 1; App.New hydrates revision endpoint
			// snapshots that did not exist in those files.
			state.SchemaVersion = 1
		default:
			return fmt.Errorf("no migration from state schema %d to %d", state.SchemaVersion, domain.SchemaVersion)
		}
	}
	return nil
}

func (s *Store) SaveConfig(config domain.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSONAtomic(s.configPath, config)
}

func (s *Store) SaveState(state *domain.RuntimeState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state.UpdatedAt = time.Now().UTC()
	return writeJSONAtomic(s.statePath, state)
}

func (s *Store) SaveConfigAndState(config domain.Config, state *domain.RuntimeState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state.UpdatedAt = time.Now().UTC()
	pending := transaction{Config: config, State: *state}
	if err := writeJSONAtomic(s.transactionPath, pending); err != nil {
		return err
	}
	if err := writeJSONAtomic(s.configPath, config); err != nil {
		return err
	}
	if err := writeJSONAtomic(s.statePath, state); err != nil {
		return err
	}
	if err := os.Remove(s.transactionPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(s.dir)
}

func readJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return nil
}

func writeJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".vless2surge-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
