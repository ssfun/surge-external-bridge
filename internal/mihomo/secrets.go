package mihomo

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const MasterKeySize = 32

func EnsurePrivateDir(path string) error {
	if path == "" {
		return errors.New("private data directory is required")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create private data directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect private data directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("private data directory must be a real directory, not a symbolic link")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("secure private data directory: %w", err)
	}
	return nil
}

// SecurePrivateTree enforces the product permission boundary after Mihomo has
// initialized or refreshed Provider resources. Mihomo upstream creates cache
// files using the process umask; this explicit pass also protects library use
// where SurgeEB's CLI-level 0077 umask is not installed.
func SecurePrivateTree(root string) error {
	if err := EnsurePrivateDir(root); err != nil {
		return err
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("private Mihomo tree contains a symbolic link: %s", filepath.Base(path))
		}
		switch {
		case info.IsDir():
			return os.Chmod(path, 0o700)
		case info.Mode().IsRegular(), info.Mode()&os.ModeSocket != 0:
			return os.Chmod(path, 0o600)
		default:
			return fmt.Errorf("private Mihomo tree contains an unsupported file type: %s", filepath.Base(path))
		}
	})
}

func PrivateTreeProtected(root string) bool {
	protected := true
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			protected = false
			return walkErr
		}
		info, err := entry.Info()
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			protected = false
			return nil
		}
		if info.IsDir() {
			protected = protected && info.Mode().Perm() == 0o700
		} else if info.Mode().IsRegular() || info.Mode()&os.ModeSocket != 0 {
			protected = protected && info.Mode().Perm() == 0o600
		} else {
			protected = false
		}
		return nil
	}); err != nil {
		return false
	}
	return protected
}

func LoadOrCreatePrivateKey(path string) ([]byte, error) {
	if err := EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	info, statErr := os.Lstat(path)
	if statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, errors.New("private key must be a regular file, not a symbolic link")
		}
		key, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read private key: %w", err)
		}
		if len(key) != MasterKeySize {
			return nil, fmt.Errorf("private key has invalid size %d", len(key))
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("secure private key: %w", err)
		}
		return key, nil
	}
	if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect private key: %w", statErr)
	}

	key := make([]byte, MasterKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate private key: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return LoadOrCreatePrivateKey(path)
	}
	if err != nil {
		return nil, fmt.Errorf("create private key: %w", err)
	}
	if _, err := file.Write(key); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("write private key: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("sync private key: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close private key: %w", err)
	}
	return key, nil
}
