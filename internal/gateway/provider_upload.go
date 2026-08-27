package gateway

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	M "github.com/ssfun/surge-external-bridge/internal/mihomo"
)

const MaxProviderUploadSize int64 = 8 << 20

const providerUploadPrefix = "provider-"

func (a *App) StoreProviderUpload(source io.Reader) (string, error) {
	if source == nil {
		return "", errors.New("Provider upload is required")
	}
	home := filepath.Join(a.store.Dir(), "mihomo")
	if err := M.EnsurePrivateDir(home); err != nil {
		return "", err
	}
	directory := filepath.Join(home, "uploads")
	if err := M.EnsurePrivateDir(directory); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(directory, providerUploadPrefix+"*.yaml")
	if err != nil {
		return "", fmt.Errorf("create private Provider upload: %w", err)
	}
	path := file.Name()
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return "", fmt.Errorf("secure private Provider upload: %w", err)
	}
	written, err := io.Copy(file, io.LimitReader(source, MaxProviderUploadSize+1))
	if err != nil {
		return "", fmt.Errorf("save private Provider upload: %w", err)
	}
	if written == 0 {
		return "", errors.New("Provider upload is empty")
	}
	if written > MaxProviderUploadSize {
		return "", fmt.Errorf("Provider upload exceeds %d bytes", MaxProviderUploadSize)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("sync private Provider upload: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close private Provider upload: %w", err)
	}
	committed = true
	return filepath.ToSlash(filepath.Join("uploads", filepath.Base(path))), nil
}

func (a *App) DiscardProviderUpload(path string) error {
	err := a.removeProviderUpload(path)
	if err != nil {
		a.addEvent("warn", "Provider 上传文件清理失败，将在下次启动重试: "+err.Error())
	}
	return err
}

func (a *App) reconcileProviderUploads(config Config) error {
	directory := filepath.Join(a.store.Dir(), "mihomo", "uploads")
	if _, err := os.Lstat(directory); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect private Provider upload directory: %w", err)
	}
	if err := M.EnsurePrivateDir(directory); err != nil {
		return err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read private Provider upload directory: %w", err)
	}
	active := make(map[string]struct{}, len(config.Providers))
	for _, provider := range config.Providers {
		if provider.Type != "file" {
			continue
		}
		if path, ok := a.managedProviderUploadPath(provider.FilePath); ok {
			active[filepath.Base(path)] = struct{}{}
		}
	}
	var cleanupErrors []error
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, providerUploadPrefix) || !strings.HasSuffix(name, ".yaml") {
			continue
		}
		if _, ok := active[name]; ok {
			continue
		}
		relative := filepath.ToSlash(filepath.Join("uploads", name))
		if err := a.removeProviderUpload(relative); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	return errors.Join(cleanupErrors...)
}

func (a *App) removeProviderUpload(path string) error {
	uploadPath, ok := a.managedProviderUploadPath(path)
	if !ok {
		return nil
	}
	if err := os.Remove(uploadPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove private Provider upload: %w", err)
	}
	return nil
}

func (a *App) managedProviderUploadPath(path string) (string, bool) {
	clean := filepath.Clean(filepath.FromSlash(strings.TrimSpace(path)))
	if filepath.IsAbs(clean) || filepath.Dir(clean) != "uploads" {
		return "", false
	}
	base := filepath.Base(clean)
	if !strings.HasPrefix(base, providerUploadPrefix) || !strings.HasSuffix(base, ".yaml") {
		return "", false
	}
	return filepath.Join(a.store.Dir(), "mihomo", clean), true
}
