package installer

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/kelp/gale/internal/attestation"
	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/filelock"
	"github.com/kelp/gale/internal/ghcr"
	"github.com/kelp/gale/internal/parallel"
	"github.com/kelp/gale/internal/prewarm"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
	"github.com/kelp/gale/internal/timing"
)

var renameDir = os.Rename

// RecipeResolver finds and parses a recipe by package name.
// Returns nil if the package has no recipe.
type RecipeResolver func(name string) (*recipe.Recipe, error)

// Installer installs packages into the store.
type Installer struct {
	Store      *store.Store
	Resolver   RecipeResolver
	Verifier   attestation.Verifier // nil = skip attestation
	SourceOnly bool                 // skip binary, build from source

	// BinaryFallbackLog receives a one-line warning when a
	// binary install fails and the installer falls back to a
	// source build. nil means write to os.Stderr — the failure
	// is always reported because reaching this branch means a
	// binary was advertised in the recipe and could not be
	// fetched/verified. Tests inject a buffer to assert on
	// the message. Because dep installs run in parallel, this
	// writer may receive concurrent writes; callers injecting a
	// non-os.Stderr writer must make it concurrency-safe.
	BinaryFallbackLog io.Writer

	// Downloads bounds the number of concurrent binary network
	// fetches across all installs sharing this Installer. A nil
	// limiter (the zero value) is unbounded.
	Downloads *parallel.Limiter
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

// Install installs a recipe into the store and links binaries.
func (inst *Installer) Install(r *recipe.Recipe) (*InstallResult, error) {
	return inst.install(r, false)
}

// Reinstall is Install but skips the IsInstalled cache check so
// callers can force a fresh install even when the store already
// satisfies the request. Used by sync's stale-reinstall path to
// migrate pre-revision bare-dir installs into the canonical layout.
func (inst *Installer) Reinstall(r *recipe.Recipe) (*InstallResult, error) {
	return inst.install(r, true)
}

func (inst *Installer) install(r *recipe.Recipe, force bool) (*InstallResult, error) {
	unlock, err := lockPackage(inst.Store.Root, r.Package.Name, r.Package.Full())
	if err != nil {
		return nil, fmt.Errorf("lock package: %w", err)
	}
	defer unlock()
	return inst.installLocked(r, force)
}

// installLocked is the body of install assuming the per-package
// lock is held by the caller. Used by install() and by
// InstallWithFinalize (added in a follow-up commit).
func (inst *Installer) installLocked(r *recipe.Recipe, force bool) (*InstallResult, error) {
	name := r.Package.Name
	version := r.Package.Version
	// Store paths use the full <version>-<revision> form so
	// multiple revisions of the same version can coexist.
	// The Store layer falls back from "<v>-1" to bare "<v>"
	// for back-compat with pre-revision installs.
	storeVersion := r.Package.Full()

	canonicalDir := filepath.Join(inst.Store.Root, name, storeVersion)
	var storeDir string
	staged := false

	// Cache check. The default path accepts IsInstalled's
	// back-compat fallback (bare pre-revision dirs count as
	// "installed"), so dep installs don't needlessly
	// re-migrate every package.
	//
	// The forced path (Reinstall) always rebuilds into a
	// sibling staging dir first. The live canonical dir stays
	// intact until the final replace succeeds, so a failed
	// stale reinstall does not break the active generation.
	if !force && inst.Store.IsInstalled(name, storeVersion) {
		return &InstallResult{
			Name:    name,
			Version: version,
			Method:  MethodCached,
		}, nil
	}

	if force {
		pkgDir := filepath.Join(inst.Store.Root, name)
		if err := os.MkdirAll(pkgDir, 0o755); err != nil {
			return nil, fmt.Errorf("create package dir: %w", err)
		}
		buildDir, err := os.MkdirTemp(pkgDir, ".build-")
		if err != nil {
			return nil, fmt.Errorf("create reinstall staging dir: %w", err)
		}
		storeDir = buildDir
		staged = true
		defer os.RemoveAll(buildDir)
	} else {
		// Create store directory.
		var err error
		storeDir, err = inst.Store.Create(name, storeVersion)
		if err != nil {
			return nil, fmt.Errorf("create store dir: %w", err)
		}
	}

	method := MethodSource
	var sha256 string
	var manifestDigest string

	bin := r.BinaryForPlatform(runtime.GOOS, runtime.GOARCH)
	binaryViable := bin != nil && !inst.SourceOnly

	// Install runtime deps up front. The binary path needs
	// them on disk (the prebuilt links against them); the
	// source path needs them too. Build-only deps are
	// deferred until we know we actually have to build from
	// source — a successful binary install avoids them
	// entirely.
	//
	// When there's no binary path to try (source-only mode,
	// or no platform binary), install everything in one
	// shot — matches the pre-revision behavior and keeps a
	// single failure surface for source-only installs.
	var depPaths *build.BuildDeps
	var err error
	if binaryViable {
		depPaths, err = inst.InstallRuntimeDeps(r)
	} else {
		if len(r.Dependencies.Build) > 0 {
			prewarm.PrewarmRecipeDeps(context.Background(), r.Dependencies.Build, inst.Resolver)
		}
		depPaths, err = inst.InstallBuildDeps(r)
	}
	if err != nil {
		os.RemoveAll(storeDir)
		return nil, fmt.Errorf("install deps: %w", err)
	}

	// Try binary first (unless source-only mode).
	if binaryViable {
		// Resolve the full declared closure so metadata
		// records every direct dep — even the build-only
		// ones we just skipped installing. IsStale needs
		// this to flag stale installs when a build dep's
		// recipe bumps revision.
		fallback, ferr := inst.ResolveDirectDeps(r)
		if ferr != nil {
			os.RemoveAll(storeDir)
			return nil, fmt.Errorf(
				"resolve deps for metadata: %w", ferr,
			)
		}
		if berr := installBinaryTo(bin, storeDir, canonicalDir, name, version, fallback, inst.Verifier, !staged, inst.Downloads); berr == nil {
			method = MethodBinary
			sha256 = bin.SHA256
			manifestDigest = bin.ManifestDigest
		} else {
			// Binary install failed — fall back to source build.
			// Reaching here means the recipe advertised a binary
			// for this platform and the fetch/verify pipeline
			// rejected it. Surface the reason so a silent source
			// build doesn't hide network errors, missing GHCR
			// artifacts, hash mismatches, or attestation failures.
			w := inst.BinaryFallbackLog
			if w == nil {
				w = os.Stderr
			}
			fmt.Fprintf(w,
				"warning: binary install for %s@%s failed: %v;"+
					" falling back to source build\n",
				name, version, berr)
			if err := os.RemoveAll(storeDir); err != nil {
				return nil, fmt.Errorf(
					"clean store dir for source fallback: %w", err,
				)
			}
			if err := os.MkdirAll(storeDir, 0o755); err != nil {
				return nil, fmt.Errorf(
					"recreate store dir for source fallback: %w", err,
				)
			}
			// Top up with the build-only deps we skipped.
			buildOnly, derr := inst.InstallBuildOnlyDeps(r)
			if derr != nil {
				os.RemoveAll(storeDir)
				return nil, fmt.Errorf(
					"install build deps for source fallback: %w", derr,
				)
			}
			depPaths = mergeBuildDeps(depPaths, buildOnly)
		}
	}

	if method != MethodBinary {
		hash, buildErr := installFromSourceTo(r, storeDir, canonicalDir, depPaths, !staged)
		if buildErr != nil {
			// Clean up failed install.
			os.RemoveAll(storeDir)
			return nil, fmt.Errorf("build from source: %w", buildErr)
		}
		sha256 = hash
	}

	if staged {
		if err := commitStaged(inst.Store.Root, canonicalDir, storeDir); err != nil {
			return nil, fmt.Errorf("install staged output: %w", err)
		}
	}

	return &InstallResult{
		Name:           name,
		Version:        version,
		Method:         method,
		SHA256:         sha256,
		ManifestDigest: manifestDigest,
	}, nil
}

// InstallLocal installs a recipe from a local source directory.
// Skips binary install and downloads — builds directly from
// sourceDir using build.BuildLocal. Always rebuilds even if
// the version exists in the store, since local source may
// have changed without a version bump.
func (inst *Installer) InstallLocal(r *recipe.Recipe, sourceDir string) (*InstallResult, error) {
	unlock, err := lockPackage(inst.Store.Root, r.Package.Name, r.Package.Full())
	if err != nil {
		return nil, fmt.Errorf("lock package: %w", err)
	}
	defer unlock()
	return inst.installLocalLocked(r, sourceDir)
}

// installLocalLocked is the body of InstallLocal assuming the
// per-package lock is held by the caller. Used by InstallLocal
// and InstallLocalWithFinalize.
func (inst *Installer) installLocalLocked(r *recipe.Recipe, sourceDir string) (*InstallResult, error) {
	name := r.Package.Name
	version := r.Package.Version
	// Store paths use <version>-<revision> so revisions of
	// the same base version don't collide. Back-compat in
	// the Store layer resolves "<v>-1" to a bare "<v>" dir
	// when one exists from a pre-revision install.
	storeVersion := r.Package.Full()

	// Build into a temp dir inside <storeRoot>/<name>/ so
	// the existing store entry and its active symlinks stay
	// intact until the build succeeds. Same-filesystem
	// rename is guaranteed since both paths are under the
	// same parent.
	storeDir := filepath.Join(inst.Store.Root, name, storeVersion)
	pkgDir := filepath.Join(inst.Store.Root, name)
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		return nil, fmt.Errorf("create package dir: %w", err)
	}

	buildDir, err := os.MkdirTemp(pkgDir, ".build-")
	if err != nil {
		return nil, fmt.Errorf("create build dir: %w", err)
	}
	defer os.RemoveAll(buildDir) // clean up on any exit path

	// Resolve and install build deps.
	if len(r.Dependencies.Build) > 0 {
		prewarm.PrewarmRecipeDeps(context.Background(), r.Dependencies.Build, inst.Resolver)
	}
	depPaths, err := inst.InstallBuildDeps(r)
	if err != nil {
		return nil, fmt.Errorf("install build deps: %w", err)
	}

	hash, buildErr := installFromLocalSource(r, sourceDir, buildDir, depPaths)
	if buildErr != nil {
		return nil, fmt.Errorf("build from local source: %w", buildErr)
	}

	// Build succeeded — swap into place atomically.
	// The per-package lock (above) ensures no other installer
	// of this package races; the store-gen lock here serializes
	// with generation.Build so a concurrent gen rebuild sees
	// either the pre-rename or post-rename tree, not a mix.
	if err := withStoreGenLock(inst.Store.Root, func() error {
		return replaceStoreDir(storeDir, buildDir)
	}); err != nil {
		return nil, fmt.Errorf("install build output: %w", err)
	}

	return &InstallResult{
		Name:    name,
		Version: version,
		Method:  MethodSource,
		SHA256:  hash,
	}, nil
}

// InstallLocalWithFinalize acquires the per-package lock, runs the
// local source install, then invokes finalize() while still holding
// the lock, then releases. finalize == nil is a no-op. finalize
// errors are returned alongside the InstallResult so the caller
// sees partial state.
func (inst *Installer) InstallLocalWithFinalize(
	r *recipe.Recipe, sourceDir string,
	finalize func(*InstallResult) error,
) (*InstallResult, error) {
	unlock, err := lockPackage(inst.Store.Root, r.Package.Name, r.Package.Full())
	if err != nil {
		return nil, fmt.Errorf("lock package: %w", err)
	}
	defer unlock()

	result, err := inst.installLocalLocked(r, sourceDir)
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

// installGitPrepare runs the pre-lock phase of a git install:
// installs build deps, creates a tmp dir, and calls build.BuildGit.
// Returns the build result, commit hash, dep paths, a cleanup
// function, and any error. cleanup is always non-nil — callers must
// unconditionally defer it immediately after the call, before
// checking err. On early-error paths (before MkdirTemp), cleanup is
// a no-op; on later errors it removes the tmp dir.
func (inst *Installer) installGitPrepare(r *recipe.Recipe) (*build.BuildResult, string, *build.BuildDeps, func(), error) {
	noop := func() {}

	// Resolve and install build deps.
	if len(r.Dependencies.Build) > 0 {
		prewarm.PrewarmRecipeDeps(context.Background(), r.Dependencies.Build, inst.Resolver)
	}
	depPaths, err := inst.InstallBuildDeps(r)
	if err != nil {
		return nil, "", nil, noop, fmt.Errorf("install build deps: %w", err)
	}

	// Build from git — returns hash as version.
	tmpDir, err := os.MkdirTemp(build.TmpDir(), "gale-install-*")
	if err != nil {
		return nil, "", nil, noop, fmt.Errorf("create temp dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(tmpDir) }

	buildResult, hash, err := build.BuildGit(r, tmpDir, r.Build.Debug, depPaths)
	if err != nil {
		return nil, "", nil, cleanup, fmt.Errorf("git build: %w", err)
	}
	return buildResult, hash, depPaths, cleanup, nil
}

// installGitLocked performs the post-build store-mutation phase of a
// git install. Assumes the per-package lock for (r.Package.Name, hash)
// is held by the caller.
func (inst *Installer) installGitLocked(r *recipe.Recipe, buildResult *build.BuildResult, hash string, depPaths *build.BuildDeps) (*InstallResult, error) {
	name := r.Package.Name

	// Skip if this hash is already installed.
	if inst.Store.IsInstalled(name, hash) {
		return &InstallResult{
			Name:    name,
			Version: hash,
			Method:  MethodCached,
		}, nil
	}

	// Create store dir and extract.
	storeDir, err := inst.Store.Create(name, hash)
	if err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}

	if err := extractBuild(buildResult, storeDir, depPaths); err != nil {
		os.RemoveAll(storeDir)
		return nil, fmt.Errorf("extracting git build: %w", err)
	}

	return &InstallResult{
		Name:    name,
		Version: hash,
		Method:  MethodSource,
		SHA256:  buildResult.SHA256,
	}, nil
}

// InstallGit clones a git repo and builds from the clone.
// Returns the install result with the commit hash as version.
func (inst *Installer) InstallGit(r *recipe.Recipe) (*InstallResult, error) {
	buildResult, hash, depPaths, cleanup, err := inst.installGitPrepare(r)
	defer cleanup()
	if err != nil {
		return nil, err
	}

	unlock, err := lockPackage(inst.Store.Root, r.Package.Name, hash)
	if err != nil {
		return nil, fmt.Errorf("lock package: %w", err)
	}
	defer unlock()
	return inst.installGitLocked(r, buildResult, hash, depPaths)
}

// InstallGitWithFinalize acquires the per-package lock (keyed on the
// resolved commit hash), runs the git install, then invokes finalize()
// while still holding the lock, then releases. finalize == nil is a
// no-op. finalize errors are returned alongside the InstallResult so
// the caller sees partial state.
func (inst *Installer) InstallGitWithFinalize(r *recipe.Recipe, finalize func(*InstallResult) error) (*InstallResult, error) {
	buildResult, hash, depPaths, cleanup, err := inst.installGitPrepare(r)
	defer cleanup()
	if err != nil {
		return nil, err
	}

	unlock, err := lockPackage(inst.Store.Root, r.Package.Name, hash)
	if err != nil {
		return nil, fmt.Errorf("lock package: %w", err)
	}
	defer unlock()

	result, err := inst.installGitLocked(r, buildResult, hash, depPaths)
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

// commitStaged finishes a staged reinstall by renaming the
// staging dir into the canonical store path and repopulating
// the shared farm. Both steps happen under the store-gen
// lock so a concurrent generation rebuild sees either the
// pre-install or completed install — never an intermediate.
//
// If the rename fails, replaceStoreDir restores the prior
// canonical dir from its .bak sibling. If farm.Populate
// fails after a successful rename, the canonical dir is
// already the new version; the caller will see the error
// and the farm will be repopulated on the next sync.
func commitStaged(storeRoot, canonicalDir, stagingDir string) error {
	return withStoreGenLock(storeRoot, func() error {
		if err := replaceStoreDir(canonicalDir, stagingDir); err != nil {
			return err
		}
		if farmDir := farm.DirFromStoreDir(canonicalDir); farmDir != "" {
			if err := farm.Populate(canonicalDir, farmDir); err != nil {
				return fmt.Errorf("populate farm: %w", err)
			}
		}
		return nil
	})
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
// lock and populates the farm. When inPlace is false, the
// caller is staging into a sibling dir and will commit the
// rename + farm.Populate itself.
//
// The fetch streams directly into a sibling staging directory
// (no on-disk .tar.zst intermediate). The staging dir is
// renamed into extractDir inside the store-gen lock so a
// concurrent generation.Build sees either the pre-install
// or the completed install — never an intermediate.
func installBinaryTo(bin *recipe.Binary, extractDir, finalStoreDir, name, version string, depsFallback []ResolvedDep, v attestation.Verifier, inPlace bool, dl *parallel.Limiter) error {
	// Enforce the recipe's declared trust policy before
	// fetching anything. A recipe that ships a non-GHCR
	// URL with the default (sigstore) policy is rejected
	// here: we can't produce an attestation for an
	// arbitrary third-party host, and silently skipping
	// attestation for non-GHCR URLs was the C3 bypass.
	if err := checkBinaryTrustPolicy(bin); err != nil {
		return err
	}

	pkgID := name + "@" + version

	// Resolve bearer token for GHCR URLs; empty string for
	// non-GHCR (FetchAndExtractTarZstd omits the header).
	var token string
	if isGHCR(bin.URL) {
		repo := repoFromURL(bin.URL)
		var err error
		token, err = ghcr.Token(repo)
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
	if err := verifyManifestDigest(bin, token); err != nil {
		return fmt.Errorf("verify manifest digest: %w", err)
	}

	// Stream fetch + SHA verification + extraction in one
	// pass into a sibling staging directory. The network
	// fetch stays outside the store-gen lock so a slow
	// download does not block concurrent sync operations.
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
	// bytes to a unique tempfile. gh attestation verify computes
	// the digest by reading the subject as a file, so we verify
	// the teed copy of the downloaded archive (the exact bytes the
	// attestation covers) and delete it after via defer. Binary
	// installs run 8-way parallel, so the tempfile must be
	// collision-free (os.CreateTemp guarantees this).
	needAttest := bin.EffectiveTrust() == recipe.TrustSigstore && v != nil && v.Available()
	var archiveOut string
	if needAttest {
		af, err := os.CreateTemp(build.TmpDir(), "gale-verify-*.tar.zst")
		if err != nil {
			return fmt.Errorf("create attestation tempfile: %w", err)
		}
		archiveOut = af.Name()
		af.Close()
		defer os.Remove(archiveOut)
	}

	// Bound the number of concurrent network fetches. The slot is
	// held ONLY around the leaf fetch and released before the
	// attestation/commit steps, so no install ever holds a slot
	// while waiting on a child dep — that would risk a deadlock.
	// A nil limiter is unbounded.
	fetchErr := func() error {
		dl.Acquire()
		defer dl.Release()
		streamDone := timing.Phase("binary-stream " + pkgID)
		defer streamDone()
		_, err := download.FetchAndExtractTarZstdWithArchive(bin.URL, stagingDir, bin.SHA256, token, archiveOut)
		return err
	}()
	if fetchErr != nil {
		return fmt.Errorf("fetch binary: %w", fetchErr)
	}

	// Attestation verification fires only when the recipe's
	// trust policy requires it (sigstore — the default) AND
	// a Verifier is wired up and available. `nil = skip` is
	// load-bearing: tests set Verifier=nil instead of mocking
	// the gh CLI. The explicit trust-policy check above
	// closes the C3 bypass where a non-GHCR URL dodged this
	// step entirely; here we only verify when the recipe
	// opted in to sigstore (which by definition means GHCR).
	if needAttest {
		attestDone := timing.Phase("attestation " + pkgID)
		err := verifyPrebuiltAttestation(bin, version, archiveOut, token, v)
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
	if err := fixupExtracted(stagingDir, finalStoreDir, storeRoot); err != nil {
		return err
	}

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
	if !HasDepsMetadata(stagingDir) {
		md := DepsMetadata{Deps: depsFallback}
		if err := WriteDepsMetadata(stagingDir, md); err != nil {
			return fmt.Errorf("write deps metadata: %w", err)
		}
	}

	// Commit: rename the fully-finalized staging dir into the
	// canonical extract dir (+ farm wiring when inPlace). The
	// fetch + verify + fixups above intentionally stay outside
	// the store-gen lock: they don't touch the canonical store
	// dir, and a network stall must not block a concurrent sync.
	return commitExtracted(stagingDir, extractDir, finalStoreDir, storeRoot, inPlace)
}

// fixupExtracted runs the post-extract fixup pipeline on dir,
// rewriting content for its final home at finalStoreDir. dir is
// always a staging dir: nothing here may assume the package is
// visible in the store yet (gh#41 — the canonical dir must only
// ever hold fully-finalized content).
func fixupExtracted(dir, finalStoreDir, storeRoot string) error {
	// Rewrite .pc files so pkg-config resolves from
	// the store dir, not the original build prefix.
	if err := build.FixupPkgConfig(dir); err != nil {
		return fmt.Errorf("fixup pkg-config: %w", err)
	}

	// Replace @@GALE_PREFIX@@ placeholders with the
	// actual store dir in scripts and text files.
	if err := build.RestorePrefixPlaceholderTo(dir, finalStoreDir); err != nil {
		return fmt.Errorf("restore prefix placeholders: %w", err)
	}

	// Rewrite CI-baked .gale/pkg/ paths in text files
	// (scripts, .pc Libs.private, .la files, etc.) so
	// they use the local store root.
	if err := build.RelocateStalePathsInTextFiles(dir, storeRoot); err != nil {
		return fmt.Errorf("relocate stale paths in text files: %w", err)
	}

	// Migrate legacy absolute rpaths in prebuilts published
	// before the relative-rpath change: rewrite any RPATH
	// referencing a foreign gale store root (CI-baked paths
	// like /Users/runner/.gale/pkg/... or /home/runner/.gale)
	// to the local store root. Runs on both darwin and linux.
	// Current builds (since #26) ship $ORIGIN/@loader_path-
	// relative rpaths that fall through this untouched, so a
	// fresh-box install is byte-for-byte with the attested
	// artifact and needs no patchelf at all.
	//
	// On linux the rewrite needs patchelf; if it is absent the
	// step no-ops and returns nil — it does NOT error and does
	// NOT trigger a source rebuild (the source fallback fires
	// only when installBinaryTo returns a non-nil error).
	// That gap only bites the obsolete pre-#26 prebuilts whose
	// absolute CI rpath this step exists to repair; for them a
	// patchelf-less box would install a binary with a stale
	// rpath. Relative-rpath builds are unaffected.
	if err := build.RelocateStaleRpaths(dir, storeRoot); err != nil {
		return fmt.Errorf("relocate rpaths: %w", err)
	}

	// Ad-hoc sign any Mach-O that arrived unsigned — Apple
	// Silicon kernels SIGKILL unsigned binaries on exec, and
	// RelocateStaleRpaths only re-signs files whose rpaths
	// were rewritten. No-op on Linux.
	if err := build.EnsureCodeSigned(dir); err != nil {
		return fmt.Errorf("ensure code signed: %w", err)
	}

	return nil
}

// commitExtracted promotes a fully-finalized staging dir into
// the canonical store dir. The rename is the commit point: all
// fixups already ran in stagingDir (gh#41), so a crash on
// either side of the rename leaves the store consistent —
// before: only a transient staging dir; after: a complete
// install (at worst missing farm links, which farm.Rebuild
// restores on the next gen swap).
//
// When inPlace is true, the swap and farm.Populate run under
// the store-gen lock so a concurrent generation.Build sees
// either the pre-install state or the completed install —
// never an intermediate. When inPlace is false, the caller is
// staging into a sibling dir and owns the final commit
// (commitStaged), including the farm wiring.
func commitExtracted(stagingDir, extractDir, finalStoreDir, storeRoot string, inPlace bool) error {
	swap := func() error {
		// extractDir was created empty by Store.Create (or is
		// the caller's empty staging target). Remove it so the
		// rename can land in its place.
		if err := os.RemoveAll(extractDir); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove empty extract dir: %w", err)
		}
		if err := renameDir(stagingDir, extractDir); err != nil {
			return fmt.Errorf("promote staging dir: %w", err)
		}
		if !inPlace {
			return nil
		}
		// Populate the shared lib farm with symlinks to this
		// package's versioned dylibs. A conflict (two packages
		// claiming the same dylib) is a recipe bug — fail the
		// install on EVERY path (gh#42) so the bad recipe gets
		// fixed instead of silently shipping a farm where one
		// package wins.
		if farmDir := farm.DirFromStoreDir(finalStoreDir); farmDir != "" {
			if err := farm.Populate(finalStoreDir, farmDir); err != nil {
				return fmt.Errorf("populate farm: %w", err)
			}
		}
		return nil
	}
	if inPlace {
		return withStoreGenLock(storeRoot, swap)
	}
	return swap()
}

// checkBinaryTrustPolicy enforces the recipe-declared
// verification policy for a [binary.<platform>] entry.
//
// Decision table, indexed by the effective trust (empty
// defaults to sigstore):
//
//   - sigstore + GHCR URL: accept. Attestation is
//     verified later when the Verifier is available.
//   - sigstore + non-GHCR URL: reject. We cannot produce
//     a Sigstore attestation for a third-party host that
//     isn't signing under our CI identity. Silently
//     skipping attestation here was the C3 bypass.
//   - sha256-only: accept regardless of host. Recipe has
//     explicitly opted out of attestation; only the
//     SHA256 is verified downstream.
//
// The returned error text names the field (trust) and the
// policy value (sigstore) so the installer's fallback log
// surfaces an actionable message.
func checkBinaryTrustPolicy(bin *recipe.Binary) error {
	switch bin.EffectiveTrust() {
	case recipe.TrustSHA256Only:
		return nil
	case recipe.TrustSigstore:
		if !isGHCR(bin.URL) {
			return fmt.Errorf(
				"binary trust policy: %q requires a ghcr.io URL "+
					"(got %q); set trust = %q to opt out",
				recipe.TrustSigstore, bin.URL, recipe.TrustSHA256Only,
			)
		}
		return nil
	default:
		return fmt.Errorf(
			"binary trust policy: unknown trust value %q",
			bin.Trust,
		)
	}
}

// isGHCR returns true if the URL host is ghcr.io. Only
// ghcr.io receives bearer tokens — never send credentials
// to arbitrary hosts based on path patterns alone.
func isGHCR(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return u.Host == "ghcr.io"
}

// verifyManifestDigest enforces digest-based fetch (gh#121). When a
// binary carries a manifest digest, gale pulls the OCI manifest by
// that digest and confirms it references exactly the layer the
// ledger's sha256 names — the manifest is the immutable, attested
// handle, and the sha256 is the cross-check second factor. Returns
// nil when no digest is declared (legacy recipes fetch the blob
// directly). All failures propagate so the binary install aborts
// to a source-build fallback.
func verifyManifestDigest(bin *recipe.Binary, token string) error {
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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

// binaryOCIURI builds the OCI image reference to verify for a
// prebuilt binary. The attestation is keyed to the manifest digest
// when available; otherwise it falls back to the mutable tag.
func binaryOCIURI(bin *recipe.Binary, version string) string {
	platform := runtime.GOOS + "-" + runtime.GOARCH
	repoPath := repoFromURL(bin.URL)
	return attestation.OCIURI(repoPath, version, platform, bin.ManifestDigest)
}

// fetchReferrerBundle is the seam over ghcr.FetchReferrerBundle so
// tests can stay hermetic (no real GHCR referrers API call).
var fetchReferrerBundle = ghcr.FetchReferrerBundle

// verifyPrebuiltAttestation routes a prebuilt binary's attestation
// check through attestation.VerifyPrebuilt: the tokenless OCI-referrer
// path first, falling back to the teed archive file only when no
// referrer exists. archiveOut is the teed copy of the downloaded
// archive bytes the file path verifies.
func verifyPrebuiltAttestation(bin *recipe.Binary, version, archiveOut, token string, v attestation.Verifier) error {
	return attestation.VerifyPrebuilt(v, attestation.PrebuiltParams{
		Repo:           attestation.DefaultRepo,
		OCIURI:         binaryOCIURI(bin, version),
		ManifestDigest: bin.ManifestDigest,
		FetchBundle: func() ([]byte, error) {
			ctx, cancel := context.WithTimeout(
				context.Background(), 30*time.Second,
			)
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

// InstallBuildDeps installs every declared direct
// dependency (build + runtime, after applying the platform
// overlay and merging in implicit system tools). Used by
// the source-build paths (`gale build`, InstallLocal,
// InstallGit) where the full toolchain is needed.
//
// The binary-first install path uses InstallRuntimeDeps and
// InstallBuildOnlyDeps instead so build-only deps can be
// skipped when a prebuilt binary install succeeds.
func (inst *Installer) InstallBuildDeps(r *recipe.Recipe) (*build.BuildDeps, error) {
	deps := withSystemDeps(
		r.DependenciesForPlatform(runtime.GOOS, runtime.GOARCH),
		r.Build.System,
	)
	rCopy := copyRecipeForDeps(r, deps)
	seen := make(map[string]bool)
	return inst.installDepsInner(rCopy, seen, &sync.Mutex{})
}

// InstallRuntimeDeps installs only the runtime-tagged
// direct deps of r. Build-only deps are not installed.
// Transitive resolution inside each runtime dep is
// unchanged — that dep's own build deps still get
// installed if it has to be built from source.
//
// Called by the binary-first install path so a prebuilt
// binary install doesn't drag in autoconf et al.
func (inst *Installer) InstallRuntimeDeps(r *recipe.Recipe) (*build.BuildDeps, error) {
	deps := r.DependenciesForPlatform(runtime.GOOS, runtime.GOARCH)
	deps.Build = nil
	rCopy := copyRecipeForDeps(r, deps)
	seen := make(map[string]bool)
	return inst.installDepsInner(rCopy, seen, &sync.Mutex{})
}

// InstallBuildOnlyDeps installs only the build-tagged
// direct deps of r (plus implicit system tools). Used by
// the binary-fallback-to-source path after
// InstallRuntimeDeps has already installed the runtime
// deps: now we top up with the build-only pieces before
// running the source build.
func (inst *Installer) InstallBuildOnlyDeps(r *recipe.Recipe) (*build.BuildDeps, error) {
	deps := withSystemDeps(
		r.DependenciesForPlatform(runtime.GOOS, runtime.GOARCH),
		r.Build.System,
	)
	deps.Runtime = nil
	rCopy := copyRecipeForDeps(r, deps)
	seen := make(map[string]bool)
	return inst.installDepsInner(rCopy, seen, &sync.Mutex{})
}

// ResolveDirectDeps returns the (name, version, revision)
// tuple for every direct declared dep of r (build +
// runtime, deduped, after platform overlay). Does NOT
// install anything — the binary-install path uses this to
// populate .gale-deps.toml with the full declared closure
// even though build-only deps weren't actually installed.
// Keeps IsStale's "declared dep missing from metadata =
// stale" contract intact.
func (inst *Installer) ResolveDirectDeps(r *recipe.Recipe) ([]ResolvedDep, error) {
	if inst.Resolver == nil {
		return nil, nil
	}
	deps := withSystemDeps(
		r.DependenciesForPlatform(runtime.GOOS, runtime.GOARCH),
		r.Build.System,
	)
	names := make([]string, 0,
		len(deps.Build)+len(deps.Runtime))
	seen := make(map[string]bool)
	for _, d := range deps.Build {
		if !seen[d] {
			seen[d] = true
			names = append(names, d)
		}
	}
	for _, d := range deps.Runtime {
		if !seen[d] {
			seen[d] = true
			names = append(names, d)
		}
	}
	resolved := make([]ResolvedDep, 0, len(names))
	for _, name := range names {
		dr, err := inst.Resolver(name)
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
		resolved = append(resolved, ResolvedDep{
			Name:     name,
			Version:  dr.Package.Version,
			Revision: dr.Package.Revision,
		})
	}
	return resolved, nil
}

// withSystemDeps returns deps with build.SystemDeps(system)
// merged into deps.Build (deduped). Returns the input
// unchanged when there are no system deps to merge.
func withSystemDeps(deps recipe.Dependencies, system string) recipe.Dependencies {
	sysDeps := build.SystemDeps(system)
	if len(sysDeps) == 0 {
		return deps
	}
	explicit := make(map[string]bool, len(deps.Build))
	for _, d := range deps.Build {
		explicit[d] = true
	}
	merged := append([]string{}, deps.Build...)
	for _, d := range sysDeps {
		if !explicit[d] {
			merged = append(merged, d)
		}
	}
	deps.Build = merged
	return deps
}

// mergeBuildDeps returns a BuildDeps whose slices and
// NamedDirs are the union of a and b. Used to combine the
// runtime-only install (done before the binary attempt)
// with the build-only install (done after binary failure)
// before handing the merged set to a source build.
func mergeBuildDeps(a, b *build.BuildDeps) *build.BuildDeps {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	out := &build.BuildDeps{
		BinDirs:   append([]string{}, a.BinDirs...),
		StoreDirs: append([]string{}, a.StoreDirs...),
		NamedDirs: make(map[string]string,
			len(a.NamedDirs)+len(b.NamedDirs)),
	}
	for k, v := range a.NamedDirs {
		out.NamedDirs[k] = v
	}
	out.BinDirs = append(out.BinDirs, b.BinDirs...)
	out.StoreDirs = append(out.StoreDirs, b.StoreDirs...)
	for k, v := range b.NamedDirs {
		if _, exists := out.NamedDirs[k]; !exists {
			out.NamedDirs[k] = v
		}
	}
	return out
}

// copyRecipeForDeps creates a shallow copy of a Recipe with
// deep-copied Build.Platform and Binary maps, and the given
// merged build deps. This prevents map aliasing between the
// copy and the original.
func copyRecipeForDeps(r *recipe.Recipe, deps recipe.Dependencies) *recipe.Recipe {
	var platformCopy map[string]recipe.PlatformBuild
	if r.Build.Platform != nil {
		platformCopy = make(
			map[string]recipe.PlatformBuild, len(r.Build.Platform),
		)
		for k, v := range r.Build.Platform {
			platformCopy[k] = v
		}
	}

	var binaryCopy map[string]recipe.Binary
	if r.Binary != nil {
		binaryCopy = make(
			map[string]recipe.Binary, len(r.Binary),
		)
		for k, v := range r.Binary {
			binaryCopy[k] = v
		}
	}

	var depPlatformCopy map[string]recipe.PlatformDependencies
	if r.Dependencies.Platform != nil {
		depPlatformCopy = make(map[string]recipe.PlatformDependencies, len(r.Dependencies.Platform))
		for k, v := range r.Dependencies.Platform {
			depPlatformCopy[k] = v
		}
	}

	// Preserve the Constraints map so the recursive
	// installDepsInner can enforce version constraints the
	// parent recipe declared (C4). Without this copy, the
	// merged-deps recipe handed to installDepsInner loses
	// every constraint entry.
	var constraintsCopy map[string]string
	if r.Dependencies.Constraints != nil {
		constraintsCopy = make(map[string]string, len(r.Dependencies.Constraints))
		for k, v := range r.Dependencies.Constraints {
			constraintsCopy[k] = v
		}
	}

	return &recipe.Recipe{
		Package: r.Package,
		Source:  r.Source,
		Build: recipe.Build{
			System:    r.Build.System,
			Steps:     r.Build.Steps,
			Debug:     r.Build.Debug,
			Env:       r.Build.Env,
			Toolchain: r.Build.Toolchain,
			Platform:  platformCopy,
		},
		Binary: binaryCopy,
		Dependencies: recipe.Dependencies{
			Build:       deps.Build,
			Runtime:     deps.Runtime,
			Constraints: constraintsCopy,
			Platform:    depPlatformCopy,
		},
	}
}

// installDepsInner recursively installs build and runtime
// dependencies. The seen map prevents cycles and deduplicates
// diamond dependency graphs.
func (inst *Installer) installDepsInner(
	r *recipe.Recipe,
	seen map[string]bool,
	seenMu *sync.Mutex,
) (*build.BuildDeps, error) {
	deps := r.DependenciesForPlatform(runtime.GOOS, runtime.GOARCH)
	allDeps := append([]string{}, deps.Build...)
	allDeps = append(allDeps, deps.Runtime...)

	if len(allDeps) == 0 || inst.Resolver == nil {
		return &build.BuildDeps{}, nil
	}

	var result build.BuildDeps
	// seenMu (shared across every recursion level) guards the seen
	// map for cycle/diamond dedup; resMu guards every write into
	// this level's result. The dep loop now fans out: each dep
	// installs in its own goroutine so their leaf network fetches
	// overlap, bounded by the Installer's Downloads limiter inside
	// installBinaryTo. No limiter or store-gen lock is held across
	// the wait on child goroutines, so the nested recursion cannot
	// deadlock against the fan-out.
	var resMu sync.Mutex

	errs := parallel.ForEach(
		context.Background(), allDeps, len(allDeps),
		func(_ context.Context, dep string) error {
			// Claim the dep before installing so two goroutines
			// never both install the same shared (diamond) dep.
			seenMu.Lock()
			if seen[dep] {
				seenMu.Unlock()
				return nil
			}
			seen[dep] = true
			seenMu.Unlock()

			depRecipe, err := inst.Resolver(dep)
			if err != nil {
				return fmt.Errorf("resolve dep %q: %w", dep, err)
			}
			if depRecipe == nil {
				return fmt.Errorf(
					"no recipe found for dependency %q", dep,
				)
			}

			// C4: enforce any version constraint the parent recipe
			// declared on this dep. The constraint was parsed and
			// stored at recipe load time but was previously only
			// consulted by IsStale — the resolver silently accepted
			// whatever version the registry said was latest. Now a
			// constraint mismatch fails the install with a clear
			// message naming the dep, required constraint, and
			// resolved version. Bare-string deps have no entry in
			// Constraints and skip this check, preserving today's
			// "resolve to latest" behavior.
			if expr, has := r.Dependencies.Constraints[dep]; has && expr != "" {
				c, cerr := recipe.ParseConstraint(expr)
				if cerr != nil {
					return fmt.Errorf(
						"dep %q: invalid version constraint %q: %w",
						dep, expr, cerr,
					)
				}
				if !c.Satisfies(
					depRecipe.Package.Version,
					depRecipe.Package.Revision,
				) {
					return fmt.Errorf(
						"dep %q: resolved version %s does not "+
							"satisfy constraint %q (declared in %s)",
						dep, depRecipe.Package.Full(), expr,
						r.Package.Name,
					)
				}
			}

			// Install the dep (will be cached if already present).
			if _, err := inst.Install(depRecipe); err != nil {
				return fmt.Errorf("install dep %q: %w", dep, err)
			}

			// Resolve the dep's actual store path. Install wrote
			// to <name>/<version>-<revision>/, but Store.StorePath
			// also falls back to a bare <version>/ dir for
			// pre-revision installs.
			storeDir, ok := inst.Store.StorePath(
				dep, depRecipe.Package.Full(),
			)
			if !ok {
				return fmt.Errorf(
					"dep %q at %s not in store after install",
					dep, depRecipe.Package.Full(),
				)
			}

			binDir := filepath.Join(storeDir, "bin")
			_, binErr := os.Stat(binDir)

			// Recurse for transitive deps before recording this
			// dep, so the merged result keeps the dep ahead of its
			// own transitive closure as the serial loop did.
			transitive, err := inst.installDepsInner(depRecipe, seen, seenMu)
			if err != nil {
				return fmt.Errorf("transitive deps of %q: %w",
					dep, err)
			}

			resMu.Lock()
			defer resMu.Unlock()
			result.StoreDirs = append(result.StoreDirs, storeDir)
			if result.NamedDirs == nil {
				result.NamedDirs = make(map[string]string)
			}
			result.NamedDirs[dep] = storeDir
			if binErr == nil {
				result.BinDirs = append(result.BinDirs, binDir)
			}
			result.BinDirs = append(
				result.BinDirs, transitive.BinDirs...,
			)
			result.StoreDirs = append(
				result.StoreDirs, transitive.StoreDirs...,
			)
			for k, v := range transitive.NamedDirs {
				if _, exists := result.NamedDirs[k]; !exists {
					result.NamedDirs[k] = v
				}
			}
			return nil
		},
	)
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return &result, nil
}

func installFromLocalSource(r *recipe.Recipe, sourceDir, storeDir string, deps *build.BuildDeps) (string, error) {
	tmpDir, err := os.MkdirTemp(build.TmpDir(), "gale-install-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	result, err := build.BuildLocal(r, sourceDir, tmpDir, r.Build.Debug, deps)
	if err != nil {
		return "", fmt.Errorf("building from local source: %w", err)
	}
	return result.SHA256, extractBuild(result, storeDir, deps)
}

// installFromSourceTo runs the source build and extracts the
// archive into extractDir. inPlace mirrors installBinaryTo:
// true means extractDir is the live canonical dir; false
// means the caller is staging.
func installFromSourceTo(r *recipe.Recipe, extractDir, finalStoreDir string, deps *build.BuildDeps, inPlace bool) (string, error) {
	tmpDir, err := os.MkdirTemp(build.TmpDir(), "gale-install-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	result, err := build.Build(r, tmpDir, r.Build.Debug, deps)
	if err != nil {
		return "", fmt.Errorf("building from source: %w", err)
	}
	return result.SHA256, extractBuildTo(result, extractDir, finalStoreDir, deps, inPlace)
}

// extractBuild extracts a build archive into the store dir
// and restores prefix placeholders to the actual store path.
// If deps is non-nil, writes .gale-deps.toml recording the
// dep closure the build was linked against.
//
// H7: the store-visible commit (rename + farm.Populate inside
// commitExtracted) runs under the store-gen lock so a
// concurrent generation.Build cannot observe a half-extracted
// package or race with farm.Populate. Extraction itself
// happens in a transient staging sibling outside the lock —
// non-locking readers skip it (isTransientStoreEntry).
func extractBuild(result *build.BuildResult, storeDir string, deps *build.BuildDeps) error {
	return extractBuildTo(result, storeDir, storeDir, deps, true)
}

// extractBuildTo extracts result.Archive into extractDir,
// rewriting prefix placeholders to point at finalStoreDir.
// When inPlace is true, the work happens in a staging sibling
// that is promoted into extractDir under the store-gen lock
// (with farm wiring); otherwise extractDir is the caller's own
// staging dir and the caller commits both (commitStaged).
func extractBuildTo(result *build.BuildResult, extractDir, finalStoreDir string, deps *build.BuildDeps, inPlace bool) error {
	storeRoot := filepath.Dir(filepath.Dir(finalStoreDir))

	// Extract + fix up in a transient staging sibling, then
	// promote with a rename (gh#41). Extracting straight into
	// the live canonical dir meant a crash mid-install left a
	// partial-but-non-empty dir that IsInstalled trusted
	// forever. The ".stream" suffix is in isTransientStoreEntry's
	// skip set, so non-locking readers never see the staging dir.
	workDir := extractDir
	if inPlace {
		workDir = extractDir + ".stream"
		// A previous crashed install may have left stale
		// staging debris; wipe before fresh extraction.
		if err := os.RemoveAll(workDir); err != nil {
			return fmt.Errorf("clean staging dir: %w", err)
		}
		// Pre-create the staging dir: an archive with no
		// entries (legal for trivial recipes) would otherwise
		// never create it and the metadata write below fails.
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			return fmt.Errorf("create staging dir: %w", err)
		}
		defer os.RemoveAll(workDir) // clean up on any exit path
	}

	if err := download.ExtractTarZstd(result.Archive, workDir); err != nil {
		return fmt.Errorf("extract build output: %w", err)
	}
	if err := build.RestorePrefixPlaceholderTo(workDir, finalStoreDir); err != nil {
		return fmt.Errorf("restore prefix paths: %w", err)
	}
	if deps != nil {
		md := DepsMetadata{Deps: BuildDepsToResolved(deps)}
		if err := WriteDepsMetadata(workDir, md); err != nil {
			return fmt.Errorf("write deps metadata: %w", err)
		}
	}
	if !inPlace {
		return nil
	}
	return commitExtracted(workDir, extractDir, finalStoreDir, storeRoot, true)
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
// package or race with farm.Populate.
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
func (inst *Installer) InstallWithFinalize(r *recipe.Recipe, force bool, finalize func(*InstallResult) error) (*InstallResult, error) {
	unlock, err := lockPackage(inst.Store.Root, r.Package.Name, r.Package.Full())
	if err != nil {
		return nil, fmt.Errorf("lock package: %w", err)
	}
	defer unlock()

	result, err := inst.installLocked(r, force)
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
