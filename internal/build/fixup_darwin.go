//go:build darwin

package build

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Mach-O magic numbers.
var machoMagics = [][]byte{
	{0xfe, 0xed, 0xfa, 0xce}, // MH_MAGIC (32-bit)
	{0xfe, 0xed, 0xfa, 0xcf}, // MH_MAGIC_64 (64-bit)
	{0xce, 0xfa, 0xed, 0xfe}, // MH_CIGAM (32-bit, swapped)
	{0xcf, 0xfa, 0xed, 0xfe}, // MH_CIGAM_64 (64-bit, swapped)
	{0xca, 0xfe, 0xba, 0xbe}, // FAT_MAGIC (universal)
}

// isMachO returns true if the file at path is a Mach-O
// binary (executable, dylib, or universal binary).
func isMachO(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	magic := make([]byte, 4)
	if _, err := f.Read(magic); err != nil {
		return false
	}

	for _, m := range machoMagics {
		if magic[0] == m[0] && magic[1] == m[1] &&
			magic[2] == m[2] && magic[3] == m[3] {
			return true
		}
	}
	return false
}

// isObjectFile returns true if the path ends with .o —
// a relocatable object file that cannot have its install
// names changed by install_name_tool.
func isObjectFile(path string) bool {
	return strings.HasSuffix(path, ".o")
}

// isDSYMBundle returns true if the path is inside a
// .dSYM debug symbol bundle. These contain Mach-O
// DWARF data that install_name_tool cannot modify.
func isDSYMBundle(path string) bool {
	return strings.Contains(path, ".dSYM/")
}

// FixupBinaries rewrites dynamic library paths in all
// binaries and shared libraries under prefixDir so they
// use @rpath instead of absolute build-time paths.
func FixupBinaries(prefixDir string) error {
	// Walk the entire prefix tree for Mach-O files.
	// Some packages install binaries outside bin/ and
	// lib/ (e.g., git uses libexec/git-core/).
	var files []string
	err := filepath.Walk(prefixDir,
		func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil //nolint:nilerr // skip unreadable files
			}
			if isMachO(path) && !isObjectFile(path) && !isDSYMBundle(path) {
				files = append(files, path)
			}
			return nil
		})
	if err != nil {
		return fmt.Errorf("scan prefix: %w", err)
	}

	if len(files) == 0 {
		return nil
	}

	libDir := filepath.Join(prefixDir, "lib")

	for _, file := range files {
		inLib := strings.HasPrefix(file, libDir)

		// Fix dylib ID for libraries.
		if inLib {
			name := filepath.Base(file)
			if err := run("install_name_tool", "-id",
				"@rpath/"+name, file); err != nil {
				return fmt.Errorf("set dylib id %s: %w",
					name, err)
			}
		}

		// Rewrite dependency paths that point into the
		// prefix (build-time paths).
		var changed bool
		deps, err := otoolDeps(file)
		if err != nil {
			return fmt.Errorf("otool deps %s: %w",
				filepath.Base(file), err)
		}
		for _, dep := range deps {
			if !strings.HasPrefix(dep, prefixDir) {
				continue
			}
			base := filepath.Base(dep)
			if err := run("install_name_tool", "-change",
				dep, "@rpath/"+base, file); err != nil {
				return fmt.Errorf(
					"change %s in %s: %w", dep, file, err)
			}
			changed = true
		}

		// Add rpath entries so binaries/dylibs can find
		// shared libs at runtime. Only needed when we
		// rewrote deps or set a dylib ID.
		if changed || inLib {
			if inLib {
				_ = run("install_name_tool", "-add_rpath",
					"@loader_path", file)
			} else {
				_ = run("install_name_tool", "-add_rpath",
					"@executable_path/../lib", file)
			}
		}

		// Re-sign (required on macOS after modification).
		_ = run("codesign", "--force", "--sign", "-", file)
	}

	return nil
}

// AddDepRpaths scans Mach-O binaries under prefixDir for
// @rpath/ references to libraries in dep store dirs and
// adds LC_RPATH entries so they resolve at runtime.
//
// FixupBinaries already adds @executable_path/../lib for
// the package's own libs. This handles EXTERNAL deps whose
// dylibs live in other store directories.
//
// # Invariant
//
// This function is the single source of truth for dep
// rpaths on darwin. Link-time -Wl,-rpath injection was
// removed because (a) Ruby's configure rejects it in
// LDFLAGS sanity checks and (b) libtool/cmake strip or
// rewrite -Wl,-rpath unreliably, making it dead code in
// most cases.
//
// For this to work, every dep dylib in a gale store
// directory MUST have an @rpath/-style install name.
// FixupBinaries enforces this for source-built packages;
// gale-recipes CI enforces it for prebuilt GHCR artifacts
// by running the same pipeline. A binary that references
// a dep dylib via an absolute path or a @loader_path/
// reference will not have an rpath added by this function
// and will fail at runtime.
func AddDepRpaths(prefixDir string, depStoreDirs []string) error {
	if len(depStoreDirs) == 0 {
		return nil
	}

	// Walk the entire prefix tree for Mach-O files.
	var files []string
	_ = filepath.Walk(prefixDir,
		func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil //nolint:nilerr
			}
			if isMachO(path) && !isObjectFile(path) && !isDSYMBundle(path) {
				files = append(files, path)
			}
			return nil
		})

	if len(files) == 0 {
		return nil
	}

	// Build a map: library basename → dep lib dir.
	// e.g. "libpcre2-8.dylib" → "~/.gale/pkg/pcre2/10.44/lib"
	libDirMap := make(map[string]string)
	for _, storeDir := range depStoreDirs {
		libDir := filepath.Join(storeDir, "lib")
		entries, err := os.ReadDir(libDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".dylib") {
				libDirMap[e.Name()] = libDir
			}
		}
	}

	ownLib := filepath.Join(prefixDir, "lib")

	for _, file := range files {
		deps, err := otoolDeps(file)
		if err != nil {
			continue
		}

		// Collect unique dep lib dirs needed by this
		// binary.
		needed := make(map[string]bool)
		for _, dep := range deps {
			if !strings.HasPrefix(dep, "@rpath/") {
				// Warn on non-@rpath dep references
				// that point outside system locations.
				// This usually means a dep dylib was
				// built without FixupBinaries, which
				// violates the invariant above.
				if isSuspiciousDepRef(dep, depStoreDirs) {
					fmt.Fprintf(os.Stderr,
						"warning: %s references %s with a"+
							" non-@rpath install name; rpath"+
							" cannot be added automatically\n",
						filepath.Base(file), dep)
				}
				continue
			}
			libName := strings.TrimPrefix(dep, "@rpath/")

			// Skip libs in the package's own lib dir.
			if _, err := os.Stat(
				filepath.Join(ownLib, libName)); err == nil {
				continue
			}

			if dir, ok := libDirMap[libName]; ok {
				needed[dir] = true
			}
		}

		if len(needed) == 0 {
			continue
		}

		// Get existing rpaths to avoid duplicates.
		existing := existingRpaths(file)

		changed := false
		for dir := range needed {
			if existing[dir] {
				continue
			}
			err := run("install_name_tool",
				"-add_rpath", dir, file)
			if err != nil {
				// Header too small — strip signature to
				// free space, then retry.
				_ = run("codesign", "--remove-signature",
					file)
				if retryErr := run("install_name_tool",
					"-add_rpath", dir, file); retryErr != nil {
					fmt.Fprintf(os.Stderr,
						"warning: cannot add rpath %s to %s: "+
							"not enough header space (link with "+
							"-Wl,-headerpad_max_install_names)\n",
						dir, filepath.Base(file))
					continue
				}
			}
			changed = true
		}

		if changed {
			_ = run("codesign", "--force", "--sign",
				"-", file)
		}
	}

	return nil
}

// existingRpaths returns the set of LC_RPATH entries
// already present in a Mach-O binary.
func existingRpaths(path string) map[string]bool {
	cmd := exec.Command("otool", "-l", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil
	}

	rpaths := make(map[string]bool)
	lines := strings.Split(string(out), "\n")
	for i, line := range lines {
		if strings.Contains(line, "cmd LC_RPATH") {
			// The path is two lines after "cmd LC_RPATH":
			//   cmd LC_RPATH
			//   cmdsize ...
			//   path /some/path (offset ...)
			if i+2 < len(lines) {
				pline := strings.TrimSpace(lines[i+2])
				if strings.HasPrefix(pline, "path ") {
					p := strings.TrimPrefix(pline, "path ")
					if idx := strings.Index(p, " ("); idx > 0 {
						rpaths[p[:idx]] = true
					}
				}
			}
		}
	}
	return rpaths
}

// otoolDeps returns the list of dynamic library paths
// referenced by a Mach-O file.
func otoolDeps(path string) ([]string, error) {
	cmd := exec.Command("otool", "-L", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}

	var deps []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasSuffix(line, ":") {
			continue
		}
		// Format: "/path/to/lib.dylib (compatibility ...)"
		if idx := strings.Index(line, " ("); idx > 0 {
			deps = append(deps, line[:idx])
		}
	}
	return deps, nil
}

// isSuspiciousDepRef reports whether a LC_LOAD_DYLIB entry
// looks like it should have been an @rpath/ reference to a
// gale dep dylib. Returns true when the reference is an
// absolute path into one of the dep store dirs — which
// means a dep dylib was shipped without its install name
// being rewritten to @rpath/, violating the invariant
// documented on AddDepRpaths.
//
// Returns false for @loader_path/, @executable_path/,
// system paths (/usr/lib, /System/), and absolute paths
// outside the store (user-installed libraries that gale
// doesn't manage).
func isSuspiciousDepRef(ref string, depStoreDirs []string) bool {
	if strings.HasPrefix(ref, "@") {
		return false
	}
	if strings.HasPrefix(ref, "/usr/lib/") ||
		strings.HasPrefix(ref, "/System/") {
		return false
	}
	for _, storeDir := range depStoreDirs {
		if strings.HasPrefix(ref, storeDir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// run executes a command and returns any error.
func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %s", name, args, out)
	}
	return nil
}
