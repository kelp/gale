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
	"strings"
	"time"

	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/recipe"
)

// BuildResult holds the output of a successful build.
type BuildResult struct {
	Archive string // path to the tar.zst file
	SHA256  string // hex-encoded hash of the archive
}

// Build builds a recipe from source and packages the result.
// outputDir is where the tar.zst will be written. Optional
// extraPaths are prepended to the build environment PATH.
func Build(r *recipe.Recipe, outputDir string, extraPaths ...string) (*BuildResult, error) {
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

	// Reset file timestamps to avoid autotools clock-skew errors.
	if err := touchAll(srcDir); err != nil {
		return nil, fmt.Errorf("reset timestamps: %w", err)
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
	build := r.BuildForPlatform(runtime.GOOS, runtime.GOARCH)
	for _, step := range build.Steps {
		if err := runStep(step, sourceRoot, prefixDir, jobs, extraPaths); err != nil {
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
// and JOBS environment variables set. Uses a clean environment
// with only essential variables to avoid interference from the
// host environment (e.g., nix coreutils aliases).
func runStep(step, sourceRoot, prefixDir, jobs string, extraPaths []string) error {
	cmd := exec.Command("sh", "-c", step)
	cmd.Dir = sourceRoot
	cmd.Env = buildEnv(prefixDir, jobs, extraPaths)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build step %q failed: %s\n%s", step, err, output)
	}

	return nil
}

// buildEnv constructs a minimal, clean environment for build steps.
// Resolves build tool locations from the host PATH so nix-installed
// compilers work, without pulling in the full nix coreutils.
func buildEnv(prefixDir, jobs string, extraPaths []string) []string {
	home := os.Getenv("HOME")
	toolsDir, err := os.MkdirTemp("", "gale-tools-*")
	if err != nil {
		toolsDir = filepath.Join(os.TempDir(), "gale-tools")
		_ = os.MkdirAll(toolsDir, 0o755)
	}
	path := buildPath(home, toolsDir)
	if len(extraPaths) > 0 {
		path = strings.Join(extraPaths, ":") + ":" + path
	}
	tmpdir := os.Getenv("TMPDIR")
	if tmpdir == "" {
		tmpdir = "/tmp"
	}
	env := []string{
		"PREFIX=" + prefixDir,
		"JOBS=" + jobs,
		"PATH=" + path,
		"HOME=" + home,
		"TMPDIR=" + tmpdir,
		"LANG=en_US.UTF-8",
	}
	// Pass through compiler if set.
	if cc := os.Getenv("CC"); cc != "" {
		env = append(env, "CC="+cc)
	}
	if cxx := os.Getenv("CXX"); cxx != "" {
		env = append(env, "CXX="+cxx)
	}
	return env
}

// buildPath constructs the PATH for build steps. Creates an
// isolated tools directory with symlinks to resolved build
// tools, avoiding importing directories that may contain
// non-standard coreutils (e.g. nix vibeutils) that break
// autotools.
func buildPath(home, toolsDir string) string {
	base := []string{
		home + "/.gale/bin",
		home + "/.cargo/bin",
		"/opt/homebrew/bin",
		"/usr/local/bin",
		"/usr/bin",
		"/bin",
		"/usr/sbin",
		"/sbin",
	}

	// Resolve common build tools from the host environment.
	// If a tool lives in a well-known base directory, no
	// symlink is needed. Otherwise, symlink just that binary
	// into toolsDir to avoid pulling in the whole directory.
	tools := []string{"go", "cargo", "rustc", "cmake", "autoconf", "automake", "libtool"}
	baseSet := map[string]bool{}
	for _, d := range base {
		baseSet[d] = true
	}

	var resolved []string
	for _, tool := range tools {
		p, err := exec.LookPath(tool)
		if err != nil {
			continue
		}
		if baseSet[filepath.Dir(p)] {
			continue
		}
		resolved = append(resolved, p)
	}

	resolveTools(toolsDir, resolved)

	// Prepend toolsDir so isolated symlinks take priority.
	return strings.Join(append([]string{toolsDir}, base...), ":")
}

// resolveTools creates symlinks in toolsDir pointing to each
// resolved tool path. This isolates individual binaries from
// directories that may contain incompatible coreutils.
func resolveTools(toolsDir string, toolPaths []string) {
	for _, p := range toolPaths {
		name := filepath.Base(p)
		link := filepath.Join(toolsDir, name)
		_ = os.Symlink(p, link) // best effort
	}
}

// touchAll resets all file modification times under dir to now.
// Prevents autotools clock-skew errors after extracting tarballs.
func touchAll(dir string) error {
	now := time.Now()
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil //nolint:nilerr // best-effort: skip broken symlinks
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil // skip symlinks
		}
		_ = os.Chtimes(path, now, now) // best effort
		return nil
	})
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
