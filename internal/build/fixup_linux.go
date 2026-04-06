//go:build linux

package build

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ELF magic bytes.
var elfMagic = []byte{0x7f, 'E', 'L', 'F'}

// isELF returns true if the file at path is an ELF binary.
func isELF(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	magic := make([]byte, 4)
	if _, err := f.Read(magic); err != nil {
		return false
	}

	return magic[0] == elfMagic[0] &&
		magic[1] == elfMagic[1] &&
		magic[2] == elfMagic[2] &&
		magic[3] == elfMagic[3]
}

// FixupBinaries rewrites ELF rpath entries so binaries
// find shared libraries relative to themselves using
// $ORIGIN/../lib.
func FixupBinaries(prefixDir string) error {
	patchelf, err := exec.LookPath("patchelf")
	if err != nil {
		// patchelf not available — skip fixup silently.
		// Static binaries don't need it, and patchelf
		// may not be installed yet during bootstrap.
		return nil //nolint:nilerr // intentional: missing patchelf is not an error
	}

	// Scan bin/ and lib/ for ELF files.
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
				if isELF(path) {
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

	for _, file := range files {
		// Set rpath to find libs relative to the binary.
		// $ORIGIN is the directory containing the binary.
		cmd := exec.Command(patchelf, "--set-rpath",
			"$ORIGIN/../lib", file)
		if out, err := cmd.CombinedOutput(); err != nil {
			// patchelf fails on static binaries and some
			// special ELF files — skip silently.
			_ = out
			continue
		}
	}

	return nil
}

// AddDepRpaths adds dep library directories to the ELF
// RUNPATH of all binaries under prefixDir. This ensures
// that shared libraries from gale deps are found at
// runtime without LD_LIBRARY_PATH.
func AddDepRpaths(prefixDir string, depStoreDirs []string) error {
	if len(depStoreDirs) == 0 {
		return nil
	}

	patchelf, err := exec.LookPath("patchelf")
	if err != nil {
		return nil //nolint:nilerr // patchelf not available
	}

	// Collect dep lib dirs that exist.
	var depLibDirs []string
	for _, storeDir := range depStoreDirs {
		libDir := filepath.Join(storeDir, "lib")
		if _, err := os.Stat(libDir); err == nil {
			depLibDirs = append(depLibDirs, libDir)
		}
	}
	if len(depLibDirs) == 0 {
		return nil
	}

	// Build the rpath string: $ORIGIN/../lib + all dep lib dirs.
	rpathParts := []string{"$ORIGIN/../lib"}
	rpathParts = append(rpathParts, depLibDirs...)
	rpath := strings.Join(rpathParts, ":")

	// Walk bin/ and lib/ for ELF files.
	for _, subdir := range []string{"bin", "lib"} {
		dir := filepath.Join(prefixDir, subdir)
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		_ = filepath.Walk(dir,
			func(path string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return nil //nolint:nilerr
				}
				if !isELF(path) {
					return nil
				}
				cmd := exec.Command(patchelf,
					"--set-rpath", rpath, path)
				_ = cmd.Run() // skip errors (static binaries)
				return nil
			})
	}

	return nil
}

// RelocateStaleRpaths rewrites ELF RUNPATH entries that
// contain a foreign .gale/pkg/ store prefix to use the
// local store root. This handles prebuilt binaries from
// CI where rpaths are baked to the CI runner's home dir.
func RelocateStaleRpaths(prefixDir, storeRoot string) error {
	patchelf, err := exec.LookPath("patchelf")
	if err != nil {
		return nil //nolint:nilerr
	}

	marker := ".gale/pkg/"

	for _, subdir := range []string{"bin", "lib"} {
		dir := filepath.Join(prefixDir, subdir)
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		_ = filepath.Walk(dir,
			func(path string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return nil //nolint:nilerr
				}
				if !isELF(path) {
					return nil
				}

				// Read current rpath.
				cmd := exec.Command(patchelf,
					"--print-rpath", path)
				out, err := cmd.Output()
				if err != nil {
					return nil //nolint:nilerr
				}
				rpath := strings.TrimSpace(string(out))
				if rpath == "" {
					return nil
				}

				// Check if any entry has a foreign store.
				parts := strings.Split(rpath, ":")
				changed := false
				for i, p := range parts {
					idx := strings.Index(p, marker)
					if idx < 0 {
						continue
					}
					// Rewrite prefix to local store.
					suffix := p[idx+len(marker):]
					parts[i] = filepath.Join(
						storeRoot, suffix)
					changed = true
				}
				if !changed {
					return nil
				}

				newRpath := strings.Join(parts, ":")
				cmd = exec.Command(patchelf,
					"--set-rpath", newRpath, path)
				_ = cmd.Run()
				return nil
			})
	}

	return nil
}
