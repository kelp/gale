package installer

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kelp/gale/internal/attestation"
	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/filelock"
	"github.com/kelp/gale/internal/ghcr"
	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/lockplan"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
	"github.com/kelp/gale/internal/timing"
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
	if dest.staged {
		defer os.RemoveAll(dest.storeDir)
	}
	got, err := inst.populateStore(ctx, dest, r)
	if err != nil {
		return nil, err
	}
	if dest.staged {
		// The result is built before the commit rather than after,
		// because the guard inside commitStaged decides on these
		// exact values and a refusal must be able to name them.
		rep := Replacement{
			CanonicalDir: dest.canonicalDir,
			StagingDir:   dest.storeDir,
			Result:       got,
		}
		if err := inst.commitStaged(
			inst.Store.Root, dest.storeDir, rep,
		); err != nil {
			return nil, fmt.Errorf("install staged output: %w", err)
		}
	}
	return &got, nil
}

// populateStore writes the package into dest.storeDir (binary
// first when that path is viable, source otherwise). The
// caller commits a staged dest after this returns.
func (inst *Installer) populateStore(ctx context.Context, dest installDest, r *recipe.Recipe) (InstallResult, error) {
	bin := r.BinaryForPlatform(runtime.GOOS, runtime.GOARCH)
	binaryViable := bin != nil && !inst.SourceOnly
	// The locked method is binding in both directions. Plan
	// construction already proved the recipe can serve it
	// (lockplan.validateMethod), so this selects rather than
	// checks: SourceOnly, which is a caller preference, cannot
	// override a locked binary node either.
	if dest.locked {
		binaryViable = dest.planned.Method == lockgraph.MethodBinary
	}
	if !binaryViable {
		os.RemoveAll(dest.storeDir)
		return InstallResult{}, fmt.Errorf(
			"%s: %w",
			lockgraph.Key(dest.name, dest.storeVersion), ErrSourceGone,
		)
	}

	got, berr := inst.tryBinaryInstall(ctx, binaryAttempt{
		r: r, storeDir: dest.storeDir, canonicalDir: dest.canonicalDir,
		staged: dest.staged, locked: dest.locked, storeVersion: dest.storeVersion,
		bin: bin,
	})
	if berr != nil {
		return InstallResult{}, berr
	}
	method := got.method
	sha256 := got.sha
	manifestDigest := got.digest
	if method != MethodBinary {
		os.RemoveAll(dest.storeDir)
		return InstallResult{}, fmt.Errorf(
			"%s: %w",
			lockgraph.Key(dest.name, dest.storeVersion), ErrSourceGone,
		)
	}
	return InstallResult{
		Name:           dest.name,
		Version:        dest.version,
		Method:         method,
		SHA256:         sha256,
		ManifestDigest: manifestDigest,
	}, nil
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

// installBinaryTo fetches and finalizes a prebuilt archive
// into extractDir. When inPlace is true, extractDir IS the
// canonical store dir: the function acquires the store-gen
// lock and renames. When inPlace is false, the caller is
// staging into a sibling dir and will commit the rename
// itself.
//
// The fetch streams directly into a sibling staging directory
// (no on-disk .tar.zst intermediate). The staging dir is
// renamed into extractDir inside the store-gen lock so a
// concurrent generation.Build sees either the pre-install
// or the completed install — never an intermediate.
type binaryAttempt struct {
	r            *recipe.Recipe
	storeDir     string
	canonicalDir string
	staged       bool
	locked       bool
	storeVersion string
	bin          *recipe.Binary
}

type binaryResult struct {
	method InstallMethod
	sha    string
	digest string
}

func (inst *Installer) tryBinaryInstall(ctx context.Context, a binaryAttempt) (binaryResult, error) {
	// Resolve the declared runtime closure so metadata
	// records the deps the shipped binary can link.
	// Build-only tools are deliberately excluded
	// (gh#157): recording them would pin those tools in
	// the store for gc even though the binary never
	// links them.
	fallback, ferr := inst.ResolveDirectDeps(ctx, a.r)
	if ferr != nil {
		os.RemoveAll(a.storeDir)
		return binaryResult{}, fmt.Errorf(
			"resolve deps for metadata: %w", ferr,
		)
	}
	berr := inst.installBinaryTo(ctx, a.r, extractDest{a.storeDir, a.canonicalDir, !a.staged}, fallback)
	switch {
	case berr == nil:
		return binaryResult{
			method: MethodBinary,
			sha:    a.bin.SHA256,
			digest: a.bin.ManifestDigest,
		}, nil
	case a.locked || inst.BinaryOnly:
		// A locked binary node never demotes to a source build
		// (acceptance 8): the method is a locked field, so a
		// source build would install bytes the lock never named
		// and fail its own hash check minutes later. Leave
		// nothing behind — the mismatching artifact must be
		// absent from the store (acceptance 1).
		os.RemoveAll(a.storeDir)
		// A hash disagreement is an integrity violation, and design
		// §8 puts it in the class that stops a pipeline for a human.
		// Every other reason a fetch can fail — a 404, a refused
		// connection, a corrupt archive — stays ordinary, because
		// acceptance 11 turns on exactly that distinction: an
		// ordinary failure under a lock must not read as tampering.
		if errors.Is(berr, download.ErrSHA256Mismatch) {
			return binaryResult{}, fmt.Errorf(
				"%s of %s: %w: %w", refusalLabel(a.locked),
				lockgraph.Key(a.r.Package.Name, a.storeVersion),
				provenance.ErrInvalid, berr,
			)
		}
		return binaryResult{}, fmt.Errorf(
			"%s of %s: %w", refusalLabel(a.locked),
			lockgraph.Key(a.r.Package.Name, a.storeVersion), berr,
		)
	default:
		os.RemoveAll(a.storeDir)
		return binaryResult{}, fmt.Errorf(
			"%w: binary install of %s failed: %w",
			ErrSourceGone,
			lockgraph.Key(a.r.Package.Name, a.storeVersion),
			berr,
		)
	}
}

// extractDest is the on-disk target of a binary or source extract.
type extractDest struct {
	dir, canonical string
	inPlace        bool
}

func (inst *Installer) installBinaryTo(
	ctx context.Context,
	r *recipe.Recipe,
	dest extractDest,
	depsFallback []depsmeta.ResolvedDep,
) error {
	bin := r.BinaryForPlatform(runtime.GOOS, runtime.GOARCH)
	name := r.Package.Name
	version := r.Package.Version
	v := inst.Verifier

	// Enforce the recipe's declared trust policy before
	// fetching anything. A recipe that ships a non-GHCR
	// URL with the default (sigstore) policy is rejected
	// here: we can't produce an attestation for an
	// arbitrary third-party host, and silently skipping
	// attestation for non-GHCR URLs was the C3 bypass.
	if err := bin.CheckTrustPolicy(); err != nil {
		return err
	}

	pkgID := name + "@" + version

	// Resolve bearer token for GHCR URLs; empty string for
	// non-GHCR (FetchAndExtractTarZstd omits the header).
	var token string
	if recipe.IsGHCR(bin.URL) {
		repo := repoFromURL(bin.URL)
		var err error
		token, err = ghcr.Token(ctx, repo)
		if err != nil {
			return fmt.Errorf("ghcr auth: %w", err)
		}
	}

	// Digest-based fetch (gh#121): when the recipe carries a
	// manifest digest, confirm the immutable OCI manifest
	// references exactly the layer the ledger's sha256 names
	// before pulling it. Fail-closed — any failure aborts the
	// binary install so the caller falls back to a source build
	// rather than trusting an unverifiable artifact.
	if err := verifyManifestDigest(ctx, bin, token); err != nil {
		return fmt.Errorf("verify manifest digest: %w", err)
	}

	// Stream fetch + SHA verification + extraction in one
	// pass into a sibling staging directory. The network
	// fetch stays outside the store-gen lock so a slow
	// download does not block concurrent sync operations.
	extractDir, finalStoreDir, inPlace := dest.dir, dest.canonical, dest.inPlace
	stagingDir := extractDir + ".stream"
	defer os.RemoveAll(stagingDir) // clean up on any exit path

	// A previous crashed install of this package may have left a
	// stale staging dir on disk. FetchAndExtractTarZstd's MkdirAll
	// is idempotent and additive — it would extract on top of the
	// stale state, leaving partial files alive after rename. Wipe
	// the staging dir before fresh extraction.
	if err := os.RemoveAll(stagingDir); err != nil {
		return fmt.Errorf("clean staging dir: %w", err)
	}

	// When sigstore attestation is needed, tee the raw archive
	// bytes to a unique tempfile. The file-fallback verification
	// path hashes the subject as a file, so we verify the teed
	// copy of the downloaded archive (the exact bytes the
	// attestation covers) and delete it after via defer. Binary
	// os.CreateTemp guarantees a unique tempfile.
	needAttest, archiveOut, err := setupAttestTempfile(bin, v)
	if err != nil {
		return err
	}
	if archiveOut != "" {
		defer os.Remove(archiveOut)
	}

	fetchErr := func() error {
		streamDone := timing.Phase("binary-stream " + pkgID)
		defer streamDone()
		_, err := download.FetchAndExtractTarZstdWithArchive(ctx, download.FetchExtract{
			URL: bin.URL, DestDir: stagingDir, ExpectedSHA256: bin.SHA256,
			Token: token, ArchiveOut: archiveOut,
		})
		return err
	}()
	if fetchErr != nil {
		return fmt.Errorf("fetch binary: %w", fetchErr)
	}

	// Attestation verification fires whenever the recipe's
	// trust policy requires it (sigstore — the default) and a
	// Verifier is wired up. A non-nil verifier ALWAYS verifies
	// and a failure aborts the binary path (the caller falls
	// back to a source build) — fail closed, never a silent
	// skip (gh#129). nil is a test-only seam meaning "skip
	// attestation"; production wiring never passes nil. The
	// explicit trust-policy check above closes the C3 bypass
	// where a non-GHCR URL dodged this step entirely; here we
	// only verify when the recipe opted in to sigstore (which
	// by definition means GHCR).
	if needAttest {
		attestDone := timing.Phase("attestation " + pkgID)
		err := verifyPrebuiltAttestation(ctx, bin, archiveOut, token, v)
		attestDone()
		if err != nil {
			return fmt.Errorf("attestation: %w", err)
		}
	}

	// Run the whole fixup pipeline in the staging dir BEFORE
	// the rename, so the canonical store dir only ever appears
	// fully finalized (gh#41). A crash or error anywhere in
	// the pipeline leaves only the transient ".stream" staging
	// dir, which IsInstalled and the generation resolver
	// already skip — a retry starts clean instead of trusting
	// a broken-but-non-empty dir forever. Every fixup writes
	// final paths (finalStoreDir / storeRoot), never staging
	// paths, so the content is correct after the rename.
	storeRoot := filepath.Dir(filepath.Dir(finalStoreDir))

	// Record the dep closure the prebuilt expects at
	// runtime so staleness can be detected when a dep's
	// recipe changes. If the archive already shipped a
	// .gale-deps.toml (built by `gale build` with full
	// knowledge of the linked versions), keep that —
	// it's the authoritative record. Otherwise write our
	// locally-resolved closure, which is approximate but
	// preserves backwards-compat with archives built
	// before the build-time emit landed.
	//
	// The write happens even when depsFallback is empty:
	// a zero-dep recipe must still record an empty file
	// so doctor's "missing metadata = legacy install of
	// unknown deps" heuristic doesn't flag a fresh
	// install as stale. The legacy-stale path is
	// preserved for installs that genuinely predate this
	// metadata (no file on disk at all).
	present, err := stagedDepsPresent(stagingDir)
	if err != nil {
		return err
	}
	if !present {
		md := depsmeta.Metadata{Deps: depsFallback}
		if err := depsmeta.Write(stagingDir, md); err != nil {
			return fmt.Errorf("write deps metadata: %w", err)
		}
	}

	if err := recordRecipeDigest(stagingDir, r.Digest); err != nil {
		return fmt.Errorf("write recipe metadata: %w", err)
	}

	// Record what this install verified, beside the metadata and
	// before the commit rename, so the canonical dir never appears
	// without its provenance. Identity is the recipe's canonical
	// version-revision, never the store dir's basename.
	if err := recordProvenance(storeRoot, stagingDir, commitArtifact{
		Name:           name,
		Version:        r.Package.Full(),
		Method:         lockgraph.MethodBinary,
		SHA256:         bin.SHA256,
		ManifestDigest: bin.ManifestDigest,
	}); err != nil {
		return err
	}

	// Commit: rename the fully-finalized staging dir into the
	// canonical extract dir. The
	// fetch + verify + fixups above intentionally stay outside
	// the store-gen lock: they don't touch the canonical store
	// dir, and a network stall must not block a concurrent sync.
	return commitExtracted(commitRequest{
		StagingDir:    stagingDir,
		ExtractDir:    extractDir,
		FinalStoreDir: finalStoreDir,
		StoreRoot:     storeRoot,
		InPlace:       inPlace,
	})
}

// setupAttestTempfile creates a collision-free tempfile for tee-ing
// the raw download archive when sigstore attestation is required.
// Returns (needAttest, tempfilePath, error). When attestation is not
// needed, tempfilePath is empty and the caller must not defer-remove
// it. The caller is responsible for defer os.Remove(tempfilePath).
func setupAttestTempfile(bin *recipe.Binary, v attestation.Verifier) (bool, string, error) {
	if bin.EffectiveTrust() != recipe.TrustSigstore || v == nil {
		return false, "", nil
	}
	scratch, err := store.TmpDir()
	if err != nil {
		return false, "", fmt.Errorf("temp dir: %w", err)
	}
	af, err := os.CreateTemp(scratch, "gale-verify-*.tar.zst")
	if err != nil {
		return false, "", fmt.Errorf("create attestation tempfile: %w", err)
	}
	path := af.Name()
	af.Close()
	return true, path, nil
}

func commitExtracted(req commitRequest) error {
	swap := func() error {
		if err := os.RemoveAll(req.ExtractDir); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove empty extract dir: %w", err)
		}
		if err := renameDir(req.StagingDir, req.ExtractDir); err != nil {
			return fmt.Errorf("promote staging dir: %w", err)
		}
		return nil
	}
	if req.InPlace {
		return withStoreGenLock(req.StoreRoot, swap)
	}
	return swap()
}

type commitRequest struct {
	StagingDir    string
	ExtractDir    string
	FinalStoreDir string
	StoreRoot     string
	InPlace       bool
}

// verifyManifestDigest enforces digest-based fetch (gh#121). When a
// binary carries a manifest digest, gale pulls the OCI manifest by
// that digest and confirms it references exactly the layer the
// ledger's sha256 names — the manifest is the immutable, attested
// handle, and the sha256 is the cross-check second factor. Returns
// nil when no digest is declared (legacy recipes fetch the blob
// directly). All failures propagate so the binary install aborts
// to a source-build fallback.
func verifyManifestDigest(ctx context.Context, bin *recipe.Binary, token string) error {
	if bin.ManifestDigest == "" {
		return nil
	}
	manifestURL, err := ghcr.ManifestURLForBlob(bin.URL, bin.ManifestDigest)
	if err != nil {
		// Not a GHCR blob URL, so there is no OCI manifest to
		// verify. A manifest digest only rides on ledger-sourced
		// GHCR binaries (always /blobs/ URLs); on any other URL it
		// is inert metadata — sigstore+non-GHCR is already
		// rejected, and sha256-only verifies the blob bytes
		// directly. Nothing to check.
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	layerDigest, err := ghcr.FetchManifestLayer(ctx, manifestURL, bin.ManifestDigest, token)
	if err != nil {
		return err
	}
	got := strings.TrimPrefix(layerDigest, "sha256:")
	if !strings.EqualFold(got, bin.SHA256) {
		return fmt.Errorf(
			"manifest layer %s does not match ledger sha256 %s",
			layerDigest, bin.SHA256,
		)
	}
	return nil
}

// repoFromURL extracts the repository path from a GHCR blob
// URL like "https://ghcr.io/v2/owner/repo/name/blobs/sha256:...".
// Returns "owner/repo/name".
func repoFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	// Path: /v2/owner/repo/name/blobs/sha256:...
	// Strip "/v2/" prefix and "/blobs/..." suffix.
	p := strings.TrimPrefix(u.Path, "/v2/")
	if idx := strings.Index(p, "/blobs/"); idx != -1 {
		p = p[:idx]
	}
	return p
}

// fetchReferrerBundle is the seam over ghcr.FetchReferrerBundle so
// tests can stay hermetic (no real GHCR referrers API call).
var fetchReferrerBundle = ghcr.FetchReferrerBundle

// verifyPrebuiltAttestation routes a prebuilt binary's attestation
// check through attestation.VerifyPrebuilt: the tokenless OCI-referrer
// path first, falling back to the teed archive file only when no
// referrer exists. archiveOut is the teed copy of the downloaded
// archive bytes the file path verifies.
func verifyPrebuiltAttestation(ctx context.Context, bin *recipe.Binary, archiveOut, token string, v attestation.Verifier) error {
	return attestation.VerifyPrebuilt(v, attestation.PrebuiltParams{
		Repo:           attestation.DefaultRepo,
		ManifestDigest: bin.ManifestDigest,
		FetchBundle: func() ([]byte, error) {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			return fetchReferrerBundle(
				ctx, bin.URL, bin.ManifestDigest, token,
			)
		},
		Archive: func() (string, func(), error) {
			return archiveOut, nil, nil
		},
	})
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
