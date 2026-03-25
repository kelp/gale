package installer

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/profile"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
)

// Installer installs packages into the store and links them.
type Installer struct {
	Store   *store.Store
	Profile *profile.Profile
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
		}
		// Fall through to source on binary failure.
	}

	if method != "binary" {
		if err := installFromSource(r, storeDir); err != nil {
			// Clean up failed install.
			os.RemoveAll(storeDir)
			return nil, fmt.Errorf("build from source: %w", err)
		}
	}

	// Link binaries into profile.
	binDir := filepath.Join(storeDir, "bin")
	if _, err := os.Stat(binDir); err == nil {
		if err := inst.Profile.LinkPackageBinaries(binDir); err != nil {
			return nil, fmt.Errorf("link binaries: %w", err)
		}
	}

	return &InstallResult{
		Name:    name,
		Version: version,
		Method:  method,
	}, nil
}

func installBinary(bin *recipe.Binary, storeDir string) error {
	tmpFile := storeDir + ".download.tar.zst"
	defer os.Remove(tmpFile)

	if err := download.Fetch(bin.URL, tmpFile); err != nil {
		return fmt.Errorf("fetch binary: %w", err)
	}

	if err := download.VerifySHA256(tmpFile, bin.SHA256); err != nil {
		return fmt.Errorf("verify binary: %w", err)
	}

	if err := download.ExtractTarZstd(tmpFile, storeDir); err != nil {
		return fmt.Errorf("extract binary: %w", err)
	}

	return nil
}

func installFromSource(r *recipe.Recipe, storeDir string) error {
	// Build to a temp directory.
	tmpDir, err := os.MkdirTemp("", "gale-install-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	result, err := build.Build(r, tmpDir)
	if err != nil {
		return err
	}

	// Extract the built archive into the store.
	if err := download.ExtractTarZstd(result.Archive, storeDir); err != nil {
		return fmt.Errorf("extract build output: %w", err)
	}

	return nil
}
