package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"

	"github.com/kelp/gale/internal/atomicfile"
)

// Repo represents a recipe repository entry in config.toml.
type Repo struct {
	Name     string `toml:"name"`
	URL      string `toml:"url"`
	Key      string `toml:"key"`
	Priority int    `toml:"priority"`
}

// AppConfig represents ~/.gale/config.toml (app-level settings).
type AppConfig struct {
	Repos      []Repo           `toml:"repos"`
	Build      BuildConfig      `toml:"build"`
	Anthropic  AIConfig         `toml:"anthropic"`
	Registry   RegistryConfig   `toml:"registry"`
	Generation GenerationConfig `toml:"generation"`
	Sync       SyncConfig       `toml:"sync"`
}

// SyncConfig controls sync/download behavior. Parallelism is the
// number of concurrent downloads; default DefaultParallelism when
// unset or non-positive.
type SyncConfig struct {
	Parallelism int `toml:"parallelism,omitempty"`
}

// DefaultParallelism is the fallback download parallelism when
// nothing is configured via GALE_JOBS or [sync] parallelism.
const DefaultParallelism = 8

// ResolveParallelism returns the effective download parallelism.
func ResolveParallelism(cfg *AppConfig) int { return 0 } // stub — wrong on purpose

// BuildConfig holds build-related settings.
type BuildConfig struct {
	Debug bool `toml:"debug,omitempty"`
}

// GenerationConfig holds gen-retention settings. Keep is the
// number of recent generations (including the current one)
// preserved after each rebuild's auto-gc pass. Default 10 when
// unset or non-positive; set to a negative value to disable
// auto-gc entirely.
type GenerationConfig struct {
	Keep int `toml:"keep,omitempty"`
}

// DefaultGenerationKeep is the number of generations preserved
// when config.toml has no [generation] section. Sized to cover
// a typical week of installs while keeping inode usage bounded:
// at ~28K inodes per gen, 10 gens ≈ 280K inodes total, two
// orders of magnitude under typical ext4 default budgets.
const DefaultGenerationKeep = 10

// EffectiveGenerationKeep returns the configured Keep value
// when positive, otherwise DefaultGenerationKeep. A negative
// value (the "disabled" sentinel) is returned as-is so the
// caller can short-circuit auto-gc.
func (g GenerationConfig) EffectiveGenerationKeep() int {
	if g.Keep == 0 {
		return DefaultGenerationKeep
	}
	return g.Keep
}

// AIConfig holds Anthropic API settings.
type AIConfig struct {
	APIKey     string `toml:"api_key"`
	PromptFile string `toml:"prompt_file"`
}

// RegistryConfig holds registry settings.
type RegistryConfig struct {
	URL string `toml:"url"`
}

// ParseAppConfig parses a config.toml string.
func ParseAppConfig(data string) (*AppConfig, error) {
	var cfg AppConfig
	if _, err := toml.Decode(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing app config: %w", err)
	}
	return &cfg, nil
}

// WriteAppConfig writes an AppConfig to the given path atomically.
func WriteAppConfig(path string, cfg *AppConfig) error {
	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	if err := enc.Encode(cfg); err != nil {
		return fmt.Errorf("encoding app config: %w", err)
	}
	return atomicfile.Write(path, buf.Bytes())
}

// ErrRepoNotFound is returned when a repo to remove does
// not exist in the config.
var ErrRepoNotFound = errors.New("repo not found")

// AddRepo adds a repository to the config.toml at path.
// Creates the file if it does not exist.
func AddRepo(path string, repo Repo) error {
	return withFileLock(path, func() error {
		var cfg *AppConfig

		data, err := os.ReadFile(path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("reading app config: %w", err)
			}
			cfg = &AppConfig{}
		} else {
			cfg, err = ParseAppConfig(string(data))
			if err != nil {
				return err
			}
		}

		cfg.Repos = append(cfg.Repos, repo)
		return WriteAppConfig(path, cfg)
	})
}

// RemoveRepo removes a repository by name from config.toml.
func RemoveRepo(path string, name string) error {
	return withFileLock(path, func() error {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading app config: %w", err)
		}

		cfg, err := ParseAppConfig(string(data))
		if err != nil {
			return err
		}

		found := false
		filtered := cfg.Repos[:0]
		for _, r := range cfg.Repos {
			if r.Name == name {
				found = true
				continue
			}
			filtered = append(filtered, r)
		}

		if !found {
			return ErrRepoNotFound
		}

		cfg.Repos = filtered
		return WriteAppConfig(path, cfg)
	})
}
