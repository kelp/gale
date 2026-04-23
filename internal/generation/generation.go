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
// resolution without an import cycle: a bare version
// returns the highest "<v>-<N>" on disk, falls through
// to an exact match, and finally to a bare "<v>" (legacy
// pre-revision install). A "<v>-1" request also falls
// back to a bare "<v>" when the suffixed one is absent.
func resolveStoreDir(storeRoot, name, version string) string {
	if !strings.Contains(version, "-") {
		if rev, ok := highestRevisionOnDisk(storeRoot, name, version); ok {
			return filepath.Join(storeRoot, name, rev)
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

// highestRevisionOnDisk returns the directory name with the
// highest N among "<version>-<N>" siblings under
// <storeRoot>/<name>/. Skips .build-* staging dirs and
// non-directory entries (lock files).
func highestRevisionOnDisk(storeRoot, name, version string) (string, bool) {
	entries, err := os.ReadDir(filepath.Join(storeRoot, name))
	if err != nil {
		return "", false
	}
	prefix := version + "-"
	best := -1
	bestName := ""
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasPrefix(n, ".build-") {
			continue
		}
		if !strings.HasPrefix(n, prefix) {
			continue
		}
		rev, err := strconv.Atoi(n[len(prefix):])
		if err != nil || rev < 0 {
			continue
		}
		if rev > best {
			best = rev
			bestName = n
		}
	}
	if best < 0 {
		return "", false
	}
	return bestName, true
}

// ActiveStoreDirs resolves each (name, version) in pkgs to
// its on-disk store dir. Returned in an arbitrary order.
// Used by Build to populate the shared dylib farm, and by
// `gale doctor` to check farm drift against the same set.
func ActiveStoreDirs(pkgs map[string]string, storeRoot string) []string {
	active := make([]string, 0, len(pkgs))
	for name, version := range pkgs {
		active = append(active, resolveStoreDir(storeRoot, name, version))
	}
	return active
}

//go:embed gale-readme.md
var galeReadme []byte

// Build creates a new generation from the package map
// and atomically swaps the current symlink. Previous
// generations are retained for history and rollback.
func Build(pkgs map[string]string, galeDir, storeRoot string) error {
	return build(pkgs, galeDir, storeRoot, false)
}

// BuildLenient is Build but silently skips packages whose
// store dir is missing. Used by sync, whose Issue #20
// contract is to keep partial progress usable when some
// installs in a batch failed — the failure propagates via
// a separate error path, so the generation still needs to
// reflect what's actually on disk.
func BuildLenient(pkgs map[string]string, galeDir, storeRoot string) error {
	return build(pkgs, galeDir, storeRoot, true)
}

func build(pkgs map[string]string, galeDir, storeRoot string, lenient bool) error {
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

		if err := populateGeneration(genDir, pkgs, storeRoot, lenient); err != nil {
			cleanup()
			return err
		}

		// H5: validate every symlink in the new generation
		// resolves to something that exists before we commit
		// the swap. populateGeneration's per-package checks
		// guard the declared-but-missing case; this walk
		// catches races (store dir removed between populate
		// and rename) and malformed store contents — any
		// dangling link in the new gen means the swap would
		// activate a broken PATH entry.
		if err := validateGenerationSymlinks(genDir); err != nil {
			cleanup()
			return err
		}

		// Atomic swap: create a temporary symlink then rename.
		if err := swapCurrentSymlink(galeDir, next); err != nil {
			cleanup()
			return err
		}

		// Rebuild the shared-lib farm from this
		// generation's packages. Older revisions may
		// still be in the store (awaiting `gale gc`),
		// but they aren't on PATH and must not leak into
		// the farm. Best-effort — a farm error does not
		// invalidate the generation swap.
		active := ActiveStoreDirs(pkgs, storeRoot)
		if err := farm.Rebuild(
			active, farm.Dir(galeDir)); err != nil {
			fmt.Fprintf(os.Stderr,
				"farm: rebuild after gen swap: %v\n", err)
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
func populateGeneration(genDir string, pkgs map[string]string, storeRoot string, lenient bool) error {
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
				// H5: strict (Build) callers come through
				// rebuildGeneration with gale.toml-sourced
				// packages, so a missing store dir is a real
				// bug: the user expects the package to be on
				// PATH. Fail loud — install/remove/update
				// never commit a broken generation, and the
				// user learns about the corruption
				// immediately rather than discovering it when
				// `<pkg>: command not found` fires later.
				//
				// Lenient (BuildLenient) callers — only
				// `gale sync` — deliberately tolerate this
				// case. A sync batch where one install fails
				// must still land the successful installs on
				// PATH (Issue #20); sync surfaces the install
				// error via a separate channel, so swallowing
				// the missing-store-dir here is intentional.
				if !lenient {
					return fmt.Errorf(
						"%s@%s is missing from the store (%s); "+
							"run `gale install %s` or `gale sync` to restore",
						name, version, pkgDir, name)
				}
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

// validateGenerationSymlinks walks genDir and returns an
// error if any symlink target doesn't resolve. Defense in
// depth for Build/BuildLenient: catches store mutations
// racing with the generation rebuild and ensures the swap
// never activates a generation with broken PATH entries.
// Reads per-file stat, not a full SHA verify — we only
// care that the target exists on disk.
func validateGenerationSymlinks(genDir string) error {
	return filepath.Walk(genDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return fmt.Errorf("walk generation %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return nil
		}
		// os.Stat follows symlinks; a missing target
		// surfaces as ENOENT here.
		if _, statErr := os.Stat(path); statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				target, _ := os.Readlink(path)
				return fmt.Errorf(
					"generation has dangling symlink %s -> %s; "+
						"store mutated during rebuild",
					path, target)
			}
			return fmt.Errorf("stat %s: %w", path, statErr)
		}
		return nil
	})
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
// in srcDir. Recursively handles subdirectories (e.g.,
// man/man1/). srcDir must exist: callers reach this via
// populateGeneration, which ReadDir's the parent pkgDir
// and only invokes symlinkDir for entries that were
// directory children of a dir we just listed. A
// not-found here indicates a race (another process
// mutated the store during the rebuild) rather than a
// legitimate "package doesn't have this dir" state, so
// the error propagates instead of being swallowed.
func symlinkDir(srcDir, dstDir string) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
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
