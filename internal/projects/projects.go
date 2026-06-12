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
// Concurrency model: mutations serialize on
// <gale home>/projects.lock (flock), so a Register racing a
// Prune can never append to the inode Prune just replaced or be
// overwritten by the rewrite — a silently dropped entry would
// let a later gc sweep that project's store versions, the exact
// bug the registry exists to prevent (gh#115). The lock is held
// only around the file writes, never across liveness stats:
// Register runs on command hot paths (direnv's gale env, sync),
// so it dedup-checks lock-free and locks only to append, and
// Prune stats liveness before locking — a stat on a dead
// network mount must not wedge every concurrent gale command.
// Under the lock each re-reads the file, so decisions made on
// the lock-free read stay sound: Prune drops only entries dead
// at scan time (anything appended since survives until the next
// prune), and Register's lock-free dedup hit is safe because a
// project being registered exists, so Prune keeps it. List
// stays lock-free: Prune rewrites via atomic rename, so a plain
// read always sees a complete file.
package projects

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kelp/gale/internal/atomicfile"
	"github.com/kelp/gale/internal/filelock"
)

// pruneAfterScan, when non-nil, runs after Prune's lock-free
// liveness scan and before it acquires the lock. Test hook.
var pruneAfterScan func()

// registryPath returns the registry file under the gale home
// (~/.gale/projects for the default layout).
func registryPath(galeHome string) string {
	return filepath.Join(galeHome, "projects")
}

// lockPath returns the flock file that serializes registry
// mutations (Register, Prune). Lives next to the registry;
// filelock keeps it on disk after unlock.
func lockPath(galeHome string) string {
	return filepath.Join(galeHome, "projects.lock")
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
	// Lock-free fast path: already registered. Keeps the common
	// case (every direnv activation) off projects.lock — no
	// blocking behind a slow Prune, no lock-file create on a
	// read-only gale home. Safe vs a concurrent Prune: a project
	// being registered exists, so Prune keeps its entry.
	existing, err := List(galeHome)
	if err != nil {
		return err
	}
	for _, p := range existing {
		if p == path {
			return nil
		}
	}
	// Serialize the append with Prune: an unlocked append can
	// land on the inode Prune's rewrite just replaced and
	// silently vanish. Acquire also creates the gale home (lock
	// parent dir).
	unlock, err := filelock.Acquire(lockPath(galeHome))
	if err != nil {
		return fmt.Errorf("locking project registry: %w", err)
	}
	defer unlock()
	// Re-check under the lock: another Register may have
	// appended path since the lock-free read.
	existing, err = List(galeHome)
	if err != nil {
		return err
	}
	for _, p := range existing {
		if p == path {
			return nil
		}
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
	// Scan liveness OUTSIDE the lock: Lives() stats every
	// registered path, and a dead network mount can hang a stat
	// for minutes — holding projects.lock through that would
	// block every concurrent Register (direnv, sync, install).
	snapshot, err := List(galeHome)
	if err != nil || len(snapshot) == 0 {
		return err
	}
	dead := map[string]bool{}
	for _, p := range snapshot {
		if !Lives(p) {
			dead[p] = true
		}
	}
	if len(dead) == 0 {
		return nil
	}
	if pruneAfterScan != nil {
		pruneAfterScan()
	}
	// Serialize the rewrite with Register's append: it must not
	// overwrite an entry appended after the scan.
	unlock, err := filelock.Acquire(lockPath(galeHome))
	if err != nil {
		return fmt.Errorf("locking project registry: %w", err)
	}
	defer unlock()
	// Re-read under the lock and drop only entries dead at scan
	// time; anything appended since survives until the next
	// prune.
	all, err := List(galeHome)
	if err != nil {
		return err
	}
	keep := make([]string, 0, len(all))
	for _, p := range all {
		if !dead[p] {
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
