package repo

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// RepoConfig represents a configured recipe repository.
type RepoConfig struct {
	Name     string
	URL      string
	CacheDir string // local path where repo is cloned
}

// Manager manages recipe repositories.
type Manager struct {
	CacheRoot string
	Repos     []RepoConfig
}

// NewManager creates a Manager with the given cache root.
func NewManager(cacheRoot string) *Manager {
	return &Manager{CacheRoot: cacheRoot}
}

// AddRepo adds a repo configuration.
func (m *Manager) AddRepo(cfg RepoConfig) {
	if cfg.CacheDir == "" {
		cfg.CacheDir = filepath.Join(m.CacheRoot, cfg.Name)
	}
	m.Repos = append(m.Repos, cfg)
}

// findRepo returns the repo config for the given name, or an error.
func (m *Manager) findRepo(name string) (*RepoConfig, error) {
	for i := range m.Repos {
		if m.Repos[i].Name == name {
			return &m.Repos[i], nil
		}
	}
	return nil, fmt.Errorf("repo %q not found", name)
}

// Clone clones a repo to the cache directory.
func (m *Manager) Clone(name string) error {
	repo, err := m.findRepo(name)
	if err != nil {
		return fmt.Errorf("clone: %w", err)
	}

	cmd := exec.Command("git", "clone", repo.URL, repo.CacheDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("clone %s: %s: %w",
			name, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// Fetch fetches updates for a cached repo.
func (m *Manager) Fetch(name string) error {
	repo, err := m.findRepo(name)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}

	cmd := exec.Command("git", "-C", repo.CacheDir, "pull")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("fetch %s: %s: %w",
			name, strings.TrimSpace(string(out)), err)
	}
	return nil
}
