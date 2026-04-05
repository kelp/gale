package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/kelp/gale/internal/atomicfile"
	"github.com/kelp/gale/internal/filelock"
)

// ErrGaleConfigNotFound is returned when gale.toml cannot be
// found in the directory tree.
var ErrGaleConfigNotFound = errors.New(
	"gale.toml not found",
)

// ErrPackageNotFound is returned when a package to remove does
// not exist in the config.
var ErrPackageNotFound = errors.New("package not found")

// GaleConfig represents a gale.toml file (global or project).
type GaleConfig struct {
	Packages map[string]string `toml:"packages"`
	Vars     map[string]string `toml:"vars"`
	Pinned   map[string]bool   `toml:"pinned,omitempty"`
}

// ParseGaleConfig parses a gale.toml string.
func ParseGaleConfig(data string) (*GaleConfig, error) {
	var cfg GaleConfig
	if _, err := toml.Decode(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing gale config: %w", err)
	}
	return &cfg, nil
}

// FindGaleConfig walks up from dir to find gale.toml.
// Returns the path to the found file.
func FindGaleConfig(dir string) (string, error) {
	path := findFileUp(dir, "gale.toml")
	if path == "" {
		return "", ErrGaleConfigNotFound
	}
	return path, nil
}

// findFileUp walks up from dir looking for a file with the
// given name. Returns the full path if found, empty if not.
func findFileUp(dir, name string) string {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for {
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// WriteGaleConfig writes a GaleConfig to the given path atomically.
func WriteGaleConfig(path string, cfg *GaleConfig) error {
	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	if err := enc.Encode(cfg); err != nil {
		return fmt.Errorf("encoding gale config: %w", err)
	}
	return atomicfile.Write(path, buf.Bytes())
}

// withFileLock acquires an exclusive file lock on a .lock
// sibling of path, runs fn, and releases the lock. This
// serializes concurrent read-modify-write operations.
func withFileLock(path string, fn func() error) error {
	return filelock.With(path+".lock", fn)
}

// AddPackage adds or updates a package in the gale.toml at path.
// If the file does not exist, it bootstraps an empty config.
func AddPackage(path string, name, version string) error {
	return withFileLock(path, func() error {
		var cfg *GaleConfig

		data, err := os.ReadFile(path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("reading gale config: %w", err)
			}
			cfg = &GaleConfig{}
		} else {
			cfg, err = ParseGaleConfig(string(data))
			if err != nil {
				return err
			}
		}

		if cfg.Packages == nil {
			cfg.Packages = make(map[string]string)
		}
		cfg.Packages[name] = version

		return WriteGaleConfig(path, cfg)
	})
}

// RemovePackage removes a package from the gale.toml at path.
func RemovePackage(path string, name string) error {
	return withFileLock(path, func() error {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading gale config: %w", err)
		}

		cfg, err := ParseGaleConfig(string(data))
		if err != nil {
			return err
		}

		if cfg.Packages == nil {
			return ErrPackageNotFound
		}
		if _, exists := cfg.Packages[name]; !exists {
			return ErrPackageNotFound
		}
		delete(cfg.Packages, name)

		return WriteGaleConfig(path, cfg)
	})
}

// PinPackage marks a package as pinned in the gale.toml at path.
// Returns ErrPackageNotFound if the package is not in [packages].
func PinPackage(path string, name string) error {
	return withFileLock(path, func() error {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading gale config: %w", err)
		}

		cfg, err := ParseGaleConfig(string(data))
		if err != nil {
			return err
		}

		if _, ok := cfg.Packages[name]; !ok {
			return ErrPackageNotFound
		}

		if cfg.Pinned == nil {
			cfg.Pinned = make(map[string]bool)
		}
		cfg.Pinned[name] = true

		return WriteGaleConfig(path, cfg)
	})
}

// UnpinPackage removes a pin from the gale.toml at path.
func UnpinPackage(path string, name string) error {
	return withFileLock(path, func() error {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading gale config: %w", err)
		}

		cfg, err := ParseGaleConfig(string(data))
		if err != nil {
			return err
		}

		delete(cfg.Pinned, name)

		return WriteGaleConfig(path, cfg)
	})
}
