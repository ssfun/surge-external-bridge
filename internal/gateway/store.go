package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type Store struct {
	dir        string
	configPath string
}

func NewStore(dir string) *Store {
	return &Store{dir: dir, configPath: filepath.Join(dir, "gateway.json")}
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) Load() (Config, bool, error) {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return Config{}, false, err
	}
	dirInfo, err := os.Lstat(s.dir)
	if err != nil {
		return Config{}, false, err
	}
	if dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
		return Config{}, false, errors.New("data directory must be a real directory, not a symbolic link")
	}
	if err := os.Chmod(s.dir, 0o700); err != nil {
		return Config{}, false, err
	}
	configInfo, statErr := os.Lstat(s.configPath)
	if statErr == nil {
		if configInfo.Mode()&os.ModeSymlink != 0 || !configInfo.Mode().IsRegular() {
			return Config{}, false, errors.New("gateway.json must be a regular file, not a symbolic link")
		}
		if err := os.Chmod(s.configPath, 0o600); err != nil {
			return Config{}, false, err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Config{}, false, statErr
	}
	var config Config
	data, err := os.ReadFile(s.configPath)
	if errors.Is(err, os.ErrNotExist) {
		legacy, migrated, migrationErr := s.migrateLegacy()
		if migrationErr != nil {
			return Config{}, false, migrationErr
		}
		if migrated {
			if err := ValidateConfig(legacy); err != nil {
				return Config{}, false, fmt.Errorf("validate migrated config: %w", err)
			}
			if err := s.Save(legacy); err != nil {
				return Config{}, false, err
			}
			return legacy, true, nil
		}
		config = DefaultConfig()
		if err := s.Save(config); err != nil {
			return Config{}, false, err
		}
		return config, false, nil
	}
	if err != nil {
		return Config{}, false, err
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return Config{}, false, fmt.Errorf("decode gateway.json: %w", err)
	}
	if config.SchemaVersion != SchemaVersion {
		return Config{}, false, fmt.Errorf("unsupported gateway schema %d", config.SchemaVersion)
	}
	defaults := DefaultConfig()
	if config.NodeTestURL == "" {
		config.NodeTestURL = defaults.NodeTestURL
	}
	if config.NodeTestUDP == "" {
		config.NodeTestUDP = defaults.NodeTestUDP
	}
	if config.NodeTestTimeout == 0 {
		config.NodeTestTimeout = defaults.NodeTestTimeout
	}
	normalizedSource := false
	for index := range config.Providers {
		provider := &config.Providers[index]
		if provider.Type == "http" && provider.SizeLimit == 0 {
			provider.SizeLimit = 16 << 20
		}
		if provider.HealthCheck {
			if provider.HealthCheckURL == "" {
				provider.HealthCheckURL = "https://www.gstatic.com/generate_204"
			}
			if provider.HealthCheckSeconds == 0 {
				provider.HealthCheckSeconds = 300
			}
			if provider.HealthCheckTimeout == 0 {
				provider.HealthCheckTimeout = 5000
			}
			if provider.ExpectedStatus == "" {
				provider.ExpectedStatus = "200-399"
			}
		}
		normalizedSource = normalizeProviderSource(provider) || normalizedSource
	}
	if normalizedSource {
		if err := ValidateConfig(config); err != nil {
			return Config{}, false, fmt.Errorf("validate normalized gateway.json: %w", err)
		}
		if err := s.Save(config); err != nil {
			return Config{}, false, err
		}
	}
	return config, false, nil
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
	return os.Chmod(s.configPath, 0o600)
}

type legacyConfig struct {
	Mode               string `json:"mode"`
	HTTPBind           string `json:"http_bind"`
	SocksBind          string `json:"socks_bind"`
	SocksPort          uint16 `json:"socks_port"`
	SocksAdvertise     string `json:"socks_advertise"`
	PolicyBaseURL      string `json:"policy_base_url"`
	ManagementToken    string `json:"management_token"`
	PolicyToken        string `json:"policy_token"`
	PrefixSubscription bool   `json:"prefix_subscription"`
	ExcludeName        string `json:"exclude_name"`
	Subscriptions      []struct {
		ID             string            `json:"id"`
		Name           string            `json:"name"`
		SourceType     string            `json:"source_type"`
		URL            string            `json:"url"`
		Filter         string            `json:"filter"`
		Enabled        bool              `json:"enabled"`
		Headers        map[string]string `json:"headers"`
		RefreshSeconds int               `json:"refresh_seconds"`
	} `json:"subscriptions"`
}

func (s *Store) migrateLegacy() (Config, bool, error) {
	legacyPath := filepath.Join(s.dir, "config.json")
	data, err := os.ReadFile(legacyPath)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, err
	}
	var legacy legacyConfig
	if err := json.Unmarshal(data, &legacy); err != nil {
		return Config{}, false, fmt.Errorf("decode legacy config for migration: %w", err)
	}
	if err := s.backupLegacy(); err != nil {
		return Config{}, false, err
	}
	config := DefaultConfig()
	config.Mode, config.HTTPBind = legacy.Mode, legacy.HTTPBind
	config.SocksBind, config.SocksPort, config.SocksAdvertise = legacy.SocksBind, legacy.SocksPort, legacy.SocksAdvertise
	config.PolicyBaseURL = legacy.PolicyBaseURL
	config.ManagementToken, config.PolicyToken = legacy.ManagementToken, legacy.PolicyToken
	config.NodeTestURL, config.NodeTestUDP, config.NodeTestTimeout = "https://www.gstatic.com/generate_204", "8.8.8.8:53", 15
	config.PrefixProvider = legacy.PrefixSubscription
	for _, subscription := range legacy.Subscriptions {
		if subscription.SourceType == "manual" || subscription.URL == "" {
			continue
		}
		headers := make(map[string][]string, len(subscription.Headers))
		for name, value := range subscription.Headers {
			headers[name] = []string{value}
		}
		config.Providers = append(config.Providers, Provider{
			StableID: subscription.ID, Name: subscription.Name, Type: "http", URL: subscription.URL,
			Enabled: subscription.Enabled, Headers: headers, RefreshSeconds: subscription.RefreshSeconds,
			IncludeName: subscription.Filter, ExcludeName: legacy.ExcludeName,
			HealthCheck: true, HealthCheckURL: "https://www.gstatic.com/generate_204", HealthCheckSeconds: 300,
			HealthCheckTimeout: 5000, HealthCheckLazy: true, ExpectedStatus: "200-399", SizeLimit: 16 << 20,
		})
	}
	return config, true, nil
}

func (s *Store) backupLegacy() error {
	backupDir := filepath.Join(s.dir, "migration-v1-readonly")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return err
	}
	for _, name := range []string{"config.json", "state.json"} {
		source := filepath.Join(s.dir, name)
		target := filepath.Join(backupDir, name)
		if info, err := os.Lstat(target); err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return fmt.Errorf("legacy backup %s must be a regular file", name)
			}
			if err := os.Chmod(target, 0o400); err != nil {
				return err
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if info, err := os.Lstat(source); err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return fmt.Errorf("legacy source %s must be a regular file", name)
			}
		} else if errors.Is(err, os.ErrNotExist) {
			continue
		} else {
			return err
		}
		input, err := os.Open(source)
		if err != nil {
			return err
		}
		output, err := os.CreateTemp(backupDir, "."+name+"-*.tmp")
		if err != nil {
			_ = input.Close()
			return err
		}
		temporary := output.Name()
		if err := output.Chmod(0o600); err != nil {
			_ = input.Close()
			_ = output.Close()
			_ = os.Remove(temporary)
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeErr := errors.Join(input.Close(), output.Sync(), output.Close())
		if err := errors.Join(copyErr, closeErr); err != nil {
			_ = os.Remove(temporary)
			return err
		}
		if err := os.Chmod(temporary, 0o400); err != nil {
			_ = os.Remove(temporary)
			return err
		}
		if err := os.Rename(temporary, target); err != nil {
			_ = os.Remove(temporary)
			return err
		}
	}
	return nil
}
