package installer

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

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
	Store    *store.Store
	Resolver RecipeResolver
}

// InstallResult holds the outcome of an install.
type InstallResult struct {
	Name    string
	Version string
	Method  string // "binary" or "source"
}

// Install installs a recipe into the store and links binaries.
func (inst *Installer) Install(r *recipe.Recipe) (*InstallResult, error) {
	name := r.Package.Name
	version := r.Package.Version

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

	// Try binary first.
	bin := r.BinaryForPlatform(runtime.GOOS, runtime.GOARCH)
	if bin != nil {
		if err := installBinary(bin, storeDir); err == nil {
			method = "binary"
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

		if err := installFromSource(r, storeDir, depPaths); err != nil {
			// Clean up failed install.
			os.RemoveAll(storeDir)
			return nil, fmt.Errorf("build from source: %w", err)
		}
	}

	return &InstallResult{
		Name:    name,
		Version: version,
		Method:  method,
	}, nil
}

// InstallLocal installs a recipe from a local source directory.
// Skips binary install and downloads — builds directly from
// sourceDir using build.BuildLocal.
func (inst *Installer) InstallLocal(r *recipe.Recipe, sourceDir string) (*InstallResult, error) {
	name := r.Package.Name
	version := r.Package.Version

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

	// Resolve and install build deps.
	depPaths, err := inst.InstallBuildDeps(r)
	if err != nil {
		os.RemoveAll(storeDir)
		return nil, fmt.Errorf("install build deps: %w", err)
	}

	if err := installFromLocalSource(r, sourceDir, storeDir, depPaths); err != nil {
		os.RemoveAll(storeDir)
		return nil, fmt.Errorf("build from local source: %w", err)
	}

	return &InstallResult{
		Name:    name,
		Version: version,
		Method:  "source",
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

	buildResult, hash, err := build.BuildGit(r, tmpDir, depPaths...)
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
	}, nil
}

func installBinary(bin *recipe.Binary, storeDir string) error {
	tmpFile := storeDir + ".download.tar.zst"
	defer os.Remove(tmpFile)

	if isGHCR(bin.URL) {
		repo := repoFromURL(bin.URL)
		token, err := ghcr.Token(repo)
		if err != nil {
			return fmt.Errorf("ghcr auth: %w", err)
		}
		if err := download.FetchWithAuth(bin.URL, tmpFile, token); err != nil {
			return fmt.Errorf("fetch binary: %w", err)
		}
	} else {
		if err := download.Fetch(bin.URL, tmpFile); err != nil {
			return fmt.Errorf("fetch binary: %w", err)
		}
	}

	if err := download.VerifySHA256(tmpFile, bin.SHA256); err != nil {
		return fmt.Errorf("verify binary: %w", err)
	}

	if err := download.ExtractTarZstd(tmpFile, storeDir); err != nil {
		return fmt.Errorf("extract binary: %w", err)
	}

	return nil
}

// isGHCR returns true if the URL points to a GHCR blob
// endpoint. Matches both ghcr.io host and the /v2/.../blobs/
// path pattern used by OCI registries.
func isGHCR(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if u.Host == "ghcr.io" {
		return true
	}
	// Also match OCI blob URL pattern for any host (enables
	// testing with httptest servers).
	return strings.HasPrefix(u.Path, "/v2/") &&
		strings.Contains(u.Path, "/blobs/")
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
func (inst *Installer) InstallBuildDeps(r *recipe.Recipe) ([]string, error) {
	if len(r.Dependencies.Build) == 0 || inst.Resolver == nil {
		return nil, nil
	}

	var binDirs []string
	for _, dep := range r.Dependencies.Build {
		// Check if already installed — find its version
		// by resolving the recipe.
		depRecipe, err := inst.Resolver(dep)
		if err != nil {
			return nil, fmt.Errorf("resolve dep %q: %w", dep, err)
		}
		if depRecipe == nil {
			return nil, fmt.Errorf(
				"no recipe found for build dependency %q", dep)
		}

		// Install the dep (will be cached if already present).
		if _, err := inst.Install(depRecipe); err != nil {
			return nil, fmt.Errorf("install dep %q: %w", dep, err)
		}

		// Add its bin dir to the list.
		depDir := filepath.Join(inst.Store.Root,
			dep, depRecipe.Package.Version, "bin")
		if _, err := os.Stat(depDir); err == nil {
			binDirs = append(binDirs, depDir)
		}
	}
	return binDirs, nil
}

func installFromLocalSource(r *recipe.Recipe, sourceDir, storeDir string, extraPaths []string) error {
	tmpDir, err := os.MkdirTemp(build.TmpDir(), "gale-install-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	result, err := build.BuildLocal(r, sourceDir, tmpDir, extraPaths...)
	if err != nil {
		return err
	}
	return extractBuild(result, storeDir)
}

func installFromSource(r *recipe.Recipe, storeDir string, extraPaths []string) error {
	tmpDir, err := os.MkdirTemp(build.TmpDir(), "gale-install-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	result, err := build.Build(r, tmpDir, extraPaths...)
	if err != nil {
		return err
	}
	return extractBuild(result, storeDir)
}

// extractBuild extracts a build archive into the store dir.
func extractBuild(result *build.BuildResult, storeDir string) error {
	if err := download.ExtractTarZstd(result.Archive, storeDir); err != nil {
		return fmt.Errorf("extract build output: %w", err)
	}
	return nil
}
