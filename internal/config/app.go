package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/BurntSushi/toml"

	"github.com/kelp/gale/internal/atomicfile"
)

// Repo represents a recipe repository entry in config.toml.
type Repo struct {
	Name     string `toml:"name"`
	URL      string `toml:"url"`
	Priority int    `toml:"priority"`
}

// AppConfig represents ~/.gale/config.toml (app-level settings).
type AppConfig struct {
	Repos     []Repo         `toml:"repos"`
	Build     BuildConfig    `toml:"build"`
	Anthropic AIConfig       `toml:"anthropic"`
	Registry  RegistryConfig `toml:"registry"`
	Sync      SyncConfig     `toml:"sync"`
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
// Precedence: GALE_JOBS env var, then [sync] parallelism, then
// DefaultParallelism. The result is always >= 1.
func ResolveParallelism(cfg *AppConfig) int {
	if env := os.Getenv("GALE_JOBS"); env != "" {
		if n, err := strconv.Atoi(env); err == nil && n >= 1 {
			return n
		}
	}
	if cfg != nil && cfg.Sync.Parallelism >= 1 {
		return cfg.Sync.Parallelism
	}
	return DefaultParallelism
}

// BuildConfig holds build-related settings.
type BuildConfig struct {
	Debug bool `toml:"debug,omitempty"`
}

// DefaultGenerationKeep is the compiled retention: current
// plus one previous generation. config.toml cannot change
// or disable it.
const DefaultGenerationKeep = 2

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
