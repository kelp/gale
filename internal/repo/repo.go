package repo

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// RepoConfig represents a configured recipe repository.
type RepoConfig struct {
	Name     string
	URL      string
	Key      string
	Priority int
	CacheDir string // local path where repo is cloned
}

// SearchResult represents a found recipe.
type SearchResult struct {
	RepoName string
	Package  string
	FilePath string
	Priority int
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

// listRecipes returns all .toml files in a repo's recipes/ directory.
func listRecipes(repo RepoConfig) ([]SearchResult, error) {
	recipesDir := filepath.Join(repo.CacheDir, "recipes")
	entries, err := os.ReadDir(recipesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading recipes for %s: %w",
			repo.Name, err)
	}

	var results []SearchResult
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".toml") {
			continue
		}
		pkg := strings.TrimSuffix(name, ".toml")
		results = append(results, SearchResult{
			RepoName: repo.Name,
			Package:  pkg,
			FilePath: filepath.Join(recipesDir, name),
			Priority: repo.Priority,
		})
	}
	return results, nil
}

// Search finds recipes matching a package name across all repos.
func (m *Manager) Search(query string) ([]SearchResult, error) {
	var results []SearchResult
	for _, repo := range m.Repos {
		recipes, err := listRecipes(repo)
		if err != nil {
			return nil, err
		}
		for _, r := range recipes {
			if strings.Contains(r.Package, query) {
				results = append(results, r)
			}
		}
	}
	return results, nil
}

// Resolve finds the recipe for a package using priority order.
// Returns nil when the package is not found.
func (m *Manager) Resolve(name string) (*SearchResult, error) {
	var best *SearchResult
	for _, repo := range m.Repos {
		recipes, err := listRecipes(repo)
		if err != nil {
			return nil, err
		}
		for _, r := range recipes {
			if r.Package == name {
				if best == nil || r.Priority < best.Priority {
					match := r
					best = &match
				}
				break
			}
		}
	}
	return best, nil
}

// ListAll lists all available recipes across repos.
func (m *Manager) ListAll() ([]SearchResult, error) {
	var results []SearchResult
	for _, repo := range m.Repos {
		recipes, err := listRecipes(repo)
		if err != nil {
			return nil, err
		}
		results = append(results, recipes...)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Package < results[j].Package
	})
	return results, nil
}
