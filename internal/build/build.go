package build

import (
	"errors"
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
	"github.com/kelp/gale/internal/gitutil"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/recipe"
)

// out is the build output writer. Initialized to colored
// stderr output. Callers can override with SetOutput.
var out = output.New(os.Stderr, true)

func init() {
	download.ProgressPrefix = out.StepPrefix()
}

// SetOutput overrides the build output writer.
func SetOutput(o *output.Output) {
	out = o
}

// BuildResult holds the output of a successful build.
type BuildResult struct {
	Archive string // path to the tar.zst file
	SHA256  string // hex-encoded hash of the archive
}

// ErrUnsupportedPlatform is returned when a recipe restricts
// platforms and the current platform is not in the list.
var ErrUnsupportedPlatform = errors.New("unsupported platform")

// checkPlatform returns an error if the recipe restricts
// platforms and the current platform is not in the list.
func checkPlatform(r *recipe.Recipe) error {
	if len(r.Package.Platforms) == 0 {
		return nil // no restriction
	}
	current := runtime.GOOS + "-" + runtime.GOARCH
	for _, p := range r.Package.Platforms {
		if p == current {
			return nil
		}
	}
	return fmt.Errorf("%s: %w (%s not in %v)",
		r.Package.Name, ErrUnsupportedPlatform,
		current, r.Package.Platforms)
}

// Build builds a recipe from source and packages the result.
// outputDir is where the tar.zst will be written. Optional
// extraPaths are prepended to the build environment PATH.
func Build(r *recipe.Recipe, outputDir string, debug bool, deps *BuildDeps) (*BuildResult, error) {
	if err := checkPlatform(r); err != nil {
		return nil, err
	}

	workspace, err := os.MkdirTemp(TmpDir(), "gale-build-*")
	if err != nil {
		return nil, fmt.Errorf("create workspace: %w", err)
	}
	defer os.RemoveAll(workspace)

	// Fetch source tarball (check cache first).
	// Preserve the archive extension so ExtractSource
	// can detect the correct format.
	tarballPath := filepath.Join(workspace, "source"+sourceExtension(r.Source.URL))
	cached := false
	if cacheDir := sourceCache(); cacheDir != "" {
		cachedFile := filepath.Join(cacheDir, r.Source.SHA256)
		if _, err := os.Stat(cachedFile); err == nil {
			out.Step(fmt.Sprintf("Using cached source (%s)",
				r.Source.SHA256[:12]))
			if err := copyFile(cachedFile, tarballPath); err == nil {
				cached = true
			}
		}
	}
	if !cached {
		if err := download.Fetch(r.Source.URL, tarballPath); err != nil {
			return nil, fmt.Errorf("fetch source: %w", err)
		}
	}

	// Verify source SHA256.
	out.Step("Verifying SHA256...")
	if err := download.VerifySHA256(tarballPath, r.Source.SHA256); err != nil {
		return nil, fmt.Errorf("verify source: %w", err)
	}

	// Save to cache after successful verify.
	if !cached {
		if cacheDir := sourceCache(); cacheDir != "" {
			cachedFile := filepath.Join(cacheDir, r.Source.SHA256)
			_ = copyFile(tarballPath, cachedFile)
		}
	}

	// Extract source.
	out.Step("Extracting source...")
	srcDir := filepath.Join(workspace, "src")
	if err := download.ExtractSource(tarballPath, srcDir); err != nil {
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

	return buildFromDir(r, sourceRoot, workspace, outputDir, debug, deps)
}

// BuildLocal builds a recipe using a local source directory
// instead of downloading. The source directory is used as the
// build root directly.
func BuildLocal(r *recipe.Recipe, sourceDir, outputDir string, debug bool, deps *BuildDeps) (*BuildResult, error) {
	if err := checkPlatform(r); err != nil {
		return nil, err
	}

	workspace, err := os.MkdirTemp(TmpDir(), "gale-build-*")
	if err != nil {
		return nil, fmt.Errorf("create workspace: %w", err)
	}
	defer os.RemoveAll(workspace)

	return buildFromDir(r, sourceDir, workspace, outputDir, debug, deps)
}

// buildFromDir runs build steps, fixes binaries, and packages
// the result. Shared by Build and BuildLocal.
func buildFromDir(r *recipe.Recipe, sourceDir, workspace, outputDir string, debug bool, deps *BuildDeps) (*BuildResult, error) {
	prefixDir := filepath.Join(workspace, "prefix")
	if err := os.MkdirAll(prefixDir, 0o755); err != nil {
		return nil, fmt.Errorf("create prefix directory: %w", err)
	}

	jobs := strconv.Itoa(runtime.NumCPU())
	buildCfg := r.BuildForPlatform(runtime.GOOS, runtime.GOARCH)
	version := r.Package.Version
	for i, step := range buildCfg.Steps {
		out.Step(fmt.Sprintf("[%d/%d] %s",
			i+1, len(buildCfg.Steps), step))
		if err := runStep(step, sourceDir, prefixDir, jobs, version, buildCfg.System, debug, deps); err != nil {
			return nil, err
		}
	}

	out.Step("Fixing library paths...")
	if err := FixupBinaries(prefixDir); err != nil {
		return nil, fmt.Errorf("fixup binaries: %w", err)
	}

	if err := fixupShebangs(prefixDir); err != nil {
		return nil, fmt.Errorf("fixup shebangs: %w", err)
	}

	archiveName := fmt.Sprintf("%s-%s.tar.zst", r.Package.Name, r.Package.Version)
	archivePath := filepath.Join(outputDir, archiveName)
	if err := download.CreateTarZstd(prefixDir, archivePath); err != nil {
		return nil, fmt.Errorf("create archive: %w", err)
	}

	hash, err := download.HashFile(archivePath)
	if err != nil {
		return nil, fmt.Errorf("hash archive: %w", err)
	}

	return &BuildResult{
		Archive: archivePath,
		SHA256:  hash,
	}, nil
}

// BuildGit clones a git repo and builds from the clone.
// Returns the build result and the short commit hash used
// as the version. The recipe's version is overridden with
// the hash.
func BuildGit(r *recipe.Recipe, outputDir string, debug bool, deps *BuildDeps) (*BuildResult, string, error) {
	if err := checkPlatform(r); err != nil {
		return nil, "", err
	}

	if r.Source.Repo == "" {
		return nil, "", fmt.Errorf("no source.repo for git build")
	}

	cloneDir, err := os.MkdirTemp(TmpDir(), "gale-git-*")
	if err != nil {
		return nil, "", fmt.Errorf("create clone dir: %w", err)
	}
	defer os.RemoveAll(cloneDir)

	out.Step(fmt.Sprintf("Cloning %s...", r.Source.Repo))
	hash, err := gitutil.Clone(r.Source.Repo, cloneDir, r.Source.Branch)
	if err != nil {
		return nil, "", fmt.Errorf("clone: %w", err)
	}

	r.Package.Version = hash
	result, err := BuildLocal(r, cloneDir, outputDir, debug, deps)
	if err != nil {
		return nil, "", err
	}

	return result, hash, nil
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
func runStep(step, sourceRoot, prefixDir, jobs, version, system string, debug bool, deps *BuildDeps) error {
	cmd := exec.Command("sh", "-c", step)
	cmd.Dir = sourceRoot
	cmd.Env = buildEnv(prefixDir, jobs, version, system, debug, deps)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build step %q failed: %s", step, err)
	}

	return nil
}

// BuildDeps holds paths from installed build dependencies,
// used to construct the build environment.
type BuildDeps struct {
	BinDirs   []string // bin/ dirs for PATH
	StoreDirs []string // root store dirs for lib/include/pkgconfig
}

// SystemDeps returns implicit build dependencies for
// a build system. These are added alongside explicit
// [dependencies.build] entries.
func SystemDeps(system string) []string {
	switch system {
	case "cmake":
		return []string{"cmake"}
	case "go":
		return []string{"go"}
	case "cargo":
		return []string{"rust"}
	case "zig":
		return []string{"zig"}
	case "python":
		return []string{"python"}
	case "ruby":
		return []string{"ruby"}
	default:
		return nil
	}
}

// buildEnv constructs a minimal, clean environment for build steps.
// Resolves build tool locations from the host PATH so nix-installed
// compilers work, without pulling in the full nix coreutils.
func buildEnv(prefixDir, jobs, version, system string, debug bool, deps *BuildDeps) []string {
	home := os.Getenv("HOME")
	toolsDir, err := os.MkdirTemp(TmpDir(), "gale-tools-*")
	if err != nil {
		toolsDir = filepath.Join(os.TempDir(), "gale-tools")
		_ = os.MkdirAll(toolsDir, 0o755)
	}
	path := buildPath(home, toolsDir)
	if deps != nil && len(deps.BinDirs) > 0 {
		path = strings.Join(deps.BinDirs, ":") + ":" + path
	}
	tmpdir := os.Getenv("TMPDIR")
	if tmpdir == "" {
		tmpdir = "/tmp"
	}
	env := []string{
		"PREFIX=" + prefixDir,
		"VERSION=" + version,
		"JOBS=" + jobs,
		"PATH=" + path,
		"HOME=" + home,
		"TMPDIR=" + tmpdir,
		"LANG=en_US.UTF-8",
	}

	// Platform variables for use in build steps.
	env = append(env,
		"OS="+runtime.GOOS,
		"ARCH="+runtime.GOARCH,
		"PLATFORM="+runtime.GOOS+"-"+runtime.GOARCH,
	)

	// Build library/include/pkgconfig paths from dep
	// store directories.
	if deps != nil && len(deps.StoreDirs) > 0 {
		var libPaths, incPaths, pcPaths []string
		for _, d := range deps.StoreDirs {
			libPaths = append(libPaths,
				filepath.Join(d, "lib"))
			incPaths = append(incPaths,
				filepath.Join(d, "include"))
			pcPaths = append(pcPaths,
				filepath.Join(d, "lib", "pkgconfig"))
		}
		libPathStr := strings.Join(libPaths, ":")
		incPathStr := strings.Join(incPaths, ":")
		env = append(env,
			"LIBRARY_PATH="+libPathStr,
			"C_INCLUDE_PATH="+incPathStr,
			"PKG_CONFIG_PATH="+strings.Join(pcPaths, ":"),
			"CMAKE_LIBRARY_PATH="+libPathStr,
			"CMAKE_INCLUDE_PATH="+incPathStr)

		switch runtime.GOOS {
		case "linux":
			env = append(env, "LD_LIBRARY_PATH="+libPathStr)
		case "darwin":
			env = append(env,
				"DYLD_FALLBACK_LIBRARY_PATH="+libPathStr)
		}

		// cmake uses semicolons for CMAKE_PREFIX_PATH.
		if system == "cmake" {
			env = append(env,
				"CMAKE_PREFIX_PATH="+strings.Join(
					deps.StoreDirs, ";"))
		}
	}

	// Pass through compiler if set.
	if cc := os.Getenv("CC"); cc != "" {
		env = append(env, "CC="+cc)
	}
	if cxx := os.Getenv("CXX"); cxx != "" {
		env = append(env, "CXX="+cxx)
	}

	// Default compiler flags. User-set values take
	// precedence — only set if not already in the
	// environment.
	if debug {
		setDefault(&env, "CFLAGS", "-O0 -g")
		setDefault(&env, "CXXFLAGS", "-O0 -g")
		setDefault(&env, "LDFLAGS", "")
	} else {
		setDefault(&env, "CFLAGS", "-O2")
		setDefault(&env, "CXXFLAGS", "-O2")
		setDefault(&env, "LDFLAGS", "-Wl,-S")
	}

	// Deterministic ar timestamps.
	env = append(env, "ZERO_AR_DATE=1")

	return env
}

// setDefault appends key=val to env only if key is not
// already set in the host environment.
func setDefault(env *[]string, key, val string) {
	if os.Getenv(key) != "" {
		*env = append(*env, key+"="+os.Getenv(key))
		return
	}
	*env = append(*env, key+"="+val)
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
	tools := []string{"go", "cargo", "rustc", "cmake", "autoconf", "automake", "libtool", "meson", "ninja", "zig", "python3", "pip3", "ruby", "gem"}
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
		_ = os.Chtimes(path, now, now) //nolint:gosec // G122 — best-effort timestamp reset, race is acceptable
		return nil
	})
}

// fixupShebangs rewrites shebangs in scripts under
// prefixDir/bin/ that reference the build prefix. Replaces
// them with #!/usr/bin/env <interpreter> so scripts work
// after the prefix is moved to the store.
func fixupShebangs(prefixDir string) error {
	binDir := filepath.Join(prefixDir, "bin")
	if _, err := os.Stat(binDir); os.IsNotExist(err) {
		return nil // no bin/ directory
	} else if err != nil {
		return fmt.Errorf("stat bin dir: %w", err)
	}

	entries, err := os.ReadDir(binDir)
	if err != nil {
		return fmt.Errorf("read bin dir: %w", err)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(binDir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue // skip unreadable
		}
		if len(data) < 2 || data[0] != '#' || data[1] != '!' {
			continue // not a script
		}

		// Find end of shebang line.
		newline := strings.IndexByte(string(data), '\n')
		if newline < 0 {
			continue
		}
		shebang := string(data[:newline])

		// Only fix shebangs that reference the build prefix.
		if !strings.Contains(shebang, prefixDir) {
			continue
		}

		// Extract the interpreter basename.
		interp := filepath.Base(strings.TrimPrefix(shebang, "#!"))
		interp = strings.TrimSpace(interp)

		newShebang := "#!/usr/bin/env " + interp
		newData := []byte(newShebang + string(data[newline:]))

		info, _ := e.Info()
		mode := info.Mode()
		if err := os.WriteFile(path, newData, mode); err != nil { //nolint:gosec
			return fmt.Errorf("rewrite shebang %s: %w",
				e.Name(), err)
		}
	}

	return nil
}

// TmpDir returns the path to ~/.gale/tmp/, creating it
// if needed. Falls back to system temp if unavailable.
func TmpDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, ".gale", "tmp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	return dir
}

// sourceCache returns the path to ~/.gale/cache/, creating
// it if needed. Returns empty string if unavailable.
func sourceCache() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, ".gale", "cache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	return dir
}

// sourceExtension extracts the archive extension from a URL.
// Handles compound extensions like .tar.gz and .tar.xz.
// Falls back to .tar.gz for unrecognized formats.
func sourceExtension(url string) string {
	base := filepath.Base(url)
	for _, ext := range []string{
		".tar.gz", ".tar.xz", ".tar.bz2", ".tar.zst",
	} {
		if strings.HasSuffix(base, ext) {
			return ext
		}
	}
	for _, ext := range []string{".tgz", ".zip"} {
		if strings.HasSuffix(base, ext) {
			return ext
		}
	}
	return ".tar.gz"
}

// copyFile copies src to dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	out, err := os.Create(dst)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}

	return out.Close()
}
