package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type Store struct {
	dir        string
	configPath string
	notices    []string
}

type configV1 struct {
	Config
	VirtualHost string `json:"virtual_host"`
}

func NewStore(dir string) *Store {
	return &Store{dir: dir, configPath: filepath.Join(dir, "gateway.json")}
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) Notices() []string { return append([]string(nil), s.notices...) }

func (s *Store) Load() (Config, error) {
	s.notices = nil
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return Config{}, err
	}
	dirInfo, err := os.Lstat(s.dir)
	if err != nil {
		return Config{}, err
	}
	if dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
		return Config{}, errors.New("data directory must be a real directory, not a symbolic link")
	}
	if err := os.Chmod(s.dir, 0o700); err != nil {
		return Config{}, err
	}
	configInfo, statErr := os.Lstat(s.configPath)
	if statErr == nil {
		if configInfo.Mode()&os.ModeSymlink != 0 || !configInfo.Mode().IsRegular() {
			return Config{}, errors.New("gateway.json must be a regular file, not a symbolic link")
		}
		if err := os.Chmod(s.configPath, 0o600); err != nil {
			return Config{}, err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Config{}, statErr
	}
	var config Config
	data, err := os.ReadFile(s.configPath)
	if errors.Is(err, os.ErrNotExist) {
		config, err = DefaultConfig()
		if err != nil {
			return Config{}, fmt.Errorf("generate projection key: %w", err)
		}
		if err := s.Save(config); err != nil {
			return Config{}, err
		}
		return config, nil
	}
	if err != nil {
		return Config{}, err
	}
	var schema struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		return Config{}, fmt.Errorf("decode gateway.json: %w", err)
	}
	migrated := false
	switch schema.SchemaVersion {
	case 1:
		var legacy configV1
		if err := json.Unmarshal(data, &legacy); err != nil {
			return Config{}, fmt.Errorf("decode gateway.json schema 1: %w", err)
		}
		config = legacy.Config
		config.SchemaVersion = SchemaVersion
		config.SocksHost = legacy.VirtualHost
		config.PolicyHost = legacy.VirtualHost
		migrated = true
	case SchemaVersion:
		if err := json.Unmarshal(data, &config); err != nil {
			return Config{}, fmt.Errorf("decode gateway.json: %w", err)
		}
	default:
		return Config{}, fmt.Errorf("unsupported gateway schema %d", schema.SchemaVersion)
	}
	assignProviderIDs(&config)
	if config.PolicyToken == "unsafe" {
		for config.PolicyToken == "unsafe" || config.PolicyToken == config.ManagementToken {
			config.PolicyToken, err = randomToken()
			if err != nil {
				return Config{}, fmt.Errorf("replace legacy unsafe Policy Token: %w", err)
			}
		}
		s.notices = append(s.notices, "安全升级已将旧的 unsafe Policy Token 替换为强随机值；请更新 Surge Policy Path")
		migrated = true
	}
	for index := range config.Providers {
		provider := &config.Providers[index]
		if provider.Enabled && providerHasRedirectSensitiveCredentials(*provider) {
			provider.Enabled = false
			s.notices = append(s.notices, fmt.Sprintf("安全升级已停用 Provider %s：请移除 Authorization、Cookie 或 URL userinfo 后再启用", provider.Name))
			migrated = true
		}
	}
	if migrated {
		if err := s.Save(config); err != nil {
			return Config{}, fmt.Errorf("persist gateway configuration migration: %w", err)
		}
	}
	return config, nil
}

func (s *Store) Save(config Config) error {
	config.SchemaVersion = SchemaVersion
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(s.dir, ".gateway-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
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
	if err := os.Rename(temporaryPath, s.configPath); err != nil {
		return err
	}
	// The temporary file was already chmodded before publication. Returning an
	// error after rename would falsely tell callers persistence failed even
	// though gateway.json already contains the candidate configuration.
	return nil
}
