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

	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/filelock"
	"github.com/kelp/gale/internal/store"
)

// resolveStoreDir returns the actual store dir for a
// (name, version) pair by delegating to the canonical
// resolver in internal/store: a bare version returns the
// highest populated "<v>-<N>" on disk, falls through to an
// exact match, and finally to a bare "<v>" (legacy
// pre-revision install). A "<v>-1" request also falls back
// to a bare "<v>" when the suffixed one is absent. Empty
// in-flight revision dirs (created by a concurrent install,
// or left by a killed one) are skipped so they can't shadow
// the populated active revision (gh#76).
func resolveStoreDir(storeRoot, name, version string) string {
	return store.NewStore(storeRoot).ResolveDir(name, version)
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
	prev := genVersions(prevGenDir, storeRoot)
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

// genVersions returns a name → version map by reading symlinks
// under genDir. Each symlink in a generation points at
// <storeRoot>/<name>/<version>/...; the first two path
// components after storeRoot give the (name, version) pair.
// The full directory tree is walked (not just bin/) so
// lib-only packages (shared dylib deps with no bin/ entries)
// are included. First-seen-wins for packages with multiple
// symlinks in the gen (e.g. bin/ and lib/ both point into the
// same store dir).
//
// The (name, version) pair is read from the link TEXT, then the
// owning store dir <storeRoot>/<name>/<version> is stat'd to
// decide inclusion. This splits two cases the old leaf-resolving
// reader conflated:
//
//   - The store DIR is gone (the package was GC'd): skipped, so
//     List/Diff/CurrentVersions never report a phantom (the
//     dangling-symlink contract).
//   - The store dir exists but the leaf file is absent (an
//     incomplete install, or a bin symlink the package never
//     populated): retained, so gc (gh#115) keeps a version the
//     active generation still references rather than dropping it
//     over a missing leaf.
func genVersions(genDir, storeRoot string) map[string]string {
	// Resolve storeRoot through symlinks so relative path
	// computation works on macOS where /var → /private/var.
	absStore, err := filepath.EvalSymlinks(storeRoot)
	if err != nil {
		absStore = storeRoot
	}

	out := map[string]string{}
	//nolint:errcheck // best-effort walk; per-entry errors below are intentionally swallowed
	filepath.Walk(genDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			return nil //nolint:nilerr // skip unreadable entries, keep walking
		}
		// Read the link text rather than resolving it: the leaf
		// target may be absent while the store dir still exists.
		target, readErr := os.Readlink(path)
		if readErr != nil {
			return nil //nolint:nilerr // skip unreadable link, keep walking
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		// Match the target against both the symlink-resolved store
		// root (absStore, for macOS /var → /private/var) and the
		// raw storeRoot. The link text is unresolved, so depending
		// on which path the caller passed, either form may share a
		// prefix with the target.
		rel := relWithinStore(absStore, target)
		if rel == "" {
			rel = relWithinStore(storeRoot, target)
		}
		if rel == "" {
			return nil // target outside store; not ours
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) < 2 {
			return nil
		}
		name, version := parts[0], parts[1]
		// Skip when the owning store dir is gone (GC'd package):
		// a dangling link to a removed store dir must not surface.
		// Stat both store-root spellings — only one need exist.
		if !storeDirExists(absStore, name, version) &&
			!storeDirExists(storeRoot, name, version) {
			return nil
		}
		if _, seen := out[name]; !seen {
			out[name] = version
		}
		return nil
	})
	return out
}

// relWithinStore returns the path of target relative to root
// when target lies inside root, or "" otherwise. A target that
// resolves to root itself or escapes it (rel starts with "..")
// returns "".
func relWithinStore(root, target string) string {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	return rel
}

// storeDirExists reports whether <root>/<name>/<version> is an
// existing directory in the store.
func storeDirExists(root, name, version string) bool {
	info, err := os.Stat(filepath.Join(root, name, version))
	return err == nil && info.IsDir()
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
	return genVersions(prevGenDir, storeRoot), nil
}

// ActiveStoreDirs resolves each (name, version) in pkgs to
// its on-disk store dir. Returned in an arbitrary order.
// Seeds FarmStoreDirs, which Build and `gale doctor` use
// for the shared dylib farm.
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

// FarmStoreDirs returns the store dirs whose versioned dylibs
// belong in the shared farm: the resolved dir for every package
// in pkgs plus the transitive dep closure recorded in each
// dir's .gale-deps.toml. Runtime deps are farmed at install
// time but never appear in gale.toml, so rebuilding the farm
// from the config set alone deletes the entries dependents'
// rpaths resolve through (gh#43). Dep dirs missing from the
// store are skipped — the farm can only link what's on disk.
// Visited dirs are not re-expanded, so dep cycles terminate.
func FarmStoreDirs(pkgs map[string]string, storeRoot string) []string {
	dirs, _ := farmStoreDirs(pkgs, storeRoot, func(dir string, err error) error {
		// Best-effort: an unreadable metadata file must
		// not fail the whole farm rebuild.
		fmt.Fprintf(os.Stderr,
			"farm: read deps metadata in %s: %v\n", dir, err)
		return nil
	})
	return dirs
}

// FarmStoreDirsStrict is FarmStoreDirs for callers that must not
// act on a partial answer: an unreadable .gale-deps.toml stops the
// walk and returns the error instead of warning past it.
//
// The farm claimant walk needs this. FarmStoreDirs' leniency is
// correct for a rebuild, which should still repair every link it
// can read, and wrong for a claim: a claim that quietly omits a
// dep permits exactly the mutation it existed to refuse. It is the
// same split the provenance reader draws against depsmeta's
// leniency — tolerate a partial answer where a partial answer is
// still useful, never where a decision rests on it.
func FarmStoreDirsStrict(
	pkgs map[string]string, storeRoot string,
) ([]string, error) {
	return farmStoreDirs(pkgs, storeRoot, func(dir string, err error) error {
		// The remedy is part of the error because this one blocks
		// every scope's installs and removals until it clears, and
		// the obvious repairs do not work. Deleting just the
		// metadata file is worse than the error: depsmeta.Read
		// returns an empty closure for a missing file, so the walk
		// then succeeds with a silently smaller claim, which is the
		// fail-open this check exists to prevent. Reinstalling does
		// not rewrite it either — an install over an existing store
		// dir returns cached before it writes anything
		// (installer.go:153), and IsStale compares recorded deps
		// against resolved recipes without ever looking at whether a
		// dep's directory is there.
		//
		// Deleting the whole directory is what works: the walk skips
		// a dir that is not in the store, so the claim shrinks
		// honestly rather than silently.
		//
		// Sync only reinstalls it if the deletion reaches a declared
		// package. Sync evaluates declared roots alone, and an
		// install over an existing dir returns cached before it
		// installs any dep, so a cached ancestor anywhere on the
		// chain stops the repair from descending: for A -> B -> C,
		// deleting C and B leaves A cached and neither restored.
		// Every directory on the path has to go, the declared
		// package at the top of it included, which is what makes the
		// reinstall walk all the way down.
		return fmt.Errorf(
			"read deps metadata in %s: %w (repair: delete that whole "+
				"directory and every package directory on a dependency "+
				"path up to and including a declared package, then run "+
				"gale sync)",
			dir, err,
		)
	})
}

// farmStoreDirs is the shared walk. onMetaErr decides whether an
// unreadable dep file stops it.
func farmStoreDirs(
	pkgs map[string]string, storeRoot string,
	onMetaErr func(dir string, err error) error,
) ([]string, error) {
	queue := ActiveStoreDirs(pkgs, storeRoot)
	seen := make(map[string]bool, len(queue))
	for _, d := range queue {
		seen[d] = true
	}

	out := make([]string, 0, len(queue))
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		out = append(out, dir)

		md, err := depsmeta.Read(dir)
		if err != nil {
			if stop := onMetaErr(dir, err); stop != nil {
				return nil, stop
			}
			continue
		}
		for _, dep := range md.Deps {
			// Resolve by bare version so the store returns the
			// highest installed revision. The version pin holds
			// SONAME/ABI identity; the revision floats to what is
			// actually on disk, matching how top-level generation
			// entries resolve. Pinning the recorded dep.Revision
			// exactly dropped a dep from the farm whenever its
			// installed revision advanced past the revision a
			// dependent recorded in .gale-deps.toml (gh#172). The
			// recorded revision still drives staleness elsewhere;
			// this floats only farm resolution.
			depDir := resolveStoreDir(storeRoot, dep.Name, dep.Version)
			if !seen[depDir] {
				seen[depDir] = true
				queue = append(queue, depDir)
			}
		}
	}
	return out, nil
}

//go:embed gale-readme.md
var galeReadme []byte

// Build creates a new generation from the package map
// and atomically swaps the current symlink. Packages whose
// store dir is missing are silently skipped with a warning
// on stderr: gale.toml legitimately lists packages that
// aren't installed locally (`gale add` without sync, a
// fresh clone on a new host, an unsupported-platform skip),
// and erroring the rebuild would desync PATH from config
// (gh#68). The missing package is reported on stderr so the
// user learns which package fell off PATH. Previous
// generations are retained for history and rollback.
func Build(pkgs map[string]string, galeDir, storeRoot string) error {
	return BuildWithValidate(pkgs, galeDir, storeRoot, nil)
}

// BuildWithValidate is Build plus an optional revalidation callback
// run immediately after the store-generation lock is acquired and
// before anything about the new generation is created or mutated.
// A locked sync (design section 6) uses this to re-check every plan
// entry — canonical version-revision, artifact SHA, method, manifest
// digest, graph_digest — right before activation, closing the gap
// between an earlier per-artifact verification and the swap that
// makes it live. The callback runs under the SAME lock acquisition
// that guards generation construction, the current-symlink swap, and
// the farm rebuild; a second acquisition would reopen exactly the
// window this exists to close. On a callback error, build aborts
// before creating the generation directory, so nothing is mutated:
// no gen dir, no current-symlink change, no farm rebuild. A nil
// validate makes this identical to plain Build.
func BuildWithValidate(
	pkgs map[string]string, galeDir, storeRoot string, validate func() error,
) error {
	return build(pkgs, galeDir, storeRoot, validate)
}

func build(pkgs map[string]string, galeDir, storeRoot string, validate func() error) error {
	// Use the store-rooted lock path so project-scoped and global
	// Build calls contend on the same lock file as the installer.
	// filepath.Dir(storeRoot) is always the global galeDir
	// (~/.gale/ at global scope, same at project scope since the
	// store is shared). This closes the residual install-vs-project-
	// sync race described in installer.go:storeGenLockPath.
	lockPath := filepath.Join(filepath.Dir(storeRoot), "generation.lock")
	return filelock.With(lockPath, func() error {
		// Revalidate first, before touching Current or anything
		// else — a caller's callback error must abort with zero
		// mutation, and it must do so without ever releasing this
		// lock in between (see BuildWithValidate doc).
		if validate != nil {
			if err := validate(); err != nil {
				return err
			}
		}

		prev, err := Current(galeDir)
		if err != nil {
			return fmt.Errorf("read current generation: %w", err)
		}

		// Carry forward any package whose pinned store dir
		// is missing — keeps a working version on PATH when
		// gale.toml pins something that hasn't been installed
		// yet (e.g. `gale add` without sync, a fresh clone).
		if prev > 0 {
			pkgs = carryForwardMissingVersions(
				pkgs, storeRoot, galeDir, prev,
			)
		}

		// Allocate above the highest generation ever built, not
		// above current. Rollback moves current backwards, and
		// current+1 would then name a snapshot that already
		// exists and overwrite it (gh#189). A generation number,
		// once allocated, permanently identifies one snapshot:
		// current is a pointer into history, the counter only
		// moves forward, and a gap above current is normal.
		nums, err := genNumbers(galeDir)
		if err != nil {
			return err
		}
		highest := 0
		if len(nums) > 0 {
			highest = nums[len(nums)-1]
		}
		next := max(prev, highest) + 1

		genDir := filepath.Join(
			galeDir, "gen", strconv.Itoa(next),
		)

		// Tear down whatever sits at this number before
		// populating. The scan above rules out a leftover
		// directory, but it skips entries that are not
		// directories, so a regular file or other stray entry can
		// still occupy gen/<next> — os.MkdirAll would fail with
		// ENOTDIR, and symlinkDir's skip-if-dst-exists logic
		// would merge into anything it could traverse, shipping a
		// gen with the wrong store revisions or with leftover
		// symlinks for packages no longer in pkgs.
		// validateGenerationSymlinks doesn't catch that — stale
		// links still resolve, just to the wrong place.
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

		if err := populateGeneration(genDir, pkgs, storeRoot); err != nil {
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

		// Run the cross-project farm guard BEFORE the swap: a
		// refusal must leave the previous generation active and
		// the farm untouched (design §4). The returned set is
		// the proposed closure plus every other scope's claim,
		// so the wipe-and-recreate rebuild below cannot delete
		// a soname only another scope's binaries resolve.
		active, err := guardedRebuildDirs(pkgs, galeDir, storeRoot)
		if err != nil {
			cleanup()
			return err
		}

		// Atomic swap: create a temporary symlink then rename.
		if err := swapCurrentSymlink(galeDir, next); err != nil {
			cleanup()
			return err
		}

		// Rebuild the shared-lib farm from this
		// generation's packages plus their recorded dep
		// closure (gh#43) and every other scope's claimed
		// closure. Older revisions may still be in the
		// store (awaiting `gale gc`), but they aren't on
		// PATH, aren't claimed, and must not leak into the
		// farm.
		//
		// The swap above is the activation commit point, so a
		// failure here does not roll the generation back: undoing
		// a completed swap would be a second fallible transaction
		// that cannot reliably restore a partially mutated farm.
		// It is not swallowed either. The farm is what binaries
		// resolve their dylibs through, so an incomplete one is a
		// real failure the caller must see, and a line on stderr
		// inside a direnv hook is invisible (design revision 6,
		// section 6).
		if err := farm.Rebuild(
			active, farm.DirFromStoreRoot(storeRoot),
		); err != nil {
			return fmt.Errorf(
				"generation %d is active, but the shared library "+
					"farm is incomplete; run gale sync again to "+
					"repair it: %w", next, err,
			)
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
// The cutoff is numeric, so after a rollback (current below the
// highest gen) the gens above current are all preserved, and the
// numbering may have gaps. Both are expected: current is a pointer
// into history, not a high-water mark (gh#189).
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
		nums, err := genNumbers(galeDir)
		if err != nil {
			return err
		}
		genRoot := filepath.Join(galeDir, "gen")
		for _, n := range nums {
			if n >= cutoff {
				continue
			}
			if err := os.RemoveAll(
				filepath.Join(genRoot, strconv.Itoa(n)),
			); err != nil {
				return fmt.Errorf(
					"remove gen %d: %w", n, err,
				)
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
// filename conflicts. Missing store dirs are silently
// skipped with a warning (see Build).
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
				// gale.toml legitimately lists packages that
				// aren't installed locally (`gale add` without
				// sync, a fresh clone, an unsupported-platform
				// skip). Failing the rebuild would desync PATH
				// from config (gh#68). Skip the package, but
				// warn so the user learns which package fell
				// off PATH — a sync batch where one install
				// fails must still land the successful installs
				// (Issue #20).
				fmt.Fprintf(os.Stderr,
					"generation: skipping %s@%s: store dir missing (%s); "+
						"run `gale sync` to restore\n",
					name, version, pkgDir)
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
// depth for Build: catches store mutations racing with the
// generation rebuild and ensures the swap never activates
// a generation with broken PATH entries. Reads per-file
// stat, not a full SHA verify — we only care that the
// target exists on disk.
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
