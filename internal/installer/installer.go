package installer

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/kelp/gale/internal/attestation"
	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/filelock"
	"github.com/kelp/gale/internal/ghcr"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
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
	// the message.
	BinaryFallbackLog io.Writer
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
	name := r.Package.Name
	version := r.Package.Version
	// Store paths use the full <version>-<revision> form so
	// multiple revisions of the same version can coexist.
	// The Store layer falls back from "<v>-1" to bare "<v>"
	// for back-compat with pre-revision installs.
	storeVersion := r.Package.Full()

	// Acquire a file lock to prevent concurrent installs
	// of the same package version from corrupting the store.
	unlock, err := lockPackage(inst.Store.Root, name, storeVersion)
	if err != nil {
		return nil, fmt.Errorf("lock package: %w", err)
	}
	defer unlock()

	// Cache check. The default path accepts IsInstalled's
	// back-compat fallback (bare pre-revision dirs count as
	// "installed"), so dep installs don't needlessly
	// re-migrate every package. The forced path requires the
	// canonical dir specifically — a bare-only install is
	// not considered cached, which is what makes soft
	// migration actually do work.
	canonicalDir := filepath.Join(inst.Store.Root, name, storeVersion)
	if force {
		if entries, err := os.ReadDir(canonicalDir); err == nil && len(entries) > 0 {
			return &InstallResult{
				Name:    name,
				Version: version,
				Method:  MethodCached,
			}, nil
		}
	} else if inst.Store.IsInstalled(name, storeVersion) {
		return &InstallResult{
			Name:    name,
			Version: version,
			Method:  MethodCached,
		}, nil
	}

	// Create store directory.
	storeDir, err := inst.Store.Create(name, storeVersion)
	if err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}

	method := MethodSource
	var sha256 string

	// Resolve and install deps first. Needed for source
	// builds (to supply the build environment) AND for
	// prebuilt installs (so we can record the dep closure
	// in .gale-deps.toml for staleness detection).
	depPaths, err := inst.InstallBuildDeps(r)
	if err != nil {
		os.RemoveAll(storeDir)
		return nil, fmt.Errorf("install build deps: %w", err)
	}

	// Try binary first (unless source-only mode).
	bin := r.BinaryForPlatform(runtime.GOOS, runtime.GOARCH)
	if bin != nil && !inst.SourceOnly {
		if err := installBinary(bin, storeDir, name, version, depPaths, inst.Verifier); err == nil {
			method = MethodBinary
			sha256 = bin.SHA256
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
				name, version, err)
			os.RemoveAll(storeDir)
			_ = os.MkdirAll(storeDir, 0o755) //nolint:gosec
		}
	}

	if method != MethodBinary {
		hash, buildErr := installFromSource(r, storeDir, depPaths)
		if buildErr != nil {
			// Clean up failed install.
			os.RemoveAll(storeDir)
			return nil, fmt.Errorf("build from source: %w", buildErr)
		}
		sha256 = hash
	}

	return &InstallResult{
		Name:    name,
		Version: version,
		Method:  method,
		SHA256:  sha256,
	}, nil
}

// InstallLocal installs a recipe from a local source directory.
// Skips binary install and downloads — builds directly from
// sourceDir using build.BuildLocal. Always rebuilds even if
// the version exists in the store, since local source may
// have changed without a version bump.
func (inst *Installer) InstallLocal(r *recipe.Recipe, sourceDir string) (*InstallResult, error) {
	name := r.Package.Name
	version := r.Package.Version
	// Store paths use <version>-<revision> so revisions of
	// the same base version don't collide. Back-compat in
	// the Store layer resolves "<v>-1" to a bare "<v>" dir
	// when one exists from a pre-revision install.
	storeVersion := r.Package.Full()

	// Serialize concurrent local installs for the same
	// package version, matching Install's locking.
	unlock, err := lockPackage(inst.Store.Root, name, storeVersion)
	if err != nil {
		return nil, fmt.Errorf("lock package: %w", err)
	}
	defer unlock()

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
	depPaths, err := inst.InstallBuildDeps(r)
	if err != nil {
		return nil, fmt.Errorf("install build deps: %w", err)
	}

	hash, buildErr := installFromLocalSource(r, sourceDir, buildDir, depPaths)
	if buildErr != nil {
		return nil, fmt.Errorf("build from local source: %w", buildErr)
	}

	// Build succeeded — swap into place atomically.
	// The lock ensures no other process touches storeDir.
	if err := replaceStoreDir(storeDir, buildDir); err != nil {
		return nil, fmt.Errorf("install build output: %w", err)
	}

	return &InstallResult{
		Name:    name,
		Version: version,
		Method:  MethodSource,
		SHA256:  hash,
	}, nil
}

// InstallGit clones a git repo and builds from the clone.
// Returns the install result with the commit hash as version.
func (inst *Installer) InstallGit(r *recipe.Recipe) (*InstallResult, error) {
	name := r.Package.Name

	// Resolve and install build deps.
	depPaths, err := inst.InstallBuildDeps(r)
	if err != nil {
		return nil, fmt.Errorf("install build deps: %w", err)
	}

	// Build from git — returns hash as version.
	tmpDir, err := os.MkdirTemp(build.TmpDir(), "gale-install-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	buildResult, hash, err := build.BuildGit(r, tmpDir, r.Build.Debug, depPaths)
	if err != nil {
		return nil, fmt.Errorf("git build: %w", err)
	}
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

func installBinary(bin *recipe.Binary, storeDir, name, version string, deps *build.BuildDeps, v attestation.Verifier) error {
	tmpFile := storeDir + ".download.tar.zst"
	defer os.Remove(tmpFile)

	displayName := fmt.Sprintf("%s-%s.tar.zst", name, version)

	if isGHCR(bin.URL) {
		repo := repoFromURL(bin.URL)
		token, err := ghcr.Token(repo)
		if err != nil {
			return fmt.Errorf("ghcr auth: %w", err)
		}
		if err := download.FetchWithAuthNamed(bin.URL, tmpFile, token, displayName); err != nil {
			return fmt.Errorf("fetch binary: %w", err)
		}
	} else {
		if err := download.FetchNamed(bin.URL, tmpFile, displayName); err != nil {
			return fmt.Errorf("fetch binary: %w", err)
		}
	}

	if err := download.VerifySHA256(tmpFile, bin.SHA256); err != nil {
		return fmt.Errorf("verify binary: %w", err)
	}

	// Verify Sigstore attestation for GHCR binaries.
	if isGHCR(bin.URL) && v != nil && v.Available() {
		if err := v.VerifyFile(
			tmpFile, attestation.DefaultRepo); err != nil {
			return fmt.Errorf("attestation: %w", err)
		}
	}

	if err := download.ExtractTarZstd(tmpFile, storeDir); err != nil {
		return fmt.Errorf("extract binary: %w", err)
	}

	// Rewrite .pc files so pkg-config resolves from
	// the store dir, not the original build prefix.
	if err := build.FixupPkgConfig(storeDir); err != nil {
		return fmt.Errorf("fixup pkg-config: %w", err)
	}

	// Replace @@GALE_PREFIX@@ placeholders with the
	// actual store dir in scripts and text files.
	if err := build.RestorePrefixPlaceholder(storeDir); err != nil {
		return fmt.Errorf("restore prefix placeholders: %w", err)
	}

	// Rewrite CI-baked .gale/pkg/ paths in text files
	// (scripts, .pc Libs.private, .la files, etc.) so
	// they use the local store root.
	storeRoot := filepath.Dir(filepath.Dir(storeDir))
	if err := build.RelocateStalePathsInTextFiles(storeDir, storeRoot); err != nil {
		return fmt.Errorf("relocate stale paths in text files: %w", err)
	}

	// Relocate stale LC_RPATH entries that reference a
	// foreign gale store root (e.g. CI-baked paths like
	// /Users/runner/.gale/pkg/...). Only meaningful on
	// darwin; no-op on Linux where RelocateStaleRpaths
	// is a stub.
	if err := build.RelocateStaleRpaths(storeDir, storeRoot); err != nil {
		return fmt.Errorf("relocate rpaths: %w", err)
	}

	// Ad-hoc sign any Mach-O that arrived unsigned — Apple
	// Silicon kernels SIGKILL unsigned binaries on exec, and
	// RelocateStaleRpaths only re-signs files whose rpaths
	// were rewritten. No-op on Linux.
	if err := build.EnsureCodeSigned(storeDir); err != nil {
		return fmt.Errorf("ensure code signed: %w", err)
	}

	// Populate the shared lib farm with symlinks to this
	// package's versioned dylibs. A conflict (two packages
	// claiming the same dylib) is a recipe bug — fail the
	// install so the bad recipe gets fixed instead of
	// silently shipping a farm where one package wins.
	if farmDir := farm.DirFromStoreDir(storeDir); farmDir != "" {
		if err := farm.Populate(storeDir, farmDir); err != nil {
			return fmt.Errorf("populate farm: %w", err)
		}
	}

	// Record the dep closure the prebuilt expects at
	// runtime so staleness can be detected when a dep's
	// recipe changes. If the archive already shipped a
	// .gale-deps.toml (built by `gale build` with full
	// knowledge of the linked versions), keep that —
	// it's the authoritative record. Otherwise fall back
	// to locally-resolved versions, which is approximate
	// but preserves backwards-compat with archives built
	// before the build-time emit landed.
	if HasDepsMetadata(storeDir) {
		return nil
	}
	if deps != nil {
		md := DepsMetadata{Deps: BuildDepsToResolved(deps)}
		if err := WriteDepsMetadata(storeDir, md); err != nil {
			return fmt.Errorf("write deps metadata: %w", err)
		}
	}

	return nil
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

// InstallBuildDeps installs build dependencies and returns
// their bin directory paths.
func (inst *Installer) InstallBuildDeps(r *recipe.Recipe) (*build.BuildDeps, error) {
	deps := r.DependenciesForPlatform(runtime.GOOS, runtime.GOARCH)

	// Merge implicit system deps with explicit build deps
	// without mutating the recipe.
	sysDeps := build.SystemDeps(r.Build.System)
	if len(sysDeps) > 0 {
		explicit := make(map[string]bool)
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
	}
	r = copyRecipeForDeps(r, deps)

	seen := make(map[string]bool)
	return inst.installDepsInner(r, seen)
}

// copyRecipeForDeps creates a shallow copy of a Recipe with
// deep-copied Build.Platform and Binary maps, and the given
// merged build deps. This prevents map aliasing between the
// copy and the original.
func copyRecipeForDeps(r *recipe.Recipe, deps recipe.Dependencies) *recipe.Recipe {
	var platformCopy map[string]recipe.PlatformBuild
	if r.Build.Platform != nil {
		platformCopy = make(
			map[string]recipe.PlatformBuild, len(r.Build.Platform))
		for k, v := range r.Build.Platform {
			platformCopy[k] = v
		}
	}

	var binaryCopy map[string]recipe.Binary
	if r.Binary != nil {
		binaryCopy = make(
			map[string]recipe.Binary, len(r.Binary))
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
			Build:    deps.Build,
			Runtime:  deps.Runtime,
			Platform: depPlatformCopy,
		},
	}
}

// installDepsInner recursively installs build and runtime
// dependencies. The seen map prevents cycles and deduplicates
// diamond dependency graphs.
func (inst *Installer) installDepsInner(
	r *recipe.Recipe,
	seen map[string]bool,
) (*build.BuildDeps, error) {
	deps := r.DependenciesForPlatform(runtime.GOOS, runtime.GOARCH)
	allDeps := append([]string{}, deps.Build...)
	allDeps = append(allDeps, deps.Runtime...)

	if len(allDeps) == 0 || inst.Resolver == nil {
		return &build.BuildDeps{}, nil
	}

	var result build.BuildDeps
	for _, dep := range allDeps {
		if seen[dep] {
			continue
		}
		seen[dep] = true

		depRecipe, err := inst.Resolver(dep)
		if err != nil {
			return nil, fmt.Errorf("resolve dep %q: %w", dep, err)
		}
		if depRecipe == nil {
			return nil, fmt.Errorf(
				"no recipe found for dependency %q", dep)
		}

		// Install the dep (will be cached if already present).
		if _, err := inst.Install(depRecipe); err != nil {
			return nil, fmt.Errorf("install dep %q: %w", dep, err)
		}

		// Resolve the dep's actual store path. Install wrote
		// to <name>/<version>-<revision>/, but Store.StorePath
		// also falls back to a bare <version>/ dir for
		// pre-revision installs.
		storeDir, ok := inst.Store.StorePath(
			dep, depRecipe.Package.Full())
		if !ok {
			return nil, fmt.Errorf(
				"dep %q at %s not in store after install",
				dep, depRecipe.Package.Full())
		}
		result.StoreDirs = append(result.StoreDirs, storeDir)
		if result.NamedDirs == nil {
			result.NamedDirs = make(map[string]string)
		}
		result.NamedDirs[dep] = storeDir

		binDir := filepath.Join(storeDir, "bin")
		if _, err := os.Stat(binDir); err == nil {
			result.BinDirs = append(result.BinDirs, binDir)
		}

		// Recurse for transitive deps.
		transitive, err := inst.installDepsInner(depRecipe, seen)
		if err != nil {
			return nil, fmt.Errorf("transitive deps of %q: %w",
				dep, err)
		}
		result.BinDirs = append(
			result.BinDirs, transitive.BinDirs...)
		result.StoreDirs = append(
			result.StoreDirs, transitive.StoreDirs...)
		for k, v := range transitive.NamedDirs {
			if result.NamedDirs == nil {
				result.NamedDirs = make(map[string]string)
			}
			if _, exists := result.NamedDirs[k]; !exists {
				result.NamedDirs[k] = v
			}
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

func installFromSource(r *recipe.Recipe, storeDir string, deps *build.BuildDeps) (string, error) {
	tmpDir, err := os.MkdirTemp(build.TmpDir(), "gale-install-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	result, err := build.Build(r, tmpDir, r.Build.Debug, deps)
	if err != nil {
		return "", fmt.Errorf("building from source: %w", err)
	}
	return result.SHA256, extractBuild(result, storeDir, deps)
}

// extractBuild extracts a build archive into the store dir
// and restores prefix placeholders to the actual store path.
// If deps is non-nil, writes .gale-deps.toml recording the
// dep closure the build was linked against.
func extractBuild(result *build.BuildResult, storeDir string, deps *build.BuildDeps) error {
	if err := download.ExtractTarZstd(result.Archive, storeDir); err != nil {
		return fmt.Errorf("extract build output: %w", err)
	}
	if err := build.RestorePrefixPlaceholder(storeDir); err != nil {
		return fmt.Errorf("restore prefix paths: %w", err)
	}
	if farmDir := farm.DirFromStoreDir(storeDir); farmDir != "" {
		if err := farm.Populate(storeDir, farmDir); err != nil {
			fmt.Fprintf(os.Stderr, "farm: %v\n", err)
		}
	}
	if deps != nil {
		md := DepsMetadata{Deps: BuildDepsToResolved(deps)}
		if err := WriteDepsMetadata(storeDir, md); err != nil {
			return fmt.Errorf("write deps metadata: %w", err)
		}
	}
	return nil
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
