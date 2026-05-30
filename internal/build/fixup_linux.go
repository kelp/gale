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

// relativeFarmRpathLinux returns the $ORIGIN-anchored rpath
// that points from an ELF at `file` (under the build prefix
// `prefixDir`) to the shared-lib farm at <galeDir>/lib. A
// package prefix maps to <galeDir>/pkg/<name>/<version-revision>,
// a fixed three levels below <galeDir>, so the farm is reachable
// with no absolute path baked in — the binary relocates to any
// gale home. This is the Linux mirror of relativeFarmRpath in
// fixup_darwin.go. See docs/dev/relocatable-binaries.md.
func relativeFarmRpathLinux(prefixDir, file string) string {
	rel, err := filepath.Rel(prefixDir, file)
	if err != nil {
		rel = filepath.Base(file)
	}
	// Directory components between the prefix root and the file
	// (bin/exe -> 1; libexec/git-core/git -> 2).
	n := 0
	if dir := filepath.Dir(rel); dir != "." {
		n = len(strings.Split(dir, string(filepath.Separator)))
	}
	// 3 levels (pkg/<name>/<ver-rev>) + n up to <galeDir>, +lib.
	return "$ORIGIN/" + strings.Repeat("../", 3+n) + "lib"
}

// AddDepRpaths sets the ELF RUNPATH of binaries under prefixDir
// so shared libraries from gale deps resolve at runtime without
// LD_LIBRARY_PATH. The dep dylibs are reached through the shared
// lib farm via a RELATIVE ($ORIGIN-anchored) rpath, so the
// shipped artifact needs no install-time rewrite and installs
// byte-for-byte with what CI built and attested. No absolute
// .gale/pkg or .gale/lib path is baked in. This mirrors the
// darwin AddDepRpaths design — see
// docs/dev/relocatable-binaries.md.
func AddDepRpaths(prefixDir string, depStoreDirs []string) error {
	if len(depStoreDirs) == 0 {
		return nil
	}

	patchelf, err := exec.LookPath("patchelf")
	if err != nil {
		return nil //nolint:nilerr // patchelf not available
	}

	// Only bother if at least one dep actually ships a lib dir;
	// otherwise there is nothing for the farm rpath to find.
	hasDepLib := false
	for _, storeDir := range depStoreDirs {
		if _, err := os.Stat(filepath.Join(storeDir, "lib")); err == nil {
			hasDepLib = true
			break
		}
	}
	if !hasDepLib {
		return nil
	}

	// Walk bin/ and lib/ for ELF files. The farm rpath is
	// computed per file so deeper layouts (libexec/.../bin) get
	// the right $ORIGIN depth.
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
				// $ORIGIN/../lib reaches the package's own libs;
				// the relative farm rpath reaches dep dylibs via
				// <galeDir>/lib. Both relative — nothing absolute.
				rpath := "$ORIGIN/../lib:" +
					relativeFarmRpathLinux(prefixDir, path)
				cmd := exec.Command(patchelf,
					"--set-rpath", rpath, path)
				_ = cmd.Run() // skip errors (static binaries)
				return nil
			})
	}

	return nil
}

// EnsureCodeSigned is a no-op on Linux. Exists so platform-
// neutral callers can invoke it without build-tag shims.
func EnsureCodeSigned(prefixDir string) error { return nil }

// RelocateStaleRpaths rewrites ELF RUNPATH entries that
// contain a foreign .gale/pkg/ store prefix to use the
// local store root. This handles prebuilt binaries from
// CI where rpaths are baked to the CI runner's home dir.
func RelocateStaleRpaths(prefixDir, storeRoot string) error {
	patchelf, err := exec.LookPath("patchelf")
	if err != nil {
		return nil //nolint:nilerr
	}

	const pkgMarker = ".gale/pkg/"
	const libMarker = ".gale/lib"
	// galeDir = parent of storeRoot — used to compute
	// the local farm dir for .gale/lib rpath relocation.
	galeDir := filepath.Dir(storeRoot)
	currentFarmDir := filepath.Join(galeDir, "lib")

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

				parts := strings.Split(rpath, ":")
				changed := false
				for i, p := range parts {
					if idx := strings.Index(p, pkgMarker); idx >= 0 {
						suffix := p[idx+len(pkgMarker):]
						parts[i] = filepath.Join(storeRoot, suffix)
						changed = true
						continue
					}
					if strings.HasSuffix(p, libMarker) ||
						strings.Contains(p, libMarker+"/") {
						idx := strings.Index(p, libMarker)
						suffix := p[idx+len(libMarker):]
						parts[i] = currentFarmDir + suffix
						changed = true
					}
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
