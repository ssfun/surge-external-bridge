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

func (a *App) DiscardProviderUpload(path string) {
	if uploadPath, ok := a.managedProviderUploadPath(path); ok {
		_ = os.Remove(uploadPath)
	}
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
