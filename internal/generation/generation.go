package generation

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/depsmeta"
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

func resolvePkgDir(storeRoot, name, version string, fetch map[string]string) string {
	if sha := fetch[name]; sha != "" {
		p, err := store.NewStore(storeRoot).FetchPath(name, version, sha)
		if err == nil {
			return p
		}
	}
	return resolveStoreDir(storeRoot, name, version)
}

type storeRel struct {
	name, version, ownerRel string
}

func parseStoreRel(rel string) (storeRel, bool) {
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) >= 3 && parts[0] == store.FetchNamespace {
		name := parts[1]
		version := stripFetchSuffix(parts[2])
		ownerRel := filepath.Join(parts[0], parts[1], parts[2])
		if name == "" || version == "" {
			return storeRel{}, false
		}
		return storeRel{name: name, version: version, ownerRel: ownerRel}, true
	}
	if len(parts) >= 2 && parts[0] != store.FetchNamespace {
		return storeRel{
			name:     parts[0],
			version:  parts[1],
			ownerRel: filepath.Join(parts[0], parts[1]),
		}, true
	}
	return storeRel{}, false
}

func stripFetchSuffix(dir string) string {
	const suffix = 1 + 12 // hyphen + sha12
	if len(dir) > suffix && dir[len(dir)-suffix] == '-' {
		return dir[:len(dir)-suffix]
	}
	i := strings.LastIndex(dir, "-")
	if i <= 0 {
		return dir
	}
	return dir[:i]
}

func pathExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
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
	fetch map[string]string,
) map[string]string {
	prevGenDir := filepath.Join(galeDir, "gen", strconv.Itoa(prevGen))
	prev := genVersions(prevGenDir, storeRoot)
	if len(prev) == 0 {
		return pkgs
	}

	out := make(map[string]string, len(pkgs))
	for name, version := range pkgs {
		out[name] = version
		if _, ok := fetch[name]; ok {
			continue
		}
		if _, err := os.Stat(resolvePkgDir(storeRoot, name, version, fetch)); err == nil {
			continue
		}
		prevVer, ok := prev[name]
		if !ok || prevVer == version {
			continue
		}
		if _, err := os.Stat(resolvePkgDir(storeRoot, name, prevVer, fetch)); err != nil {
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
	out, _ := genVersionsWalk(genDir, storeRoot,
		func(error) error { return nil }) // keep walking past every error
	return out
}

// genVersionsStrict is genVersions for callers that must not act on
// a partial answer: an unreadable walk root, an unreadable entry or
// an unreadable link stops the walk and returns the error, named by
// path, instead of shrinking the map in silence.
//
// gc's retention needs this. genVersions' leniency is correct for a
// rebuild, which should still link everything it can read, and wrong
// for a decision to destroy bytes: "I could not read this
// generation" arrives at retention as "this generation references
// nothing", and the sweep then deletes what it could not see
// (gh#210). It is the same split AuthoritativeGenerationDirs draws
// against this walk, and FarmStoreDirsStrict against a best-effort
// walk — tolerate a partial answer where a partial answer is still
// useful, never where a decision rests on it.
func genVersionsStrict(
	genDir, storeRoot string,
) (map[string]string, error) {
	return genVersionsWalk(genDir, storeRoot,
		func(err error) error { return err })
}

// genVersionsWalk is the shared walk. onErr decides whether an
// unreadable entry stops it. Walk reports a root that cannot be
// Lstat'd through the same callback, so an unreadable generation
// directory reaches onErr like any other entry.
func genVersionsWalk(
	genDir, storeRoot string,
	onErr func(err error) error,
) (map[string]string, error) {
	// Resolve storeRoot through symlinks so relative path
	// computation works on macOS where /var → /private/var.
	absStore, err := filepath.EvalSymlinks(storeRoot)
	if err != nil {
		absStore = storeRoot
	}

	out := map[string]string{}
	walkErr := filepath.Walk(genDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return onErr(fmt.Errorf("walking %s: %w", path, err))
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return nil
		}
		// Read the link text rather than resolving it: the leaf
		// target may be absent while the store dir still exists.
		target, readErr := os.Readlink(path)
		if readErr != nil {
			return onErr(fmt.Errorf("reading link %s: %w", path, readErr))
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
		parsed, ok := parseStoreRel(rel)
		if !ok {
			return nil
		}
		name, version, ownerRel := parsed.name, parsed.version, parsed.ownerRel
		// Skip when the owning store dir is gone (GC'd package):
		// a dangling link to a removed store dir must not surface.
		if !pathExists(filepath.Join(absStore, ownerRel)) &&
			!pathExists(filepath.Join(storeRoot, ownerRel)) {
			return nil
		}
		if _, seen := out[name]; !seen {
			out[name] = version
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return out, nil
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

// CurrentStoreDirs returns name → absolute store directory
// the active generation links. A fetch tree and a source
// ResolveDir that share a version are different directories;
// activation uses this map so a ResolveDir link cannot
// satisfy a FetchPath identity.
func CurrentStoreDirs(galeDir, storeRoot string) (map[string]string, error) {
	genDir, err := currentGenDir(galeDir)
	if err != nil {
		return nil, err
	}
	if genDir == "" {
		return map[string]string{}, nil
	}
	return storeDirsWalk(genDir, storeRoot, func(err error) error { return err })
}

func storeDirsWalk(
	genDir, storeRoot string,
	onErr func(err error) error,
) (map[string]string, error) {
	absStore, err := filepath.EvalSymlinks(storeRoot)
	if err != nil {
		absStore = storeRoot
	}
	out := map[string]string{}
	walkErr := filepath.Walk(genDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return onErr(fmt.Errorf("walking %s: %w", path, err))
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return nil
		}
		target, readErr := os.Readlink(path)
		if readErr != nil {
			return onErr(fmt.Errorf("reading link %s: %w", path, readErr))
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		rel := relWithinStore(absStore, target)
		if rel == "" {
			rel = relWithinStore(storeRoot, target)
		}
		if rel == "" {
			return nil
		}
		parsed, ok := parseStoreRel(rel)
		if !ok {
			return nil
		}
		owner := filepath.Join(storeRoot, parsed.ownerRel)
		if !pathExists(owner) {
			owner = filepath.Join(absStore, parsed.ownerRel)
		}
		if !pathExists(owner) {
			return nil
		}
		if _, seen := out[parsed.name]; !seen {
			out[parsed.name] = owner
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return out, nil
}

// CurrentVersions returns the package name → version map of
// the active generation by reading its symlinks. Returns an
// empty map (no error) when no generation is active yet.
// Used by sync to detect whether gale.toml has drifted from
// the active generation — drift means the rebuild must run
// even if no installs happened, so removed packages drop off
// PATH.
func CurrentVersions(galeDir, storeRoot string) (map[string]string, error) {
	genDir, err := currentGenDir(galeDir)
	if err != nil {
		return nil, err
	}
	if genDir == "" {
		return map[string]string{}, nil
	}
	return genVersions(genDir, storeRoot), nil
}

// CurrentVersionsStrict is CurrentVersions backed by
// genVersionsStrict: an unreadable active generation is an error
// naming the directory, not an empty map. Callers that decide
// whether to destroy bytes use this one — see genVersionsStrict for
// why the two exist (gh#210).
//
// A scope with no active generation is still empty, not a failure:
// a registered project that has never synced genuinely references
// nothing.
func CurrentVersionsStrict(
	galeDir, storeRoot string,
) (map[string]string, error) {
	genDir, err := currentGenDir(galeDir)
	if err != nil {
		return nil, err
	}
	if genDir == "" {
		return map[string]string{}, nil
	}
	return genVersionsStrict(genDir, storeRoot)
}

// currentGenDir returns the directory of the active generation
// under galeDir, or "" when no generation is active yet.
func currentGenDir(galeDir string) (string, error) {
	cur, err := Current(galeDir)
	if err != nil {
		return "", err
	}
	if cur == 0 {
		return "", nil
	}
	return filepath.Join(galeDir, "gen", strconv.Itoa(cur)), nil
}

// ActiveStoreDirs resolves each (name, version) in pkgs to
// its on-disk store dir. Returned in an arbitrary order.
// Used by FarmStoreDirsStrict and `gale doctor`.
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

// FarmStoreDirsStrict returns the store dirs reachable from pkgs
// plus each dir's recorded runtime closure. An unreadable
// .gale-deps.toml stops the walk and returns the error instead of
// warning past it.
//
// gc uses this: a decision that destroys bytes must not act on a
// partial answer. It is the same split the provenance reader draws
// against depsmeta's leniency — tolerate a partial answer where a
// partial answer is still useful, never where a decision rests on
// it.
//
// Strict about an UNREADABLE record, deliberately not about an
// ABSENT one. Both walks read through depsmeta.Read, which decodes a
// missing .gale-deps.toml to an empty Metadata, so a committed store
// dir that records nothing is a LEAF here. Moving this reader to
// depsmeta.ReadStrict, which tells StateAbsent from StateRecorded,
// looks like the cleanup that lets one reader serve every caller. It
// is not (gh#238). closure.go's proposed() states the constraint:
// calling a committed directory with no metadata unknown would make
// the claim unusable for every package installed before metadata
// existed, and an unusable claim refuses every operation on the
// machine. Every caller here fails closed, so that refusal is every
// install and every removal, with no upgrade path for a store that
// predates the file.
//
// AuthoritativeClosure is the contrasting case, and the contrast is
// what makes both readers right where they sit. It decides whether to
// DESTROY bytes, so absence there is unknown and unknown is a
// refusal — its doc comment says why this walk cannot serve it. This
// walk decides what a scope must KEEP, where absence costs nothing it
// could have named and a refusal costs the machine everything.
//
// TestFarmStoreDirsStrictTreatsAbsentDepsMetadataAsLeaf pins it.
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
			// exactly dropped a dep whenever its installed
			// revision advanced past the revision a dependent
			// recorded in .gale-deps.toml (gh#172). The recorded
			// revision still drives staleness elsewhere; this
			// floats only store-dir resolution.
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
//
// Two packages shipping the same bin/ basename are refused, before
// the current symlink moves. There is no overlay that names a
// winner.
func Build(pkgs map[string]string, galeDir, storeRoot string) error {
	return BuildWithOptions(pkgs, galeDir, storeRoot, Options{})
}

// Options carries Build's optional inputs. A zero Options is plain
// Build.
type Options struct {
	// Validate is design section 6's revalidation callback. See
	// BuildWithValidate.
	Validate func() error
	// Fetch maps a package name to the artifact SHA256 used
	// with store.FetchPath. When set, Build links that fetch
	// tree and never a source ResolveDir for the same name.
	Fetch map[string]string
}

// BuildWithOptions is Build with the optional inputs in Options.
// Build and BuildWithValidate delegate here.
func BuildWithOptions(
	pkgs map[string]string, galeDir, storeRoot string, opts Options,
) error {
	return build(pkgs, galeDir, storeRoot, opts)
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
	return BuildWithOptions(pkgs, galeDir, storeRoot, Options{Validate: validate})
}

// nextGenNumber returns the number the next generation takes:
// above the highest ever built, not above current.
//
// Rollback moves current backwards, and current+1 would then name a
// snapshot that already exists and overwrite it (gh#189). A
// generation number, once allocated, permanently identifies one
// snapshot: current is a pointer into history, the counter only moves
// forward, and a gap above current is normal.
func nextGenNumber(galeDir string, prev int) (int, error) {
	nums, err := genNumbers(galeDir)
	if err != nil {
		return 0, err
	}
	highest := 0
	if len(nums) > 0 {
		highest = nums[len(nums)-1]
	}
	return max(prev, highest) + 1, nil
}

func build(pkgs map[string]string, galeDir, storeRoot string, opts Options) error {
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
		if opts.Validate != nil {
			if err := opts.Validate(); err != nil {
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
				pkgs, storeRoot, galeDir, prev, opts.Fetch,
			)
		}

		next, err := nextGenNumber(galeDir, prev)
		if err != nil {
			return err
		}

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

		if err := populateGeneration(
			genDir, pkgs, storeRoot, opts,
		); err != nil {
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

// retainedNumbers returns the generation numbers whose store
// closures must survive a sweep, sorted ascending: the keep-2
// window (current + the previous existing generation) and every
// generation ABOVE current.
//
// The branch above current exists only after a rollback, and it
// is retained history a roll-forward may return to — a number,
// once allocated, permanently identifies one snapshot (gh#189).
// cleanOldGenerations uses KeptNumbers, which shares the same
// keep-2 window below current and drops the branch above.
// A later rebuild allocates above the highest number; keep-2
// then prunes history below the new window (gh#206).
//
// History below the keep-2 window is not retained. Those
// generations are the ones auto-gc and gale gc delete, and
// reclaiming a superseded revision that has fallen out of
// the window is gc's most common job (gh#137).
//
// curGen == 0 (no current symlink) retains everything, matching
// the `n >= 0` skip that already stopped cleanOldGenerations
// from deleting anything in that state. It also stops a lost
// current symlink from letting gc sweep the store bare.
//
// The active generation is always in the set, even when its
// directory is absent: a numeric current pointing at nothing must
// reach RetainedVersionsStrict as an UNREADABLE generation, not
// as a generation nobody listed, or gc's refuse-to-sweep path
// (gh#188) never fires.
//
// curGen is read BEFORE the listing, as PruneOldGenerations and
// cleanOldGenerations both do: under the generation lock that
// ordering keeps the snapshot consistent with the directory scan.
func retainedNumbers(galeDir string) ([]int, error) {
	curGen, err := Current(galeDir)
	if err != nil {
		return nil, fmt.Errorf("read current: %w", err)
	}
	nums, err := genNumbers(galeDir)
	if err != nil {
		return nil, err
	}

	if curGen <= 0 {
		return nums, nil
	}
	retained := keptAtOrBelow(nums, curGen, config.DefaultGenerationKeep)
	for _, n := range nums {
		if n > curGen {
			retained = append(retained, n)
		}
	}
	if !slices.Contains(retained, curGen) {
		retained = append(retained, curGen)
		slices.Sort(retained)
	}
	return retained, nil
}

// RetainedNumbers is retainedNumbers for callers outside the
// package. The raw directory scan stays unexported — what widens
// here is the retention POLICY, which gc has to agree with
// exactly, not the listing (gh#206 declined to export that).
func RetainedNumbers(galeDir string) ([]int, error) {
	return retainedNumbers(galeDir)
}

// KeptNumbers is current plus the previous existing
// generation, counted positionally (gh#248). Abandoned
// generations above current after a rollback are not kept.
// A missing current symlink keeps nothing. The active
// number is always included so a dangling current reaches
// KeptStoreDirs as an unreadable generation.
func KeptNumbers(galeDir string) ([]int, error) {
	cur, err := Current(galeDir)
	if err != nil {
		return nil, fmt.Errorf("read current: %w", err)
	}
	if cur <= 0 {
		return nil, nil
	}
	nums, err := genNumbers(galeDir)
	if err != nil {
		return nil, err
	}
	kept := keptAtOrBelow(nums, cur, config.DefaultGenerationKeep)
	if !slices.Contains(kept, cur) {
		kept = append(kept, cur)
		slices.Sort(kept)
	}
	return kept, nil
}

// KeptStoreDirs walks the two kept generations' symlink
// targets and returns each exact owner path, including
// fetch/<name>/<version>-<sha12>. An unreadable kept
// generation is an error naming the number.
func KeptStoreDirs(galeDir, storeRoot string) ([]string, error) {
	nums, err := KeptNumbers(galeDir)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var out []string
	for _, n := range nums {
		dirs, err := storeDirsWalk(
			filepath.Join(galeDir, "gen", strconv.Itoa(n)),
			storeRoot,
			func(err error) error { return err },
		)
		if err != nil {
			return nil, fmt.Errorf(
				"reading kept generation %d: %w", n, err,
			)
		}
		for _, dir := range dirs {
			if _, ok := seen[dir]; ok {
				continue
			}
			seen[dir] = struct{}{}
			out = append(out, dir)
		}
	}
	sort.Strings(out)
	return out, nil
}

// RetainedVersionsStrict returns every package version the
// retained generations link, as name → versions. gc keys its
// retention set from it, so a generation whose directory gc keeps
// can never have its store closure swept — a directory without
// its closure is a hollow generation, and rolling onto one puts
// dangling entries on PATH (gh#247).
//
// The answer is name → VERSIONS, unlike CurrentVersions and its
// strict sibling: those describe one active environment, where a
// package has exactly one version. Retention spans generations,
// and the active one plus an abandoned branch routinely link two
// versions of the same package — that is the ordinary shape after
// any upgrade. Collapsing to one version per name would drop the
// other store dir out of retention and sweep it.
//
// Strict, never lenient (gh#210): an unreadable retained
// generation is an error naming the number, which gc's
// refuse-to-sweep path turns into a run that deletes nothing. The
// lenient reader answers "links nothing" for a generation it
// could not read, and a sweep computed from that deletes what it
// could not see. generation.List is backed by the lenient reader
// and must never become a retention source for the same reason.
func RetainedVersionsStrict(
	galeDir, storeRoot string,
) (map[string][]string, error) {
	nums, err := retainedNumbers(galeDir)
	if err != nil {
		return nil, err
	}
	genBase := filepath.Join(galeDir, "gen")
	out := map[string][]string{}
	for _, n := range nums {
		pkgs, err := genVersionsStrict(
			filepath.Join(genBase, strconv.Itoa(n)), storeRoot,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"reading retained generation %d: %w", n, err,
			)
		}
		for name, version := range pkgs {
			if !slices.Contains(out[name], version) {
				out[name] = append(out[name], version)
			}
		}
	}
	return out, nil
}

// gensAtOrBelow returns the ascending scan filtered to
// n <= curGen. Shared by the keep-window helpers so prune
// and gc count the same set (gh#248).
func gensAtOrBelow(nums []int, curGen int) []int {
	var out []int
	for _, n := range nums {
		if n <= curGen {
			out = append(out, n)
		}
	}
	return out
}

// keptAtOrBelow is the highest `keep` generations at or
// below curGen, counted positionally over the scan. Empty
// when keep or curGen is non-positive.
func keptAtOrBelow(nums []int, curGen, keep int) []int {
	if keep <= 0 || curGen <= 0 {
		return nil
	}
	below := gensAtOrBelow(nums, curGen)
	if len(below) <= keep {
		return below
	}
	return below[len(below)-keep:]
}

// prunableNumbers returns the generation numbers
// PruneOldGenerations must remove, given the ascending scan nums
// and the active generation curGen: everything at or below curGen
// except the highest keep, counted POSITIONALLY over the scan.
// The result is ascending, and empty when keep or curGen is
// non-positive.
//
// Positional, not numeric: gh#189 made allocation
// max(prev, highest)+1, so the numbering legitimately has gaps,
// and the old cutoff curGen-keep+1 counted the integers in a
// range rather than the generations that exist. With gens
// 1, 5, 9, 10 and keep 3 it retained two, destroying a rollback
// target the keep setting promised (gh#248); with a current above
// every staged generation it retained none.
//
// Generations above curGen are never prunable. That branch —
// visible after a rollback — is retained history a roll-forward
// may return to, and a user who abandoned it on purpose reclaims
// it by name (gh#189, gh#206). It is also where an in-flight
// gen/curGen+1 from a concurrent Build lives.
//
// keptAtOrBelow is the complement of this set among the
// generations at or below current. KeptNumbers and
// retainedNumbers use that helper so gale gc cannot undo
// the keep promise auto-gc just made.
func prunableNumbers(nums []int, curGen, keep int) []int {
	if keep <= 0 || curGen <= 0 {
		return nil
	}
	below := gensAtOrBelow(nums, curGen)
	if len(below) <= keep {
		return nil
	}
	return below[:len(below)-keep]
}

// PruneOldGenerations removes old generation directories,
// preserving the highest `keep` generations at or below curGen
// (the current one among them) and everything above curGen —
// including any in-flight gen/curGen+1 a concurrent Build may
// have created. Holds the store-rooted gen lock for its critical
// section so it serializes with Build.
//
// Retention is a count over the generations that exist, not a
// numeric cutoff: the numbering may have gaps, and current is a
// pointer into history rather than a high-water mark (gh#189).
// prunableNumbers has the rule.
//
// Returns the removed gen numbers in ascending order so the
// caller can report them. keep<=0 or no current symlink is a
// no-op (returns nil).
//
// Intended as an auto-gc hook after Build: production
// passes keep 2 so per-install gen accumulation can't
// drown the filesystem in inodes (the dev-host incident
// with ~3M gen/ inodes across 33 untouched gens).
func PruneOldGenerations(galeDir, storeRoot string, keep int) ([]int, error) {
	if keep <= 0 {
		return nil, nil
	}
	lockPath := filepath.Join(filepath.Dir(storeRoot), "generation.lock")
	var removed []int
	err := filelock.With(lockPath, func() error {
		// curGen is read BEFORE the listing, as retainedNumbers
		// and Build do: under the lock that ordering keeps the
		// snapshot consistent with the directory scan.
		curGen, err := Current(galeDir)
		if err != nil {
			return fmt.Errorf("read current: %w", err)
		}
		nums, err := genNumbers(galeDir)
		if err != nil {
			return err
		}
		genRoot := filepath.Join(galeDir, "gen")
		for _, n := range prunableNumbers(nums, curGen, keep) {
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

// Remove deletes the generation directories the caller names and
// returns the removed numbers in ascending order. Duplicated
// targets are removed once.
//
// The verb PruneOldGenerations is not: retention reclaims history
// below current by counting generations, and after a rollback the
// generations ABOVE current are unreachable to it by design — the
// number permanently identifies that snapshot (gh#189), so they
// survive until current climbs back past them. A user who
// abandoned that branch on purpose names it here instead (gh#206).
// Nothing sweeps it automatically: an unnamed set is exactly what
// makes a destructive default unsafe.
//
// Guards, in order:
//
//   - the store-rooted generation lock, the same one Build,
//     Rollback and PruneOldGenerations take. Build holds it across
//     its whole create-then-swap span, so no half-built generation
//     is ever visible here and refusing current needs no companion
//     in-flight guard.
//   - current is read INSIDE the lock, so the snapshot agrees with
//     the directory listing (the reason cleanOldGenerations reads
//     it there too), and removing it is refused: a dangling current
//     empties PATH.
//   - a target absent from genNumbers is refused as nonexistent.
//     genNumbers skips entries that are not directories, so a stray
//     regular file at gen/N is reported, never deleted.
//
// Every target is validated before the first removal, so a batch
// naming current removes nothing at all.
func Remove(galeDir, storeRoot string, targets []int) ([]int, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	lockPath := filepath.Join(filepath.Dir(storeRoot), "generation.lock")
	var removed []int
	err := filelock.With(lockPath, func() error {
		curGen, err := Current(galeDir)
		if err != nil {
			return fmt.Errorf("read current: %w", err)
		}
		nums, err := genNumbers(galeDir)
		if err != nil {
			return err
		}

		wanted := make([]int, 0, len(targets))
		for _, n := range targets {
			if curGen > 0 && n == curGen {
				return fmt.Errorf(
					"refusing to remove generation %d: "+
						"it is the current generation", n,
				)
			}
			if !slices.Contains(nums, n) {
				return fmt.Errorf("generation %d does not exist", n)
			}
			if !slices.Contains(wanted, n) {
				wanted = append(wanted, n)
			}
		}
		slices.Sort(wanted)

		genRoot := filepath.Join(galeDir, "gen")
		for _, n := range wanted {
			if err := os.RemoveAll(
				filepath.Join(genRoot, strconv.Itoa(n)),
			); err != nil {
				return fmt.Errorf("remove gen %d: %w", n, err)
			}
			removed = append(removed, n)
		}
		return nil
	})
	return removed, err
}

// populateGeneration symlinks each package's store contents into
// genDir. Packages are visited in sorted order so the result never
// depends on map iteration. Missing store dirs are silently skipped
// with a warning (see Build).
//
// Two packages shipping the same bin/ basename fail the whole
// generation, naming both providers. Leftover [bin] tables do
// not settle a collision. Sort order used to decide it silently,
// which put a binary on PATH nobody chose (gh#190). Every collision
// is collected before the error returns, so one edit fixes them all.
// Only bin/ is arbitrated: nothing else decides what runs from PATH,
// and lib/, man/ and share/ have always merged across packages.
func populateGeneration(
	genDir string, pkgs map[string]string, storeRoot string,
	opts Options,
) error {
	names := make([]string, 0, len(pkgs))
	for name := range pkgs {
		names = append(names, name)
	}
	sort.Strings(names)

	bins := NewBinArbiter()
	for _, name := range names {
		version := pkgs[name]
		pkgDir := resolvePkgDir(storeRoot, name, version, opts.Fetch)
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
		if err := linkStoreEntries(
			genDir, pkgDir, name, entries, bins,
		); err != nil {
			return err
		}
	}
	return bins.Err()
}

// linkStoreEntries mirrors one package's store contents into the
// generation: its top-level directories, plus root-level files like
// go.env. A root-level file already present from another package is
// left alone, the behavior every non-bin path has always had.
func linkStoreEntries(
	genDir, pkgDir, name string, entries []os.DirEntry, bins *BinArbiter,
) error {
	for _, e := range entries {
		if e.IsDir() {
			if skipTopLevelDirs[e.Name()] {
				continue
			}
			if err := linkStoreDir(
				genDir, pkgDir, name, e.Name(), bins,
			); err != nil {
				return err
			}
			continue
		}

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
	return nil
}

// linkStoreDir mirrors one of a package's top-level store
// directories into the generation. bin/ is arbitrated, so a basename
// a previous package already claimed records a collision instead of
// being skipped in silence; every other directory merges.
func linkStoreDir(
	genDir, pkgDir, name, dir string, bins *BinArbiter,
) error {
	srcDir := filepath.Join(pkgDir, dir)
	dstDir := filepath.Join(genDir, dir)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return fmt.Errorf("create gen %s dir: %w", dir, err)
	}
	var claim func(string) bool
	if dir == "bin" {
		claim = func(binary string) bool {
			return bins.Claim(name, binary)
		}
	}
	if err := symlinkDir(srcDir, dstDir, claim); err != nil {
		return fmt.Errorf("symlink %s/%s: %w", name, dir, err)
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
//
// claim, when non-nil, arbitrates this directory's files: it reports
// whether the calling package may provide each name. Only bin/ passes
// one — a nil claim keeps the historical skip-if-present merge that
// lib/, man/ and share/ rely on. Recursion passes nil as well: a file
// nested under bin/ is not itself on PATH, so it merges like the rest.
func symlinkDir(srcDir, dstDir string, claim func(name string) bool) error {
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
			if err := symlinkDir(src, dst, nil); err != nil {
				return err
			}
			continue
		}

		if claim != nil && !claim(entry.Name()) {
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
