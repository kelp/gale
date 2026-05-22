package config

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

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

// HostConfig represents a per-host packages/pinned overlay
// stored under [hosts.<name>] in gale.toml.
type HostConfig struct {
	Packages map[string]string `toml:"packages,omitempty"`
	Pinned   map[string]bool   `toml:"pinned,omitempty"`
}

// GaleConfig represents a gale.toml file (global or project).
type GaleConfig struct {
	Packages map[string]string     `toml:"packages"`
	Vars     map[string]string     `toml:"vars,omitempty"`
	Pinned   map[string]bool       `toml:"pinned,omitempty"`
	Hosts    map[string]HostConfig `toml:"hosts,omitempty"`
}

// ParseGaleConfig parses a gale.toml string.
func ParseGaleConfig(data string) (*GaleConfig, error) {
	var cfg GaleConfig
	if _, err := toml.Decode(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing gale config: %w", err)
	}
	return &cfg, nil
}

// EffectivePackages returns the shared [packages] merged with
// every [hosts.<key>.packages] section whose key matches host.
// Host section keys may list multiple comma-separated patterns
// and use glob wildcards (*, ?). Wildcard-bearing sections are
// applied first; exact-name sections last, so exact entries
// override globs. Does not mutate the receiver.
func (c *GaleConfig) EffectivePackages(host string) map[string]string {
	out := make(map[string]string, len(c.Packages))
	maps.Copy(out, c.Packages)
	if host == "" {
		return out
	}
	for _, k := range matchingHostKeys(c.Hosts, host) {
		maps.Copy(out, c.Hosts[k].Packages)
	}
	return out
}

// ApplyHost replaces Packages and Pinned with the effective
// merged maps for the given host. Mutates the receiver.
// Callers that need the raw on-disk view (e.g. mutators) must
// not call this.
func (c *GaleConfig) ApplyHost(host string) {
	c.Packages = c.EffectivePackages(host)
	c.Pinned = c.EffectivePinned(host)
}

// EffectivePinned merges shared [pinned] with every matching
// [hosts.<key>.pinned] overlay, using the same multi-pattern
// matching and override order as EffectivePackages. Does not
// mutate the receiver.
func (c *GaleConfig) EffectivePinned(host string) map[string]bool {
	out := make(map[string]bool, len(c.Pinned))
	maps.Copy(out, c.Pinned)
	if host == "" {
		return out
	}
	for _, k := range matchingHostKeys(c.Hosts, host) {
		maps.Copy(out, c.Hosts[k].Pinned)
	}
	return out
}

// hostKeyMatches reports whether sectionKey applies to the
// given host. The key is a comma-separated list of glob
// patterns; any matching pattern returns true.
func hostKeyMatches(sectionKey, host string) bool {
	for pat := range strings.SplitSeq(sectionKey, ",") {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			continue
		}
		if pat == host {
			return true
		}
		if ok, err := filepath.Match(pat, host); err == nil && ok {
			return true
		}
	}
	return false
}

// hostKeySpecificity ranks a section key from least to most
// specific so callers can apply broader sections first and
// let narrower ones override. Order: glob (0) < comma-list
// of literals (1) < single literal name (2).
func hostKeySpecificity(sectionKey string) int {
	if strings.ContainsAny(sectionKey, "*?[") {
		return 0
	}
	if strings.Contains(sectionKey, ",") {
		return 1
	}
	return 2
}

// matchingHostKeys returns the host section keys that apply
// to host, sorted from least to most specific so the caller
// can apply each section in order — later sections override
// earlier ones, so exact-name entries win over comma-lists,
// which in turn win over globs. Within each tier, keys are
// sorted alphabetically for deterministic merge order.
func matchingHostKeys(hosts map[string]HostConfig, host string) []string {
	keys := make([]string, 0, len(hosts))
	for k := range hosts {
		if hostKeyMatches(k, host) {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		si := hostKeySpecificity(keys[i])
		sj := hostKeySpecificity(keys[j])
		if si != sj {
			return si < sj
		}
		return keys[i] < keys[j]
	})
	return keys
}

// CurrentHost returns the active host identifier. Reads
// $GALE_HOST first; falls back to os.Hostname(). Returns "" if
// both fail.
func CurrentHost() string {
	if h := os.Getenv("GALE_HOST"); h != "" {
		return h
	}
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
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

// loadOrInit reads gale.toml at path and parses it. If the
// file does not exist, returns an empty config (used by
// AddPackage to bootstrap).
func loadOrInit(path string) (*GaleConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &GaleConfig{}, nil
		}
		return nil, fmt.Errorf("reading gale config: %w", err)
	}
	return ParseGaleConfig(string(data))
}

// hostPackages returns the packages map for cfg.Hosts[host],
// creating the host entry if needed.
func hostPackages(cfg *GaleConfig, host string) map[string]string {
	if cfg.Hosts == nil {
		cfg.Hosts = make(map[string]HostConfig)
	}
	h := cfg.Hosts[host]
	if h.Packages == nil {
		h.Packages = make(map[string]string)
	}
	cfg.Hosts[host] = h
	return cfg.Hosts[host].Packages
}

// hostPinned returns the pinned map for cfg.Hosts[host],
// creating the host entry if needed.
func hostPinned(cfg *GaleConfig, host string) map[string]bool {
	if cfg.Hosts == nil {
		cfg.Hosts = make(map[string]HostConfig)
	}
	h := cfg.Hosts[host]
	if h.Pinned == nil {
		h.Pinned = make(map[string]bool)
	}
	cfg.Hosts[host] = h
	return cfg.Hosts[host].Pinned
}

// UpsertPackage updates a package in gale.toml, preserving its
// existing location. If the package is present in the current
// host's section, it is updated there; otherwise it is written
// to the shared [packages] section. Used by install/update flows
// that should not move a host-scoped package to the shared
// section. host may be empty (no preservation; equivalent to
// AddPackage(path, "", ...)).
func UpsertPackage(path, host, name, version string) error {
	return withFileLock(path, func() error {
		cfg, err := loadOrInit(path)
		if err != nil {
			return err
		}

		if host != "" {
			if h, ok := cfg.Hosts[host]; ok {
				if _, here := h.Packages[name]; here {
					hostPackages(cfg, host)[name] = version
					return WriteGaleConfig(path, cfg)
				}
			}
		}

		if cfg.Packages == nil {
			cfg.Packages = make(map[string]string)
		}
		cfg.Packages[name] = version
		return WriteGaleConfig(path, cfg)
	})
}

// AddPackage adds or updates a package in the gale.toml at path.
// When host is empty, the package is written to the shared
// [packages] section. When non-empty, it is written under
// [hosts.<host>.packages]. If the file does not exist, it is
// bootstrapped.
func AddPackage(path, host, name, version string) error {
	return withFileLock(path, func() error {
		cfg, err := loadOrInit(path)
		if err != nil {
			return err
		}

		if host == "" {
			if cfg.Packages == nil {
				cfg.Packages = make(map[string]string)
			}
			cfg.Packages[name] = version
		} else {
			hostPackages(cfg, host)[name] = version
		}

		return WriteGaleConfig(path, cfg)
	})
}

// RemovePackage removes a package from the gale.toml at path.
// When host is empty, removes from shared [packages]; otherwise
// from [hosts.<host>.packages]. Returns ErrPackageNotFound if
// the package is not present in the targeted section.
func RemovePackage(path, host, name string) error {
	return withFileLock(path, func() error {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading gale config: %w", err)
		}
		cfg, err := ParseGaleConfig(string(data))
		if err != nil {
			return err
		}

		if host == "" {
			if _, exists := cfg.Packages[name]; !exists {
				return ErrPackageNotFound
			}
			delete(cfg.Packages, name)
		} else {
			h, ok := cfg.Hosts[host]
			if !ok {
				return ErrPackageNotFound
			}
			if _, exists := h.Packages[name]; !exists {
				return ErrPackageNotFound
			}
			delete(h.Packages, name)
			cfg.Hosts[host] = h
		}

		return WriteGaleConfig(path, cfg)
	})
}

// PinPackage marks a package as pinned in the gale.toml at path.
// When host is empty, the pin is recorded in shared [pinned] and
// the package must exist in shared [packages]. Otherwise the pin
// is recorded under [hosts.<host>.pinned] and the package must
// exist in that host's package list. Returns ErrPackageNotFound
// when the package is not in the targeted section.
func PinPackage(path, host, name string) error {
	return withFileLock(path, func() error {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading gale config: %w", err)
		}
		cfg, err := ParseGaleConfig(string(data))
		if err != nil {
			return err
		}

		if host == "" {
			if _, ok := cfg.Packages[name]; !ok {
				return ErrPackageNotFound
			}
			if cfg.Pinned == nil {
				cfg.Pinned = make(map[string]bool)
			}
			cfg.Pinned[name] = true
		} else {
			h, ok := cfg.Hosts[host]
			if !ok {
				return ErrPackageNotFound
			}
			if _, ok := h.Packages[name]; !ok {
				return ErrPackageNotFound
			}
			hostPinned(cfg, host)[name] = true
		}

		return WriteGaleConfig(path, cfg)
	})
}

// UnpinPackage removes a pin from the gale.toml at path.
// When host is empty, removes from shared [pinned]; otherwise
// from [hosts.<host>.pinned]. Missing pins are a no-op.
func UnpinPackage(path, host, name string) error {
	return withFileLock(path, func() error {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading gale config: %w", err)
		}
		cfg, err := ParseGaleConfig(string(data))
		if err != nil {
			return err
		}

		if host == "" {
			delete(cfg.Pinned, name)
		} else if h, ok := cfg.Hosts[host]; ok {
			delete(h.Pinned, name)
			cfg.Hosts[host] = h
		}

		return WriteGaleConfig(path, cfg)
	})
}
