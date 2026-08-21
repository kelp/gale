package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/kelp/gale/internal/attestation"
	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/filelock"
	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/lockplan"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
)

var renameDir = os.Rename

// ErrUnplanned reports a package a locked operation was asked to
// install that the plan does not name. Under a lock the plan is the
// complete set of installs (design §3), so this is a wiring defect
// rather than a lock defect: it means something reached the
// installer without going through plan construction.
var ErrUnplanned = errors.New("package is not in the locked plan")

// ErrUnlockedSource reports a local-directory or git-HEAD install
// attempted while a plan is set. Neither can be locked even in
// principle: a git install's identity is a commit hash and a --path
// install's bytes are whatever sits in the working tree, so no lock
// can name either. They also bypass installLocked entirely, which
// is what makes an explicit refusal necessary rather than merely
// tidy: without it they would commit, and installLocalLocked would
// replaceStoreDir, with no plan check anywhere on the path.
//
// Neither sentinel is mapped in cmd/gale/exitcode.go, deliberately.
// Both mean the caller wired an operation the lock cannot describe,
// which is an ordinary failure (exit 1), not a statement about the
// lockfile's contents (3) or its usability (4).
var ErrUnlockedSource = errors.New(
	"cannot install from a local directory or git HEAD under a lock",
)

// ErrSourceGone reports a source compile, which fetch replaced.
var ErrSourceGone = errors.New(
	"source install is gone; use gale fetch or gale fetch-adopt",
)

// ErrBottleGone reports a leftover GHCR bottle pour, which fetch replaced.
var ErrBottleGone = errors.New(
	"bottle install is gone; use gale fetch or gale fetch-adopt",
)

// RecipeResolver finds and parses a recipe by package name.
// Returns nil if the package has no recipe.
type RecipeResolver func(ctx context.Context, name string) (*recipe.Recipe, error)

// Installer installs packages into the store.
type Installer struct {
	Store    *store.Store
	Resolver RecipeResolver
	// Verifier checks Sigstore attestations for prebuilt
	// binaries. Production wiring always sets it (a native
	// in-process verifier); nil is a test-only seam meaning
	// "skip attestation entirely". When non-nil, sigstore
	// trust ALWAYS verifies — there is no availability probe
	// and no silent skip.
	Verifier   attestation.Verifier
	SourceOnly bool // skip binary, build from source

	// BinaryOnly refuses to demote a failed binary fetch to a
	// source build, exactly as a locked binary node does.
	//
	// `gale migrate` sets it. Migration is a constrained
	// replacement of BINARY-method directories (design §13), and
	// every scope was cleared against the hash the recipe
	// declares for that binary, so a silent source build would
	// commit bytes nobody was asked about. For a pre-revision
	// candidate the canonical destination is absent, so no
	// ReplaceGuard fires to catch it before the commit; the
	// refusal has to happen here or nowhere.
	//
	// SourceOnly is its opposite and the two are mutually
	// exclusive by construction: a caller that sets both has
	// asked for a package that cannot be installed, and
	// binaryViable is false, so BinaryOnly reports it rather
	// than building.
	BinaryOnly bool

	// Plan, when set, makes the lockfile the exclusive selector of
	// versions, artifacts, methods and dependency edges (design §3).
	// Every package this installer touches must name a node in it;
	// one that does not is ErrUnplanned rather than a live
	// resolution, because a single unplanned node is enough to
	// reintroduce the behaviour the lock exists to remove.
	//
	// nil is unlocked mode, which is byte-for-byte today's
	// behaviour: streaming installs, source fallback, and the
	// staged stale-reinstall path.
	Plan *lockplan.Plan

	// ReplaceGuard, when set, is design §13's cross-scope veto: it
	// is called inside the store-gen lock immediately before a
	// staged artifact supersedes an existing directory, with the
	// artifact actually about to be committed. A non-nil error
	// aborts with everything as it was. nil skips the guard, and
	// also skips superseding, which is every caller that is not
	// replacing bytes on purpose.
	ReplaceGuard func(rep Replacement) error
}

// InstallMethod represents how a package was installed.
type InstallMethod string

const (
	MethodBinary InstallMethod = "binary"
	MethodSource InstallMethod = "source"
	MethodCached InstallMethod = "cached"
)

// InstallResult holds the outcome of an install.
type InstallResult struct {
	Name    string
	Version string
	Method  InstallMethod
	SHA256  string // hex hash of installed archive
	// ManifestDigest is the OCI manifest digest from
	// .binaries.toml; empty for source builds.
	ManifestDigest string
}

// Replacement is what a ReplaceGuard decides about: one staged
// artifact and the occupied canonical directory it is about to
// overwrite.
//
// StagingDir is carried because the guard's question is not only
// "may these bytes replace those" but also "are these bytes worth
// replacing them with". recordProvenance deliberately commits an
// artifact with no record when its closure cannot be attested, so
// the result alone cannot tell a guard whether the candidate is
// provenanced; the staged record can.
//
// Only an occupied CANONICAL directory is described here. A
// pre-revision install resolves to a bare "<v>" directory that this
// install does not touch, and relocating it is machine-wide
// migrate's business rather than one scope's.
type Replacement struct {
	CanonicalDir string
	StagingDir   string
	Result       InstallResult
}

// errNoBinaryDeclared reports a BinaryOnly install of a recipe that
// declares no prebuilt binary for this platform.
var errNoBinaryDeclared = errors.New(
	"the recipe declares no binary for this platform",
)

// refusalLabel names which rule refused a demotion to source, since
// the two carry different remedies: a locked node is repaired by
// fixing the lock or the artifact, and a BinaryOnly caller is a
// command that has already ruled source builds out.
func refusalLabel(locked bool) string {
	if locked {
		return "locked binary install"
	}
	return "binary install"
}

// Install installs a recipe into the store and links binaries.
func (inst *Installer) Install(ctx context.Context, r *recipe.Recipe) (*InstallResult, error) {
	return inst.install(ctx, r, false)
}

// Reinstall is Install but skips the IsInstalled cache check so
// callers can force a fresh install even when the store already
// satisfies the request. Used by sync's stale-reinstall path to
// migrate pre-revision bare-dir installs into the canonical layout.
func (inst *Installer) Reinstall(ctx context.Context, r *recipe.Recipe) (*InstallResult, error) {
	return inst.install(ctx, r, true)
}

func (inst *Installer) install(ctx context.Context, r *recipe.Recipe, force bool) (*InstallResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	unlock, err := lockPackage(inst.Store.Root, r.Package.Name, r.Package.Full())
	if err != nil {
		return nil, fmt.Errorf("lock package: %w", err)
	}
	defer unlock()
	return inst.installLocked(ctx, r, force)
}

// installDest is the store path installLocked will write into.
// staged means a sibling rebuild dir; the caller defers cleanup
// so a failed reinstall cannot leave the tmp behind and so
// commitStaged still sees the dir.
type installDest struct {
	name         string
	version      string
	storeVersion string
	canonicalDir string
	storeDir     string
	staged       bool
	locked       bool
	planned      lockplan.Node
}

// prepareInstallDest picks the dest dir and answers the cache
// question. A non-nil cached result means the store already
// satisfies the request and installLocked must return it.
func (inst *Installer) prepareInstallDest(r *recipe.Recipe, force bool) (installDest, *InstallResult, error) {
	d := installDest{
		name:         r.Package.Name,
		version:      r.Package.Version,
		storeVersion: r.Package.Full(),
	}
	d.canonicalDir = filepath.Join(inst.Store.Root, d.name, d.storeVersion)

	planned, locked, err := inst.plannedNode(d.name, d.storeVersion)
	if err != nil {
		return d, nil, err
	}
	d.planned = planned
	d.locked = locked
	if locked {
		cached, hit, cerr := inst.lockedCacheHit(planned, d.name, d.version)
		if cerr != nil {
			return d, nil, cerr
		}
		if hit {
			return d, &cached, nil
		}
		force = false
	}

	// Cache check. The default path accepts IsInstalled's
	// back-compat fallback (bare pre-revision dirs count as
	// "installed"), so dep installs don't needlessly
	// re-migrate every package.
	//
	// The forced path (Reinstall) always rebuilds into a
	// sibling staging dir first. The live canonical dir stays
	// intact until the final replace succeeds, so a failed
	// stale reinstall does not break the active generation.
	//
	// A working-tree recipe whose digest no longer matches the
	// sidecar is not a cache hit: the occupied dir was built
	// from different recipe bytes (gh#265). Force the staged
	// rebuild rather than falling through into Store.Create on
	// the live dir — binary-fallback's os.RemoveAll would
	// otherwise delete the canonical package.
	if !locked && !force && inst.Store.IsInstalled(d.name, d.storeVersion) {
		occupied := d.canonicalDir
		if dir, ok := inst.Store.StorePath(d.name, d.storeVersion); ok {
			occupied = dir
		}
		if !workingTreeRecipeStale(occupied, r) {
			return d, &InstallResult{
				Name:    d.name,
				Version: d.version,
				Method:  MethodCached,
			}, nil
		}
		force = true
	}

	if force {
		pkgDir := filepath.Join(inst.Store.Root, d.name)
		if err := os.MkdirAll(pkgDir, 0o755); err != nil {
			return d, nil, fmt.Errorf("create package dir: %w", err)
		}
		buildDir, err := os.MkdirTemp(pkgDir, ".build-")
		if err != nil {
			return d, nil, fmt.Errorf("create reinstall staging dir: %w", err)
		}
		d.storeDir = buildDir
		d.staged = true
		return d, nil, nil
	}
	storeDir, err := inst.Store.Create(d.name, d.storeVersion)
	if err != nil {
		return d, nil, fmt.Errorf("create store dir: %w", err)
	}
	d.storeDir = storeDir
	return d, nil, nil
}

// installLocked is the body of install assuming the per-package
// lock is held by the caller. Used by install() and by
// InstallWithFinalize (added in a follow-up commit).
func (inst *Installer) installLocked(ctx context.Context, r *recipe.Recipe, force bool) (*InstallResult, error) {
	dest, cached, err := inst.prepareInstallDest(r, force)
	if err != nil {
		return nil, err
	}
	if cached != nil {
		return cached, nil
	}
	os.RemoveAll(dest.storeDir)
	return nil, fmt.Errorf(
		"%s: %w",
		lockgraph.Key(dest.name, dest.storeVersion), ErrBottleGone,
	)
}

// dirOccupied reports whether path is a directory holding at least
// one entry. An absent path is free; an empty one is not occupied,
// since a caller that skipped it would report an install that put
// nothing on PATH.
func dirOccupied(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	return len(entries) > 0, nil
}

// InstallLocalWithFinalize acquires the per-package lock, runs the
// local source install, then invokes finalize() while still holding
// the lock, then releases. finalize == nil is a no-op. finalize
// errors are returned alongside the InstallResult so the caller
// sees partial state.
func (inst *Installer) InstallLocalWithFinalize(
	r *recipe.Recipe, _ string,
	_ func(*InstallResult) error,
) (*InstallResult, error) {
	return nil, fmt.Errorf("%s: %w", r.Package.Name, ErrSourceGone)
}

// InstallGitWithFinalize acquires the per-package lock (keyed on the
// resolved commit hash), runs the git install, then invokes finalize()
// while still holding the lock, then releases. finalize == nil is a
// no-op. finalize errors are returned alongside the InstallResult so
// the caller sees partial state.
func (inst *Installer) InstallGitWithFinalize(r *recipe.Recipe, _ func(*InstallResult) error) (*InstallResult, error) {
	return nil, fmt.Errorf("%s: %w", r.Package.Name, ErrSourceGone)
}

// commitStaged finishes a staged reinstall by renaming the
// staging dir into the canonical store path. The rename happens
// under the store-gen lock so a concurrent generation rebuild
// sees either the pre-install or completed install — never an
// intermediate.
//
// If the rename fails, replaceStoreDir restores the prior
// canonical dir from its .bak sibling.
//
// The replace guard runs before the rename: a refusal decided
// afterwards would already have overwritten the bytes it was
// meant to protect (design §13).
func (inst *Installer) commitStaged(
	storeRoot, stagingDir string, rep Replacement,
) error {
	return withStoreGenLock(storeRoot, func() error {
		if err := inst.guardReplace(rep); err != nil {
			return err
		}
		return replaceStoreDir(rep.CanonicalDir, stagingDir)
	})
}

// guardReplace runs design §13's cross-scope veto for one staged
// artifact about to overwrite an occupied canonical directory. Nil
// ReplaceGuard means unwired: no guard.
//
// An ABSENT canonical dir is skipped, because nothing is being
// replaced and no prior claim can be contradicted by an install into
// free space. os.Lstat, not os.Stat: a link at the canonical path
// would otherwise have the guard asked about one path while the
// rename lands at another.
func (inst *Installer) guardReplace(rep Replacement) error {
	if inst.ReplaceGuard == nil {
		return nil
	}
	if _, err := os.Lstat(rep.CanonicalDir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf(
			"stat %s before replacing it: %w", rep.CanonicalDir, err,
		)
	}
	return inst.ReplaceGuard(rep)
}

func replaceStoreDir(storeDir, buildDir string) error {
	backupDir := storeDir + ".bak"
	_ = os.RemoveAll(backupDir)

	if _, err := os.Stat(storeDir); err == nil {
		if err := renameDir(storeDir, backupDir); err != nil {
			return fmt.Errorf("backup existing store dir: %w", err)
		}
	}

	if err := renameDir(buildDir, storeDir); err != nil {
		if _, statErr := os.Stat(backupDir); statErr == nil {
			if restoreErr := renameDir(backupDir, storeDir); restoreErr != nil {
				return fmt.Errorf("replace store dir: %w (restore old store dir: %v)", err, restoreErr)
			}
		}
		return fmt.Errorf("replace store dir: %w", err)
	}

	if err := os.RemoveAll(backupDir); err != nil {
		return fmt.Errorf("remove store dir backup: %w", err)
	}
	return nil
}

// ResolveDirectDeps returns the (name, version, revision)
// tuple for every declared runtime dep of r (deduped, after
// platform overlay). Does NOT install anything — the
// binary-install path uses this to populate .gale-deps.toml
// with the runtime closure the shipped binary links.
// Build-only deps are excluded (gh#157): recording them
// would pin build tools in the store for gc even though
// the binary never links them, and IsStale ignores them.
func (inst *Installer) ResolveDirectDeps(ctx context.Context, r *recipe.Recipe) ([]depsmeta.ResolvedDep, error) {
	if !inst.canResolve() {
		return nil, nil
	}
	deps := r.DependenciesForPlatform(runtime.GOOS, runtime.GOARCH)
	names := make([]string, 0, len(deps.Runtime))
	seen := make(map[string]bool)
	for _, d := range deps.Runtime {
		if !seen[d] {
			seen[d] = true
			names = append(names, d)
		}
	}
	resolved := make([]depsmeta.ResolvedDep, 0, len(names))
	for _, name := range names {
		dr, err := inst.resolveDep(ctx, name)
		if err != nil {
			return nil, fmt.Errorf(
				"resolve dep %q: %w", name, err,
			)
		}
		if dr == nil {
			return nil, fmt.Errorf(
				"no recipe found for dependency %q", name,
			)
		}
		resolved = append(resolved, depsmeta.ResolvedDep{
			Name:     name,
			Version:  dr.Package.Version,
			Revision: dr.Package.Revision,
		})
	}
	return resolved, nil
}

// lockPackage acquires an exclusive file lock for a package
// version. Returns an unlock function that releases the lock.
// The lock file is kept on disk so all contenders share the
// same inode — removing it would cause a race where a new
// arrival creates a separate file and acquires its own lock.
func lockPackage(storeRoot, name, version string) (func(), error) {
	lockPath := filepath.Join(storeRoot, name, version+".lock")
	return filelock.Acquire(lockPath)
}

// storeGenLockPath returns the path to the generation-build
// lock for the given store root. H7: a concurrent `gale sync`
// calls generation.Build, which locks this same path (via
// filepath.Dir(storeRoot)/generation.lock) to serialize gen
// rebuilds. The installer acquires this lock around its store-
// write critical section so a sync cannot walk a half-extracted
// package.
//
// Path semantics: generation.Build always acquires the lock at
// filepath.Dir(storeRoot)/generation.lock regardless of the
// galeDir argument. Since the store is always global, both the
// installer and project-scoped Build calls converge on the same
// file (~/.gale/generation.lock). The install-vs-project-sync
// race is fully closed: a project gale sync serializes against
// a concurrent global install.
func storeGenLockPath(storeRoot string) string {
	return filepath.Join(
		filepath.Dir(storeRoot), "generation.lock",
	)
}

// withStoreGenLock runs fn while holding the store-gen lock
// (see storeGenLockPath). The lock file is created under
// filepath.Dir(storeRoot); callers must ensure that parent
// directory exists — the Store is rooted inside it, so in
// practice this always holds.
func withStoreGenLock(storeRoot string, fn func() error) error {
	return filelock.With(storeGenLockPath(storeRoot), fn)
}

// InstallWithFinalize acquires the per-package lock, runs the install,
// then invokes finalize() while still holding the lock, then releases.
// finalize == nil is a no-op. finalize errors are returned alongside
// the InstallResult so the caller sees partial state.
func (inst *Installer) InstallWithFinalize(ctx context.Context, r *recipe.Recipe, force bool, finalize func(*InstallResult) error) (*InstallResult, error) {
	unlock, err := lockPackage(inst.Store.Root, r.Package.Name, r.Package.Full())
	if err != nil {
		return nil, fmt.Errorf("lock package: %w", err)
	}
	defer unlock()

	result, err := inst.installLocked(ctx, r, force)
	if err != nil {
		return nil, err
	}

	if finalize != nil {
		if err := finalize(result); err != nil {
			return result, fmt.Errorf("finalize: %w", err)
		}
	}

	return result, nil
}
