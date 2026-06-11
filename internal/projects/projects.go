// Package projects maintains the machine-local project
// registry at <gale home>/projects: one absolute project path
// per line, appended as a side effect of normal project-scoped
// use (env activation, sync, install, ...).
//
// The registry exists for gc liveness (gh#115): without it, gc
// can only see the global generation and the generation of the
// project it happens to run from, so it sweeps store versions
// that OTHER projects' active generations still link. gc unions
// retention across every registered project instead.
//
// Concurrency model is deliberately simple: O_APPEND writes
// (atomic for short lines) plus dedup-on-read. Concurrent
// registers can at worst duplicate a line, which List collapses.
package projects

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kelp/gale/internal/atomicfile"
)

// registryPath returns the registry file under the gale home
// (~/.gale/projects for the default layout).
func registryPath(galeHome string) string {
	return filepath.Join(galeHome, "projects")
}

// canonical normalizes a project path for stable comparisons:
// absolute, symlinks resolved (macOS /var vs /private/var),
// cleaned. Resolution is best-effort; an unresolvable path is
// kept as given.
func canonical(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}

// Register records projectPath in the registry if absent.
// Creates the gale home and registry file as needed. Callers
// on command hot paths should treat failures as best-effort —
// a read-only gale home must never block install or sync.
func Register(galeHome, projectPath string) error {
	path := canonical(projectPath)
	existing, err := List(galeHome)
	if err != nil {
		return err
	}
	for _, p := range existing {
		if p == path {
			return nil
		}
	}
	if err := os.MkdirAll(galeHome, 0o755); err != nil {
		return fmt.Errorf("creating gale home: %w", err)
	}
	f, err := os.OpenFile(
		registryPath(galeHome),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY,
		0o644, //nolint:gosec // world-readable registry, like gale.toml
	)
	if err != nil {
		return fmt.Errorf("opening project registry: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(path + "\n"); err != nil {
		return fmt.Errorf("appending to project registry: %w", err)
	}
	return nil
}

// List returns the registered project paths, in file order,
// with blank lines and duplicates dropped. A missing registry
// is an empty list, not an error (fresh install).
func List(galeHome string) ([]string, error) {
	data, err := os.ReadFile(registryPath(galeHome))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading project registry: %w", err)
	}
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		p := strings.TrimSpace(line)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out, nil
}

// Prune rewrites the registry keeping only live projects: a
// path whose gale.toml (or .tool-versions — gale's config
// fallback) still exists. A vanished project needs no gc
// retention, so gc calls this before computing liveness.
func Prune(galeHome string) error {
	all, err := List(galeHome)
	if err != nil || len(all) == 0 {
		return err
	}
	keep := make([]string, 0, len(all))
	for _, p := range all {
		if Lives(p) {
			keep = append(keep, p)
		}
	}
	if len(keep) == len(all) {
		return nil
	}
	content := strings.Join(keep, "\n")
	if content != "" {
		content += "\n"
	}
	if err := atomicfile.Write(
		registryPath(galeHome), []byte(content),
	); err != nil {
		return fmt.Errorf("rewriting project registry: %w", err)
	}
	return nil
}

// Lives reports whether path still looks like a gale project:
// a gale.toml, or the .tool-versions fallback gale's config
// loading honors. Prune keeps live paths; gc retains only
// live projects' generations.
func Lives(path string) bool {
	for _, name := range []string{"gale.toml", ".tool-versions"} {
		if _, err := os.Stat(filepath.Join(path, name)); err == nil {
			return true
		}
	}
	return false
}
