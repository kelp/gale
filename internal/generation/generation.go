package generation

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/filelock"
)

// resolveStoreDir returns the actual store dir for a
// (name, version) pair. Mirrors Store.resolveVersion's
// bidirectional resolution without an import cycle: a
// bare version prefers the canonical "<v>-1" dir, and a
// "<v>-1" request falls back to a bare dir when the
// suffixed one is absent.
func resolveStoreDir(storeRoot, name, version string) string {
	if !strings.Contains(version, "-") {
		canonical := version + "-1"
		canonicalDir := filepath.Join(storeRoot, name, canonical)
		if _, err := os.Stat(canonicalDir); err == nil {
			return canonicalDir
		}
	}
	dir := filepath.Join(storeRoot, name, version)
	if _, err := os.Stat(dir); err == nil {
		return dir
	}
	if strings.HasSuffix(version, "-1") {
		bare := strings.TrimSuffix(version, "-1")
		bareDir := filepath.Join(storeRoot, name, bare)
		if _, err := os.Stat(bareDir); err == nil {
			return bareDir
		}
	}
	return dir
}

//go:embed gale-readme.md
var galeReadme []byte

// Build creates a new generation from the package map
// and atomically swaps the current symlink. Previous
// generations are retained for history and rollback.
func Build(pkgs map[string]string, galeDir, storeRoot string) error {
	return filelock.With(generationLockPath(galeDir), func() error {
		prev, err := Current(galeDir)
		if err != nil {
			return fmt.Errorf("read current generation: %w", err)
		}

		next := prev + 1

		genDir := filepath.Join(
			galeDir, "gen", strconv.Itoa(next))

		// Always create bin/ — it's the minimum required
		// directory (user adds it to PATH).
		if err := os.MkdirAll(
			filepath.Join(genDir, "bin"), 0o755); err != nil {
			return fmt.Errorf("create generation dir: %w", err)
		}

		// Clean up the new generation directory on any
		// subsequent error so we don't leave orphaned dirs.
		cleanup := func() { os.RemoveAll(genDir) }

		if err := populateGeneration(genDir, pkgs, storeRoot); err != nil {
			cleanup()
			return err
		}

		// Atomic swap: create a temporary symlink then rename.
		if err := swapCurrentSymlink(galeDir, next); err != nil {
			cleanup()
			return err
		}

		// Rebuild the shared-lib farm to reflect the
		// packages in this generation. Rollbacks and
		// removals can drop packages whose dylibs were
		// farmed; repopulating from the store keeps the
		// farm honest. Best-effort — a farm error does
		// not invalidate the generation swap.
		if err := farm.Repopulate(
			storeRoot, farm.Dir(galeDir)); err != nil {
			fmt.Fprintf(os.Stderr,
				"farm: repopulate after gen swap: %v\n", err)
		}

		// Write README (best effort, world-readable).
		_ = os.WriteFile(
			filepath.Join(galeDir, "README.md"),
			galeReadme, 0o644)

		return nil
	})
}

func generationLockPath(galeDir string) string {
	return filepath.Join(galeDir, "generation.lock")
}

// populateGeneration symlinks each package's store
// contents into genDir. Packages are sorted
// alphabetically so the first package wins on
// filename conflicts.
func populateGeneration(genDir string, pkgs map[string]string, storeRoot string) error {
	names := make([]string, 0, len(pkgs))
	for name := range pkgs {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		version := pkgs[name]
		pkgDir := resolveStoreDir(storeRoot, name, version)
		entries, err := os.ReadDir(pkgDir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("read store %s: %w", name, err)
		}
		for _, e := range entries {
			if e.IsDir() {
				srcDir := filepath.Join(pkgDir, e.Name())
				dstDir := filepath.Join(genDir, e.Name())
				if err := os.MkdirAll(dstDir, 0o755); err != nil {
					return fmt.Errorf(
						"create gen %s dir: %w", e.Name(), err)
				}
				if err := symlinkDir(srcDir, dstDir); err != nil {
					return fmt.Errorf(
						"symlink %s/%s: %w", name, e.Name(), err)
				}
				continue
			}

			// Symlink root-level files (e.g., go.env).
			// Skip if already present from another package.
			src := filepath.Join(pkgDir, e.Name())
			dst := filepath.Join(genDir, e.Name())
			if _, err := os.Lstat(dst); err == nil {
				continue
			}
			if err := os.Symlink(src, dst); err != nil {
				return fmt.Errorf(
					"symlink %s/%s: %w", name, e.Name(), err)
			}
		}
	}
	return nil
}

// Current returns the active generation number by
// resolving the current symlink. Returns 0 if no
// current generation exists.
func Current(galeDir string) (int, error) {
	currentPath := filepath.Join(galeDir, "current")
	target, err := os.Readlink(currentPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("read current symlink: %w", err)
	}

	numStr := filepath.Base(target)
	n, err := strconv.Atoi(numStr)
	if err != nil {
		return 0, fmt.Errorf(
			"parse generation number %q: %w", numStr, err)
	}
	return n, nil
}

// Next returns the next generation number (Current+1,
// or 1 if none exists).
func Next(galeDir string) (int, error) {
	cur, err := Current(galeDir)
	if err != nil {
		return 0, err
	}
	return cur + 1, nil
}

// swapCurrentSymlink atomically points the current symlink
// at the given generation number. Uses a PID-scoped temp
// name to avoid races with concurrent processes.
func swapCurrentSymlink(galeDir string, genNum int) error {
	relTarget := filepath.Join("gen", strconv.Itoa(genNum))
	currentPath := filepath.Join(galeDir, "current")
	tmpLink := filepath.Join(galeDir,
		fmt.Sprintf("current-new.%d", os.Getpid()))
	if err := os.Remove(tmpLink); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale temp link: %w", err)
	}
	if err := os.Symlink(relTarget, tmpLink); err != nil {
		return fmt.Errorf("create temp current symlink: %w", err)
	}
	if err := os.Rename(tmpLink, currentPath); err != nil {
		os.Remove(tmpLink)
		return fmt.Errorf("atomic swap current symlink: %w", err)
	}
	return nil
}

// symlinkDir creates symlinks in dstDir for every file
// in srcDir. Skips if srcDir doesn't exist. Recursively
// handles subdirectories (e.g., man/man1/).
func symlinkDir(srcDir, dstDir string) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // package doesn't have this dir
		}
		return err
	}

	for _, entry := range entries {
		src := filepath.Join(srcDir, entry.Name())
		dst := filepath.Join(dstDir, entry.Name())

		if entry.IsDir() {
			// Recurse into subdirectories (e.g., man/man1/).
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
			if err := symlinkDir(src, dst); err != nil {
				return err
			}
			continue
		}

		// Skip if a symlink already exists (another
		// package provides the same file).
		if _, err := os.Lstat(dst); err == nil {
			continue
		}

		if err := os.Symlink(src, dst); err != nil {
			return err
		}
	}
	return nil
}
