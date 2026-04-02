//go:build linux

package build

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// AddDepRpaths is a no-op on Linux. ELF rpath is set to
// $ORIGIN/../lib by FixupBinaries, and LD_LIBRARY_PATH
// handles dep discovery at build time.
func AddDepRpaths(_ string, _ []string) error {
	return nil
}
