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
//
// "Bare" means no trailing "-<digits>" revision suffix —
// a dash from a semver pre-release/dev tag like
// "0.16.2-dev.70+676b646" still counts as bare, because
// the revision goes on the END (e.g. "...-1").
func resolveStoreDir(storeRoot, name, version string) string {
	if !hasNumericRevisionSuffix(version) {
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

// hasNumericRevisionSuffix reports whether version ends in a
// "-<digits>" revision suffix. Distinguishes a bare semver-with-
// dev-tag like "0.16.2-dev.70+676b646" (no revision) from an
// explicit pinned form like "0.16.2-dev.70+676b646-1" (revision 1).
// Used by resolveStoreDir and highestRevisionOnDisk to decide
// whether to scan for the highest revision on disk (bare → scan)
// or treat the version as an exact-match request (numeric suffix
// → exact). Mirrors cmd/gale/context.go's stripNumericRevision
// classification.
func hasNumericRevisionSuffix(version string) bool {
	i := strings.LastIndex(version, "-")
	if i < 0 || i == len(version)-1 {
		return false
	}
	for _, r := range version[i+1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// highestRevisionOnDisk returns the directory name with the
// highest N among "<version>-<N>" siblings under
// <storeRoot>/<name>/. Skips .build-* staging dirs,
// "<version>.bak" backups from in-progress reinstalls, and
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
		if strings.HasPrefix(n, ".build-") || strings.HasSuffix(n, ".bak") {
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

// carryForwardMissingVersions returns a copy of pkgs where
// any (name, version) whose store dir is absent has its
// version replaced with the version that was active in the
// previous generation, when that store dir is still on disk.
// Used by lenient build so a sync that can't install a newly-
// pinned version doesn't silently drop the previously-working
// install from PATH.
func carryForwardMissingVersions(
	pkgs map[string]string, storeRoot, galeDir string, prevGen int,
) map[string]string {
	prevGenDir := filepath.Join(galeDir, "gen", strconv.Itoa(prevGen))
	prev := previousGenVersions(prevGenDir, storeRoot)
	if len(prev) == 0 {
		return pkgs
	}

	out := make(map[string]string, len(pkgs))
	for name, version := range pkgs {
		out[name] = version
		if _, err := os.Stat(resolveStoreDir(storeRoot, name, version)); err == nil {
			continue
		}
		prevVer, ok := prev[name]
		if !ok || prevVer == version {
			continue
		}
		if _, err := os.Stat(resolveStoreDir(storeRoot, name, prevVer)); err != nil {
			continue
		}
		fmt.Fprintf(os.Stderr,
			"generation: %s@%s not installed; "+
				"carrying %s@%s forward from gen/%d\n",
			name, version, name, prevVer, prevGen)
		out[name] = prevVer
	}
	return out
}

// previousGenVersions returns a name → version map by reading
// the symlinks under prevGenDir. Each symlink in a generation
// points at <storeRoot>/<name>/<version>/...; the first two
// path components after storeRoot give the (name, version)
// pair. Unparseable links are skipped — best-effort, since the
// caller only uses this as a hint for carry-forward.
func previousGenVersions(prevGenDir, storeRoot string) map[string]string {
	out := map[string]string{}
	//nolint:errcheck // best-effort walk; per-entry errors below are intentionally swallowed
	filepath.Walk(prevGenDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			return nil //nolint:nilerr // skip unreadable entries, keep walking
		}
		target, readErr := os.Readlink(path)
		if readErr != nil {
			return nil //nolint:nilerr // skip unreadable link, keep walking
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		rel, relErr := filepath.Rel(storeRoot, target)
		if relErr != nil || strings.HasPrefix(rel, "..") {
			return nil //nolint:nilerr // target outside store; not ours
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) < 2 {
			return nil
		}
		name, version := parts[0], parts[1]
		if _, seen := out[name]; !seen {
			out[name] = version
		}
		return nil
	})
	return out
}

// CurrentVersions returns the package name → version map of
// the active generation by reading its symlinks. Returns an
// empty map (no error) when no generation is active yet.
// Used by sync to detect whether gale.toml has drifted from
// the active generation — drift means the rebuild must run
// even if no installs happened, so removed packages drop off
// PATH.
func CurrentVersions(galeDir, storeRoot string) (map[string]string, error) {
	cur, err := Current(galeDir)
	if err != nil {
		return nil, err
	}
	if cur == 0 {
		return map[string]string{}, nil
	}
	prevGenDir := filepath.Join(galeDir, "gen", strconv.Itoa(cur))
	return previousGenVersions(prevGenDir, storeRoot), nil
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

// ActiveVersions resolves each (name, version) in pkgs to the
// store-dir basename ("<version>-<revision>", or a bare
// "<version>" for legacy pre-revision installs) that a fresh
// Build would link against. Used by `gale doctor` to compare
// against CurrentVersions — which reads the active gen's
// actual symlink targets — and surface revision drift when
// the gen carries a stale link to an older revision.
func ActiveVersions(pkgs map[string]string, storeRoot string) map[string]string {
	out := make(map[string]string, len(pkgs))
	for name, version := range pkgs {
		out[name] = filepath.Base(resolveStoreDir(storeRoot, name, version))
	}
	return out
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
	// Use the store-rooted lock path so project-scoped and global
	// Build calls contend on the same lock file as the installer.
	// filepath.Dir(storeRoot) is always the global galeDir
	// (~/.gale/ at global scope, same at project scope since the
	// store is shared). This closes the residual install-vs-project-
	// sync race described in installer.go:storeGenLockPath.
	lockPath := filepath.Join(filepath.Dir(storeRoot), "generation.lock")
	return filelock.With(lockPath, func() error {
		prev, err := Current(galeDir)
		if err != nil {
			return fmt.Errorf("read current generation: %w", err)
		}

		// Lenient builds carry forward any package whose
		// pinned store dir is missing — keeps a working
		// version on PATH when gale.toml pins something
		// that hasn't been installed yet. Strict Build
		// errors on that case instead.
		if lenient && prev > 0 {
			pkgs = carryForwardMissingVersions(
				pkgs, storeRoot, galeDir, prev,
			)
		}

		next := prev + 1

		genDir := filepath.Join(
			galeDir, "gen", strconv.Itoa(next),
		)

		// Tear down any pre-existing gen dir at this number
		// before populating. Without this, symlinkDir's
		// skip-if-dst-exists logic merges new content into the
		// stale layout, shipping a gen with the wrong store
		// revisions or with leftover symlinks for packages no
		// longer in pkgs. validateGenerationSymlinks doesn't
		// catch this — stale links still resolve, just to the
		// wrong place. Reached when a prior Build's cleanup
		// didn't fire (process killed) or when current was
		// rolled back behind the highest-built gen.
		if err := os.RemoveAll(genDir); err != nil {
			return fmt.Errorf("clean stale generation dir: %w", err)
		}

		// Always create bin/ — it's the minimum required
		// directory (user adds it to PATH).
		if err := os.MkdirAll(
			filepath.Join(genDir, "bin"), 0o755,
		); err != nil {
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
			active, farm.Dir(galeDir),
		); err != nil {
			fmt.Fprintf(os.Stderr,
				"farm: rebuild after gen swap: %v\n", err)
		}

		// Write README (best effort, world-readable).
		_ = os.WriteFile(
			filepath.Join(galeDir, "README.md"),
			galeReadme, 0o644,
		)

		return nil
	})
}

// skipTopLevelDirs lists store-dir subdirectories that
// populateGeneration must NOT mirror into the generation.
// Nothing on PATH or in any dynamic-linker / man / locale
// path reads through gen/<N>/<dir>/ for these — packages
// still ship them in the store, and tools that need them
// (e.g. Go reading $GOROOT/src) resolve to the store path
// via the binary's actual location, not the gen symlink.
//
// Mirroring these was always dead weight; for Go's stdlib
// it accounted for ~45% of a gen's inode count.
var skipTopLevelDirs = map[string]bool{
	"src":  true,
	"api":  true,
	"pkg":  true,
	"doc":  true,
	"misc": true,
}

// PruneOldGenerations removes generation directories older than
// (curGen - keep + 1), preserving the most recent `keep` gens
// (including the current one). Anything at or above curGen —
// including any in-flight gen/curGen+1 a concurrent Build may
// have created — is preserved. Holds the store-rooted gen lock
// for its critical section so it serializes with Build.
//
// Returns the removed gen numbers in ascending order so the
// caller can report them. keep<=0 or no current symlink is a
// no-op (returns nil).
//
// Intended as an auto-gc hook after Build: callers pass the
// user-configured retention (default 10) so per-install gen
// accumulation can't drown the filesystem in inodes (the dev-
// host incident with ~3M gen/ inodes across 33 untouched gens).
func PruneOldGenerations(galeDir, storeRoot string, keep int) ([]int, error) {
	if keep <= 0 {
		return nil, nil
	}
	lockPath := filepath.Join(filepath.Dir(storeRoot), "generation.lock")
	var removed []int
	err := filelock.With(lockPath, func() error {
		curGen, err := Current(galeDir)
		if err != nil {
			return fmt.Errorf("read current: %w", err)
		}
		if curGen == 0 {
			return nil
		}
		cutoff := curGen - keep + 1
		if cutoff <= 1 {
			return nil
		}
		genRoot := filepath.Join(galeDir, "gen")
		entries, err := os.ReadDir(genRoot)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("read gen dir: %w", err)
		}
		var doomed []int
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			n, err := strconv.Atoi(e.Name())
			if err != nil {
				continue
			}
			if n < cutoff {
				doomed = append(doomed, n)
			}
		}
		sort.Ints(doomed)
		for _, n := range doomed {
			if err := os.RemoveAll(
				filepath.Join(genRoot, strconv.Itoa(n))); err != nil {
				return fmt.Errorf(
					"remove gen %d: %w", n, err)
			}
			removed = append(removed, n)
		}
		return nil
	})
	return removed, err
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
						name, version, pkgDir, name,
					)
				}
				continue
			}
			return fmt.Errorf("read store %s: %w", name, err)
		}
		for _, e := range entries {
			if e.IsDir() {
				if skipTopLevelDirs[e.Name()] {
					continue
				}
				srcDir := filepath.Join(pkgDir, e.Name())
				dstDir := filepath.Join(genDir, e.Name())
				if err := os.MkdirAll(dstDir, 0o755); err != nil {
					return fmt.Errorf(
						"create gen %s dir: %w", e.Name(), err,
					)
				}
				if err := symlinkDir(srcDir, dstDir); err != nil {
					return fmt.Errorf(
						"symlink %s/%s: %w", name, e.Name(), err,
					)
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
					"symlink %s/%s: %w", name, e.Name(), err,
				)
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
					path, target,
				)
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
			"parse generation number %q: %w", numStr, err,
		)
	}
	return n, nil
}

// Resolve is Current plus a target-existence check. It
// returns (gen, relTarget, nil) when the current symlink
// points at an extant gen directory, (0, "", nil) when no
// current symlink exists yet, and a descriptive error when
// the symlink dangles (target gen directory absent) or its
// name doesn't parse. `gale doctor` uses this to flag a
// corrupted current pointer — the case where the active
// generation has been deleted out from under us by rm -rf,
// a partial gc, or a half-restored backup — which Current
// alone cannot detect because it only Readlinks the symlink.
func Resolve(galeDir string) (int, string, error) {
	currentPath := filepath.Join(galeDir, "current")
	target, err := os.Readlink(currentPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, "", nil
		}
		return 0, "", fmt.Errorf("read current symlink: %w", err)
	}

	numStr := filepath.Base(target)
	n, err := strconv.Atoi(numStr)
	if err != nil {
		return 0, target, fmt.Errorf(
			"parse generation number %q: %w", numStr, err,
		)
	}

	// Resolve relative targets against galeDir so Stat hits
	// the right path (current is created with a relative
	// target like "gen/4").
	absTarget := target
	if !filepath.IsAbs(absTarget) {
		absTarget = filepath.Join(galeDir, target)
	}
	if _, err := os.Stat(absTarget); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return n, target, fmt.Errorf(
				"current symlink points at %s but that "+
					"generation directory does not exist", target,
			)
		}
		return n, target, fmt.Errorf(
			"stat current target %s: %w", target, err,
		)
	}
	return n, target, nil
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
