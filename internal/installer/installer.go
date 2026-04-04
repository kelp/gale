package installer

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/kelp/gale/internal/attestation"
	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/ghcr"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
)

// RecipeResolver finds and parses a recipe by package name.
// Returns nil if the package has no recipe.
type RecipeResolver func(name string) (*recipe.Recipe, error)

// Installer installs packages into the store.
type Installer struct {
	Store      *store.Store
	Resolver   RecipeResolver
	Verifier   attestation.Verifier // nil = skip attestation
	SourceOnly bool                 // skip binary, build from source
}

// InstallResult holds the outcome of an install.
type InstallResult struct {
	Name    string
	Version string
	Method  string // "binary", "source", or "cached"
	SHA256  string // hex hash of installed archive
}

// Install installs a recipe into the store and links binaries.
func (inst *Installer) Install(r *recipe.Recipe) (*InstallResult, error) {
	name := r.Package.Name
	version := r.Package.Version

	// Acquire a file lock to prevent concurrent installs
	// of the same package version from corrupting the store.
	unlock, err := lockPackage(inst.Store.Root, name, version)
	if err != nil {
		return nil, fmt.Errorf("lock package: %w", err)
	}
	defer unlock()

	// Skip if already installed.
	if inst.Store.IsInstalled(name, version) {
		return &InstallResult{
			Name:    name,
			Version: version,
			Method:  "cached",
		}, nil
	}

	// Create store directory.
	storeDir, err := inst.Store.Create(name, version)
	if err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}

	method := "source"
	var sha256 string

	// Try binary first (unless source-only mode).
	bin := r.BinaryForPlatform(runtime.GOOS, runtime.GOARCH)
	if bin != nil && !inst.SourceOnly {
		if err := installBinary(bin, storeDir, name, version, inst.Verifier); err == nil {
			method = "binary"
			sha256 = bin.SHA256
		} else {
			// Clean partial download before source fallback.
			os.RemoveAll(storeDir)
			_ = os.MkdirAll(storeDir, 0o755) //nolint:gosec
		}
	}

	if method != "binary" {
		// Resolve and install build deps, collect their
		// bin dirs for the build PATH.
		depPaths, err := inst.InstallBuildDeps(r)
		if err != nil {
			os.RemoveAll(storeDir)
			return nil, fmt.Errorf("install build deps: %w", err)
		}

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

	// Serialize concurrent local installs for the same
	// package version, matching Install's locking.
	unlock, err := lockPackage(inst.Store.Root, name, version)
	if err != nil {
		return nil, fmt.Errorf("lock package: %w", err)
	}
	defer unlock()

	// Build into a temp dir inside <storeRoot>/<name>/ so
	// the existing store entry and its active symlinks stay
	// intact until the build succeeds. Same-filesystem
	// rename is guaranteed since both paths are under the
	// same parent.
	storeDir := filepath.Join(inst.Store.Root, name, version)
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
	os.RemoveAll(storeDir)
	if err := os.Rename(buildDir, storeDir); err != nil {
		return nil, fmt.Errorf("install build output: %w", err)
	}

	return &InstallResult{
		Name:    name,
		Version: version,
		Method:  "source",
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
			Method:  "cached",
		}, nil
	}

	// Create store dir and extract.
	storeDir, err := inst.Store.Create(name, hash)
	if err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}

	if err := extractBuild(buildResult, storeDir); err != nil {
		os.RemoveAll(storeDir)
		return nil, err
	}

	return &InstallResult{
		Name:    name,
		Version: hash,
		Method:  "source",
		SHA256:  buildResult.SHA256,
	}, nil
}

func installBinary(bin *recipe.Binary, storeDir, name, version string, v attestation.Verifier) error {
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
	// Merge implicit system deps with explicit build deps
	// without mutating the recipe.
	sysDeps := build.SystemDeps(r.Build.System)
	if len(sysDeps) > 0 {
		explicit := make(map[string]bool)
		for _, d := range r.Dependencies.Build {
			explicit[d] = true
		}
		merged := append([]string{},
			r.Dependencies.Build...)
		for _, d := range sysDeps {
			if !explicit[d] {
				merged = append(merged, d)
			}
		}
		r = copyRecipeForDeps(r, merged)
	}

	seen := make(map[string]bool)
	return inst.installDepsInner(r, seen)
}

// copyRecipeForDeps creates a shallow copy of a Recipe with
// deep-copied Build.Platform and Binary maps, and the given
// merged build deps. This prevents map aliasing between the
// copy and the original.
func copyRecipeForDeps(r *recipe.Recipe, mergedBuildDeps []string) *recipe.Recipe {
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

	return &recipe.Recipe{
		Package: r.Package,
		Source:  r.Source,
		Build: recipe.Build{
			System:   r.Build.System,
			Steps:    r.Build.Steps,
			Debug:    r.Build.Debug,
			Platform: platformCopy,
		},
		Binary: binaryCopy,
		Dependencies: recipe.Dependencies{
			Build:   mergedBuildDeps,
			Runtime: r.Dependencies.Runtime,
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
	allDeps := append([]string{}, r.Dependencies.Build...)
	allDeps = append(allDeps, r.Dependencies.Runtime...)

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

		// Record this dep's store path.
		storeDir := filepath.Join(inst.Store.Root,
			dep, depRecipe.Package.Version)
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
		return "", err
	}
	return result.SHA256, extractBuild(result, storeDir)
}

func installFromSource(r *recipe.Recipe, storeDir string, deps *build.BuildDeps) (string, error) {
	tmpDir, err := os.MkdirTemp(build.TmpDir(), "gale-install-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	result, err := build.Build(r, tmpDir, r.Build.Debug, deps)
	if err != nil {
		return "", err
	}
	return result.SHA256, extractBuild(result, storeDir)
}

// extractBuild extracts a build archive into the store dir
// and restores prefix placeholders to the actual store path.
func extractBuild(result *build.BuildResult, storeDir string) error {
	if err := download.ExtractTarZstd(result.Archive, storeDir); err != nil {
		return fmt.Errorf("extract build output: %w", err)
	}
	if err := build.RestorePrefixPlaceholder(storeDir); err != nil {
		return fmt.Errorf("restore prefix paths: %w", err)
	}
	return nil
}

// lockPackage acquires an exclusive file lock for a package
// version. Returns an unlock function that releases the lock.
// The lock file is kept on disk so all contenders share the
// same inode — removing it would cause a race where a new
// arrival creates a separate file and acquires its own lock.
func lockPackage(storeRoot, name, version string) (func(), error) {
	// Ensure the package directory exists for the lock file.
	pkgDir := filepath.Join(storeRoot, name)
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}

	lockPath := filepath.Join(pkgDir, version+".lock")
	f, err := os.OpenFile(
		lockPath, os.O_CREATE|os.O_RDWR, 0o644) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil { //nolint:gosec
		f.Close()
		return nil, fmt.Errorf("acquire lock: %w", err)
	}

	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck,gosec
		f.Close()
	}, nil
}
