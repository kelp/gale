package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// TmpDir returns a writable scratch directory. It prefers
// ~/.gale/tmp so gc can sweep interrupted work. When that path
// is unusable it falls back to os.TempDir(). Only when neither
// location works does it return an error, never "".
func TmpDir() (string, error) {
	dir, err := galeHomeSubdir("tmp")
	if err == nil {
		return dir, nil
	}
	fallback := os.TempDir()
	if fbErr := ensureWritableDir(fallback); fbErr != nil {
		return "", fmt.Errorf("no usable temp dir: %w; %w", err, fbErr)
	}
	return fallback, nil
}

func ensureWritableDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	probe, err := os.CreateTemp(dir, ".gale-probe-*")
	if err != nil {
		return fmt.Errorf("write to %s: %w", dir, err)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return nil
}

func galeHomeSubdir(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	dir := filepath.Join(home, ".gale", name)
	if err := ensureWritableDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}
