//go:build linux

package build

import (
	"debug/elf"
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

// elfHasDynamicDeps reports whether path is an ELF that links
// against shared libraries at runtime (has DT_NEEDED entries).
// Statically linked binaries — including Go builds with no cgo
// deps — return false; patchelf must not rewrite them because it
// can corrupt the layout even when it exits 0 (#134). When the
// file cannot be parsed as ELF, return true so patchelf decides
// (preserves prior behavior for truncated or special files).
func elfHasDynamicDeps(path string) bool {
	f, err := elf.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()

	needed, err := f.DynString(elf.DT_NEEDED)
	return err == nil && len(needed) > 0
}

// walkPrefixELF calls fn for every regular ELF file under
// prefixDir. The whole prefix tree is walked — not just bin/
// and lib/ — so ELF helpers under libexec/ and sbin/ get the
// same rpath fixups (git installs most of its executables in
// libexec/git-core/). Mirrors the darwin full-prefix walk in
// fixup_darwin.go.
func walkPrefixELF(prefixDir string, fn func(path string)) error {
	return filepath.Walk(prefixDir,
		func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil //nolint:nilerr // skip unreadable files
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return nil // patch targets once, not via links
			}
			if isELF(path) {
				fn(path)
			}
			return nil
		})
}

// FixupBinaries rewrites ELF rpath entries so binaries
// find shared libraries relative to themselves via an
// $ORIGIN-anchored path to the package's lib/ directory.
func FixupBinaries(prefixDir string) error {
	patchelf, err := exec.LookPath("patchelf")
	if err != nil {
		// patchelf not available — skip fixup silently.
		// Static binaries don't need it, and patchelf
		// may not be installed yet during bootstrap.
		return nil //nolint:nilerr // intentional: missing patchelf is not an error
	}

	// Scan the whole prefix for ELF files.
	var files []string
	if err := walkPrefixELF(prefixDir, func(path string) {
		files = append(files, path)
	}); err != nil {
		return fmt.Errorf("scan prefix: %w", err)
	}

	if len(files) == 0 {
		return nil
	}

	for _, file := range files {
		if !elfHasDynamicDeps(file) {
			continue
		}
		// Set rpath to find libs relative to the binary.
		// $ORIGIN is the directory containing the binary;
		// the depth-aware path reaches <prefix>/lib from
		// wherever the file sits (bin/, libexec/git-core/).
		cmd := exec.Command(patchelf, "--set-rpath",
			relativeOwnLibRpathLinux(prefixDir, file), file)
		if out, err := cmd.CombinedOutput(); err != nil {
			// patchelf fails on static binaries and some
			// special ELF files — skip silently.
			_ = out
			continue
		}
	}

	return nil
}

// prefixDepth returns the number of directory components
// between prefixDir and file (bin/exe -> 1;
// libexec/git-core/git -> 2).
func prefixDepth(prefixDir, file string) int {
	rel, err := filepath.Rel(prefixDir, file)
	if err != nil {
		rel = filepath.Base(file)
	}
	if dir := filepath.Dir(rel); dir != "." {
		return len(strings.Split(dir, string(filepath.Separator)))
	}
	return 0
}

// relativeOwnLibRpathLinux returns the $ORIGIN-anchored rpath
// from an ELF at `file` to its own package's lib/ directory:
// bin/exe -> $ORIGIN/../lib; libexec/git-core/git ->
// $ORIGIN/../../lib.
func relativeOwnLibRpathLinux(prefixDir, file string) string {
	n := prefixDepth(prefixDir, file)
	return "$ORIGIN/" + strings.Repeat("../", n) + "lib"
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
	// 3 levels (pkg/<name>/<ver-rev>) + the file's own depth
	// up to <galeDir>, then +lib.
	n := prefixDepth(prefixDir, file)
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

	// Walk the whole prefix for ELF files. Both rpath
	// components are computed per file so deeper layouts
	// (libexec/git-core/, sbin/) get the right $ORIGIN depth.
	_ = walkPrefixELF(prefixDir, func(path string) {
		if !elfHasDynamicDeps(path) {
			return
		}
		// The own-lib rpath reaches the package's own libs;
		// the relative farm rpath reaches dep dylibs via
		// <galeDir>/lib. Both relative — nothing absolute.
		rpath := relativeOwnLibRpathLinux(prefixDir, path) + ":" +
			relativeFarmRpathLinux(prefixDir, path)
		cmd := exec.Command(patchelf,
			"--set-rpath", rpath, path)
		_ = cmd.Run() // skip errors on special ELF files
	})

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

	_ = walkPrefixELF(prefixDir, func(path string) {
		if !elfHasDynamicDeps(path) {
			return
		}
		// Read current rpath.
		cmd := exec.Command(patchelf,
			"--print-rpath", path)
		out, err := cmd.Output()
		if err != nil {
			return
		}
		rpath := strings.TrimSpace(string(out))
		if rpath == "" {
			return
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
			return
		}

		newRpath := strings.Join(parts, ":")
		cmd = exec.Command(patchelf,
			"--set-rpath", newRpath, path)
		_ = cmd.Run()
	})

	return nil
}
