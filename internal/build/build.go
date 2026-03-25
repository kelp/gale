package build

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/recipe"
)

// BuildResult holds the output of a successful build.
type BuildResult struct {
	Archive string // path to the tar.zst file
	SHA256  string // hex-encoded hash of the archive
}

// Build builds a recipe from source and packages the result.
// outputDir is where the tar.zst will be written.
func Build(r *recipe.Recipe, outputDir string) (*BuildResult, error) {
	workspace, err := os.MkdirTemp("", "gale-build-*")
	if err != nil {
		return nil, fmt.Errorf("create workspace: %w", err)
	}
	defer os.RemoveAll(workspace)

	// Fetch source tarball.
	tarballPath := filepath.Join(workspace, "source.tar.gz")
	if err := download.Fetch(r.Source.URL, tarballPath); err != nil {
		return nil, fmt.Errorf("fetch source: %w", err)
	}

	// Verify source SHA256.
	if err := download.VerifySHA256(tarballPath, r.Source.SHA256); err != nil {
		return nil, fmt.Errorf("verify source: %w", err)
	}

	// Extract source.
	srcDir := filepath.Join(workspace, "src")
	if err := download.ExtractTarGz(tarballPath, srcDir); err != nil {
		return nil, fmt.Errorf("extract source: %w", err)
	}

	// Detect single top-level directory.
	sourceRoot, err := detectSourceRoot(srcDir)
	if err != nil {
		return nil, fmt.Errorf("detect source root: %w", err)
	}

	// Create prefix directory.
	prefixDir := filepath.Join(workspace, "prefix")
	if err := os.MkdirAll(prefixDir, 0o755); err != nil {
		return nil, fmt.Errorf("create prefix directory: %w", err)
	}

	// Run build steps.
	jobs := strconv.Itoa(runtime.NumCPU())
	for _, step := range r.Build.Steps {
		if err := runStep(step, sourceRoot, prefixDir, jobs); err != nil {
			return nil, err
		}
	}

	// Package prefix as tar.zst.
	archiveName := fmt.Sprintf("%s-%s.tar.zst", r.Package.Name, r.Package.Version)
	archivePath := filepath.Join(outputDir, archiveName)
	if err := download.CreateTarZstd(prefixDir, archivePath); err != nil {
		return nil, fmt.Errorf("create archive: %w", err)
	}

	// Compute SHA256 of the archive.
	hash, err := computeSHA256(archivePath)
	if err != nil {
		return nil, fmt.Errorf("hash archive: %w", err)
	}

	return &BuildResult{
		Archive: archivePath,
		SHA256:  hash,
	}, nil
}

// detectSourceRoot returns the source root directory. If the
// extracted source contains exactly one top-level subdirectory,
// that directory is returned. Otherwise srcDir itself is returned.
func detectSourceRoot(srcDir string) (string, error) {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return "", fmt.Errorf("read source directory: %w", err)
	}

	var dirs []os.DirEntry
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e)
		}
	}

	if len(dirs) == 1 && len(entries) == 1 {
		return filepath.Join(srcDir, dirs[0].Name()), nil
	}

	return srcDir, nil
}

// runStep executes a single build step using sh -c with PREFIX
// and JOBS environment variables set.
func runStep(step, sourceRoot, prefixDir, jobs string) error {
	cmd := exec.Command("sh", "-c", step)
	cmd.Dir = sourceRoot
	cmd.Env = append(os.Environ(),
		"PREFIX="+prefixDir,
		"JOBS="+jobs,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build step %q failed: %s\n%s", step, err, output)
	}

	return nil
}

// computeSHA256 returns the hex-encoded SHA256 hash of the file
// at the given path.
func computeSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
