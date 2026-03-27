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

// FixupBinaries rewrites dynamic library paths in all
// binaries and shared libraries under prefixDir so they
// use @rpath instead of absolute build-time paths.
func FixupBinaries(prefixDir string) error {
	// Scan bin/ and lib/ for Mach-O files.
	var files []string
	for _, subdir := range []string{"bin", "lib"} {
		dir := filepath.Join(prefixDir, subdir)
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		err := filepath.Walk(dir,
			func(path string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return nil //nolint:nilerr // skip unreadable files
				}
				if isMachO(path) {
					files = append(files, path)
				}
				return nil
			})
		if err != nil {
			return fmt.Errorf("scan %s: %w", subdir, err)
		}
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
		if err == nil {
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

// run executes a command and returns any error.
func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %s", name, args, out)
	}
	return nil
}
