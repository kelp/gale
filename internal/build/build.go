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

	buildCfg := r.BuildForPlatform(runtime.GOOS, runtime.GOARCH)
	bc := &BuildContext{
		PrefixDir: prefixDir,
		SourceDir: sourceDir,
		Jobs:      strconv.Itoa(runtime.NumCPU()),
		Version:   r.Package.Version,
		System:    buildCfg.System,
		Toolchain: buildCfg.Toolchain,
		Debug:     debug,
		Env:       buildCfg.Env,
		Deps:      deps,
	}
	for i, step := range buildCfg.Steps {
		out.Step(fmt.Sprintf("[%d/%d] %s",
			i+1, len(buildCfg.Steps), step))
		if err := runStep(bc, step); err != nil {
			return nil, err
		}
	}

	out.Step("Fixing library paths...")
	if err := FixupBinaries(prefixDir); err != nil {
		return nil, fmt.Errorf("fixup binaries: %w", err)
	}

	// Add rpath entries for dependency store dirs so
	// binaries can find dep dylibs at runtime.
	var depStoreDirs []string
	if deps != nil {
		depStoreDirs = deps.StoreDirs
	}
	if err := AddDepRpaths(prefixDir, depStoreDirs); err != nil {
		return nil, fmt.Errorf("add dep rpaths: %w", err)
	}

	if err := FixupPkgConfig(prefixDir); err != nil {
		return nil, fmt.Errorf("fixup pkg-config: %w", err)
	}

	if err := fixupShebangs(prefixDir); err != nil {
		return nil, fmt.Errorf("fixup shebangs: %w", err)
	}

	// Replace hardcoded build prefix in text files with a
	// placeholder. At install time, RestorePrefixPlaceholder
	// replaces it with the actual store path.
	if err := ReplacePrefixInTextFiles(prefixDir, PrefixPlaceholder); err != nil {
		return nil, fmt.Errorf("fixup text prefix paths: %w", err)
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

	if len(dirs) == 1 {
		return filepath.Join(srcDir, dirs[0].Name()), nil
	}

	return srcDir, nil
}

// runStep executes a single build step using sh -c with PREFIX
// and JOBS environment variables set. Uses a clean environment
// with only essential variables to avoid interference from the
// host environment (e.g., nix coreutils aliases).
func runStep(bc *BuildContext, step string) error {
	env, cleanup, err := buildEnv(bc)
	if err != nil {
		return fmt.Errorf("build environment: %w", err)
	}
	defer cleanup()

	cmd := exec.Command("sh", "-c", step)
	cmd.Dir = bc.SourceDir
	cmd.Env = env
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build step %q failed: %w", step, err)
	}

	return nil
}

// BuildDeps holds paths from installed build dependencies,
// used to construct the build environment.
type BuildDeps struct {
	BinDirs   []string          // bin/ dirs for PATH
	StoreDirs []string          // root store dirs for lib/include/pkgconfig
	NamedDirs map[string]string // dep name → store directory
}

// BuildContext holds parameters passed through build steps
// and environment construction.
type BuildContext struct {
	PrefixDir string
	SourceDir string
	Jobs      string
	Version   string
	System    string
	Toolchain string
	Debug     bool
	Env       map[string]string // extra env vars from recipe [build] env
	Deps      *BuildDeps
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

// baseEnv returns the core environment variables: PREFIX,
// VERSION, JOBS, HOME, TMPDIR, LANG, PATH, OS, ARCH, PLATFORM.
func (bc *BuildContext) baseEnv(home, path, tmpdir string) []string {
	return []string{
		"PREFIX=" + bc.PrefixDir,
		"VERSION=" + bc.Version,
		"JOBS=" + bc.Jobs,
		"PATH=" + path,
		"HOME=" + home,
		"TMPDIR=" + tmpdir,
		"LANG=en_US.UTF-8",
		"OS=" + runtime.GOOS,
		"ARCH=" + runtime.GOARCH,
		"PLATFORM=" + runtime.GOOS + "-" + runtime.GOARCH,
	}
}

func joinFlags(parts ...string) string {
	var out []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return strings.Join(out, " ")
}

func llvmToolchainFlags(goos, llvmDir string) (cppflags, cxxflags, ldflags string) {
	if goos != "linux" || llvmDir == "" {
		return "", "", ""
	}

	includeDir := filepath.Join(llvmDir, "include", "c++", "v1")
	libDir := filepath.Join(llvmDir, "lib")
	if _, err := os.Stat(includeDir); err == nil {
		cppflags = "-isystem " + includeDir
	}
	cxxflags = "-stdlib=libc++"

	var ldParts []string
	if _, err := os.Stat(libDir); err == nil {
		ldParts = append(ldParts,
			"-L"+libDir,
			"-Wl,-rpath,"+libDir,
		)
	}
	ldParts = append(ldParts, "-fuse-ld=lld", "-stdlib=libc++")
	ldflags = strings.Join(ldParts, " ")
	return cppflags, cxxflags, ldflags
}

func (bc *BuildContext) toolchainCompilerFlags(goos string) (cppflags, cxxflags, ldflags string) {
	if os.Getenv("CXX") != "" || os.Getenv("LD") != "" {
		return "", "", ""
	}
	if bc.Env != nil && (bc.Env["CXX"] != "" || bc.Env["LD"] != "") {
		return "", "", ""
	}
	if bc.Toolchain == "llvm" && bc.Deps != nil && bc.Deps.NamedDirs != nil {
		return llvmToolchainFlags(goos, bc.Deps.NamedDirs["llvm"])
	}
	return "", "", ""
}

func (bc *BuildContext) toolchainEnv() ([]string, error) {
	switch bc.Toolchain {
	case "":
		return nil, nil
	case "llvm":
		if bc.Deps == nil || bc.Deps.NamedDirs == nil {
			return nil, fmt.Errorf("llvm toolchain requires build dependency llvm")
		}
		llvmDir := bc.Deps.NamedDirs["llvm"]
		if llvmDir == "" {
			return nil, fmt.Errorf("llvm toolchain requires build dependency llvm")
		}
		binDir := filepath.Join(llvmDir, "bin")
		var env []string
		setDefault(&env, "CC", filepath.Join(binDir, "clang"))
		setDefault(&env, "CXX", filepath.Join(binDir, "clang++"))
		setDefault(&env, "AR", filepath.Join(binDir, "llvm-ar"))
		setDefault(&env, "NM", filepath.Join(binDir, "llvm-nm"))
		setDefault(&env, "RANLIB", filepath.Join(binDir, "llvm-ranlib"))
		if _, err := os.Stat(filepath.Join(binDir, "ld.lld")); err == nil {
			setDefault(&env, "LD", filepath.Join(binDir, "ld.lld"))
		}
		return env, nil
	default:
		return nil, fmt.Errorf("unknown toolchain %q", bc.Toolchain)
	}
}

// compilerFlags generates CFLAGS, CXXFLAGS, LDFLAGS, CPPFLAGS,
// CC, CXX pass-through, and ZERO_AR_DATE.
func (bc *BuildContext) compilerFlags(depCPPFLAGS, depLDFLAGS string) []string {
	var env []string

	// Pass through compiler if set.
	if cc := os.Getenv("CC"); cc != "" {
		env = append(env, "CC="+cc)
	}
	if cxx := os.Getenv("CXX"); cxx != "" {
		env = append(env, "CXX="+cxx)
	}

	toolchainCPPFLAGS, toolchainCXXFLAGS, toolchainLDFLAGS := bc.toolchainCompilerFlags(runtime.GOOS)

	// On macOS, always add headerpad so install_name_tool
	// can add LC_RPATH entries post-build.
	headerpad := ""
	if runtime.GOOS == "darwin" {
		headerpad = "-Wl,-headerpad_max_install_names"
	}
	// On Linux, always compile with -fPIC so static
	// libraries can be linked into shared objects.
	fpic := ""
	if runtime.GOOS == "linux" {
		fpic = "-fPIC"
	}
	if bc.Debug {
		setDefault(&env, "CFLAGS", joinFlags("-O0", "-g", fpic))
		setDefault(&env, "CXXFLAGS", joinFlags("-O0", "-g", fpic, toolchainCXXFLAGS))
		setDefault(&env, "LDFLAGS", joinFlags(depLDFLAGS, toolchainLDFLAGS, headerpad))
	} else {
		setDefault(&env, "CFLAGS", joinFlags("-O2", fpic))
		setDefault(&env, "CXXFLAGS", joinFlags("-O2", fpic, toolchainCXXFLAGS))
		setDefault(&env, "LDFLAGS", joinFlags(depLDFLAGS, toolchainLDFLAGS, "-Wl,-S", headerpad))
	}
	if cppflags := joinFlags(toolchainCPPFLAGS, depCPPFLAGS); cppflags != "" {
		setDefault(&env, "CPPFLAGS", cppflags)
	}

	// Deterministic ar timestamps.
	env = append(env, "ZERO_AR_DATE=1")

	return env
}

// perDepEnv generates per-dependency environment variables:
// DEP_<NAME>=<path>, PYTHONPATH, DEP_CPPFLAGS, DEP_LDFLAGS.
// Returns the env slice and the raw depCPPFLAGS/depLDFLAGS
// strings for use by compilerFlags.
func (bc *BuildContext) perDepEnv() (env []string, depCPPFLAGS, depLDFLAGS string) {
	deps := bc.Deps
	if deps == nil {
		return nil, "", ""
	}

	// DEP_<NAME>=<store_dir>.
	for name, dir := range deps.NamedDirs {
		key := "DEP_" + strings.ToUpper(
			strings.ReplaceAll(name, "-", "_"))
		env = append(env, key+"="+dir)
	}

	// Discover Python site-packages for PYTHONPATH.
	var pyPaths []string
	for _, d := range deps.StoreDirs {
		matches, _ := filepath.Glob(
			filepath.Join(d, "lib", "python*", "site-packages"))
		pyPaths = append(pyPaths, matches...)
	}
	if len(pyPaths) > 0 {
		env = append(env,
			"PYTHONPATH="+strings.Join(pyPaths, ":"))
	}

	// Compute DEP_CPPFLAGS / DEP_LDFLAGS from dep include/lib.
	if len(deps.StoreDirs) > 0 {
		var cppParts, ldParts []string
		for _, d := range deps.StoreDirs {
			incDir := filepath.Join(d, "include")
			libDir := filepath.Join(d, "lib")
			if _, err := os.Stat(incDir); err == nil {
				cppParts = append(cppParts, "-I"+incDir)
			}
			if _, err := os.Stat(libDir); err == nil {
				ldParts = append(ldParts, "-L"+libDir)
				// On darwin, also inject -Wl,-rpath so that
				// binaries built during make can find dep
				// dylibs immediately. SIP strips DYLD_* vars
				// from /bin/sh children, so link-time rpath
				// is the only reliable mechanism during the
				// build phase (e.g. Python test-loads _lzma).
				//
				// AddDepRpaths still runs post-build as the
				// authority — it catches cases where build
				// systems strip link-time rpaths, and
				// existingRpaths() deduplicates.
				if runtime.GOOS == "darwin" {
					ldParts = append(ldParts,
						"-Wl,-rpath,"+libDir)
				}
			}
		}
		depCPPFLAGS = strings.Join(cppParts, " ")
		depLDFLAGS = strings.Join(ldParts, " ")
	}
	if depCPPFLAGS != "" {
		env = append(env, "DEP_CPPFLAGS="+depCPPFLAGS)
	}
	if depLDFLAGS != "" {
		env = append(env, "DEP_LDFLAGS="+depLDFLAGS)
	}

	return env, depCPPFLAGS, depLDFLAGS
}

// depSearchPaths computes library, include, pkg-config, and
// cmake prefix paths from dependency store directories.
//
// Only existing directories are included. Stale or
// nonexistent paths would trigger linker warnings
// ("search path ... not found") that break configure
// scripts with strict LDFLAGS validation (e.g. Ruby).
func (bc *BuildContext) depSearchPaths() (libPath, incPath, pcPath, cmakePath string) {
	deps := bc.Deps
	if deps == nil || len(deps.StoreDirs) == 0 {
		return "", "", "", ""
	}
	var libPaths, incPaths, pcPaths []string
	for _, d := range deps.StoreDirs {
		libDir := filepath.Join(d, "lib")
		incDir := filepath.Join(d, "include")
		pcDir := filepath.Join(d, "lib", "pkgconfig")
		if _, err := os.Stat(libDir); err == nil {
			libPaths = append(libPaths, libDir)
		}
		if _, err := os.Stat(incDir); err == nil {
			incPaths = append(incPaths, incDir)
		}
		if _, err := os.Stat(pcDir); err == nil {
			pcPaths = append(pcPaths, pcDir)
		}
	}
	libPath = strings.Join(libPaths, ":")
	incPath = strings.Join(incPaths, ":")
	pcPath = strings.Join(pcPaths, ":")
	// cmakePath is intentionally not filtered: CMAKE_PREFIX_PATH
	// entries are prefixes, not subdirectories. CMake walks
	// each prefix looking for lib/cmake/<pkg>, share/cmake/<pkg>,
	// etc. and tolerates missing subdirs silently. The store
	// dirs themselves always exist.
	if bc.System == "cmake" {
		cmakePath = strings.Join(deps.StoreDirs, ";")
	}
	return libPath, incPath, pcPath, cmakePath
}

// buildEnv constructs a minimal, clean environment for build steps.
// Resolves build tool locations from the host PATH so nix-installed
// compilers work, without pulling in the full nix coreutils.
func buildEnv(bc *BuildContext) ([]string, func(), error) {
	deps := bc.Deps
	home := os.Getenv("HOME")
	toolsDir, err := os.MkdirTemp(TmpDir(), "gale-tools-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create tools directory: %w", err)
	}
	cleanup := func() { os.RemoveAll(toolsDir) }
	path := buildPath(home, toolsDir)
	if deps != nil && len(deps.BinDirs) > 0 {
		path = strings.Join(deps.BinDirs, ":") + ":" + path
	}
	tmpdir := os.Getenv("TMPDIR")
	if tmpdir == "" {
		tmpdir = "/tmp"
	}
	env := bc.baseEnv(home, path, tmpdir)

	// Dependency search paths. Each guard is independent
	// because depSearchPaths filters nonexistent subdirs:
	// a header-only dep (no lib/) still needs incPath, and
	// a bin-only dep (no lib/ or include/) still needs
	// CMAKE_PREFIX_PATH if system=cmake.
	libPathStr, incPathStr, pcPathStr, cmakePrefix := bc.depSearchPaths()
	if libPathStr != "" {
		env = append(env,
			"LIBRARY_PATH="+libPathStr,
			"CMAKE_LIBRARY_PATH="+libPathStr)
		switch runtime.GOOS {
		case "linux":
			env = append(env, "LD_LIBRARY_PATH="+libPathStr)
		case "darwin":
			env = append(env,
				"DYLD_FALLBACK_LIBRARY_PATH="+libPathStr)
		}
	}
	if incPathStr != "" {
		env = append(env,
			"C_INCLUDE_PATH="+incPathStr,
			"CMAKE_INCLUDE_PATH="+incPathStr)
	}
	if pcPathStr != "" {
		env = append(env, "PKG_CONFIG_PATH="+pcPathStr)
	}
	if cmakePrefix != "" {
		env = append(env, "CMAKE_PREFIX_PATH="+cmakePrefix)
	}

	// Per-dep env vars and dep compiler flags.
	perDep, depCPPFLAGS, depLDFLAGS := bc.perDepEnv()
	env = append(env, perDep...)

	toolchainEnv, err := bc.toolchainEnv()
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	env = append(env, toolchainEnv...)

	// Compiler flags (CC/CXX pass-through, CFLAGS, LDFLAGS,
	// CPPFLAGS, ZERO_AR_DATE).
	env = append(env, bc.compilerFlags(depCPPFLAGS, depLDFLAGS)...)

	// Recipe-defined env vars (from [build] env = {...}).
	// Expand ${PREFIX}, ${VERSION}, ${JOBS} placeholders.
	for k, v := range bc.Env {
		v = strings.ReplaceAll(v, "${PREFIX}", bc.PrefixDir)
		v = strings.ReplaceAll(v, "${VERSION}", bc.Version)
		v = strings.ReplaceAll(v, "${JOBS}", bc.Jobs)
		env = append(env, k+"="+v)
	}

	return env, cleanup, nil
}

// setDefault appends key=val to env only if key is not
// already present in the env slice or the host environment.
func setDefault(env *[]string, key, val string) {
	prefix := key + "="
	for _, e := range *env {
		if strings.HasPrefix(e, prefix) {
			return
		}
	}
	if hostVal := os.Getenv(key); hostVal != "" {
		*env = append(*env, key+"="+hostVal)
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
	if _, err := os.Stat(binDir); errors.Is(err, os.ErrNotExist) {
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

// PrefixPlaceholder is embedded in text files during build
// to replace the build-time temp prefix. At install time,
// RestorePrefixPlaceholder replaces it with the actual store
// path. This avoids hardcoded temp paths in scripts.
const PrefixPlaceholder = "@@GALE_PREFIX@@"

// ReplacePrefixInTextFiles walks prefixDir and replaces all
// occurrences of buildPrefix in text files with replacement.
// Binary files (those containing null bytes in the first 512
// bytes) are skipped. Directories scanned: bin, sbin,
// libexec, share, etc, lib (for .la files and scripts).
func ReplacePrefixInTextFiles(prefixDir, replacement string) error {
	dirs := []string{
		"bin", "sbin", "libexec", "share", "etc", "lib",
	}
	for _, d := range dirs {
		dir := filepath.Join(prefixDir, d)
		if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
			continue
		}
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error { //nolint:gosec // G122 — build output is trusted, not user-controlled
			if err != nil || info.IsDir() {
				return err
			}
			// Skip large files (> 10MB) — unlikely to be
			// text config/scripts.
			if info.Size() > 10*1024*1024 {
				return nil
			}
			data, readErr := os.ReadFile(path) //nolint:gosec // G122 — build/store output is trusted
			if readErr != nil {
				return nil //nolint:nilerr // skip unreadable files
			}
			if !isTextContent(data) {
				return nil
			}
			if !strings.Contains(string(data), prefixDir) {
				return nil
			}
			newData := strings.ReplaceAll(
				string(data), prefixDir, replacement)
			return os.WriteFile(path, []byte(newData), info.Mode()) //nolint:gosec
		})
		if err != nil {
			return fmt.Errorf("fixup text files in %s: %w", d, err)
		}
	}
	return nil
}

// RestorePrefixPlaceholder replaces PrefixPlaceholder with
// storeDir in all text files under storeDir. Called after
// extracting a build archive into the package store.
func RestorePrefixPlaceholder(storeDir string) error {
	dirs := []string{
		"bin", "sbin", "libexec", "share", "etc", "lib",
	}
	for _, d := range dirs {
		dir := filepath.Join(storeDir, d)
		if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
			continue
		}
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error { //nolint:gosec // G122 — store content is trusted
			if err != nil || info.IsDir() {
				return err
			}
			if info.Size() > 10*1024*1024 {
				return nil
			}
			data, readErr := os.ReadFile(path) //nolint:gosec // G122 — build/store output is trusted
			if readErr != nil {
				return nil //nolint:nilerr // skip unreadable files
			}
			if !strings.Contains(string(data), PrefixPlaceholder) {
				return nil
			}
			newData := strings.ReplaceAll(
				string(data), PrefixPlaceholder, storeDir)
			return os.WriteFile(path, []byte(newData), info.Mode()) //nolint:gosec
		})
		if err != nil {
			return fmt.Errorf("restore prefix in %s: %w", d, err)
		}
	}
	return nil
}

// isTextContent returns true if data appears to be text
// (no null bytes in the first 512 bytes).
func isTextContent(data []byte) bool {
	n := len(data)
	if n > 512 {
		n = 512
	}
	for i := 0; i < n; i++ {
		if data[i] == 0 {
			return false
		}
	}
	return n > 0
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

// copyFile copies src to dst, preserving file permissions.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	srcInfo, err := in.Stat()
	if err != nil {
		return err
	}

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

	if err := out.Chmod(srcInfo.Mode()); err != nil {
		out.Close()
		return err
	}

	return out.Close()
}
