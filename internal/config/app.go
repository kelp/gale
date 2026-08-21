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
	Priority int    `toml:"priority"`
}

// AppConfig represents ~/.gale/config.toml (app-level settings).
//
// Frozen: these three tables are the key set. Add no keys.
// [registry] url and [sync] parallelism are ignored leftovers;
// they must not repoint resolution or change the limiter.
// Retention is the compiled DefaultGenerationKeep.
type AppConfig struct {
	Repos     []Repo      `toml:"repos"`
	Build     BuildConfig `toml:"build"`
	Anthropic AIConfig    `toml:"anthropic"`
}

// DefaultParallelism is the compiled download/sync limiter
// size. config.toml and GALE_JOBS cannot change it.
const DefaultParallelism = 8

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
