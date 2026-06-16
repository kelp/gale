//go:build darwin

package build

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kelp/gale/internal/farm"
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

	// Trailing separator so libexec/ and lib64/ paths are
	// not misclassified as lib/ (they hold executables, not
	// dylibs, and must not get dylib-only fixups).
	libDir := filepath.Join(prefixDir, "lib") +
		string(filepath.Separator)

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
					"change %s in %s: %w", dep, file, err,
				)
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

		// Re-sign ONLY binaries gale actually modified. An
		// untouched Mach-O (e.g. qemu's self-signed mains,
		// carrying an HVF entitlement) is left byte-identical:
		// re-signing it serves no purpose and would strip the
		// entitlement (issue #27). Apple Silicon SIGKILLs
		// unsigned Mach-Os on exec, so a failed re-sign of a
		// binary we DID modify must fail the build rather than
		// ship a broken tarball.
		if shouldResign(changed, inLib) {
			if err := resign(file); err != nil {
				return fmt.Errorf("codesign %s: %w",
					filepath.Base(file), err)
			}
		}
	}

	return nil
}

// AddDepRpaths scans Mach-O binaries under prefixDir for
// @rpath/ references to libraries in dep store dirs and
// adds LC_RPATH entries so they resolve at runtime.
//
// It also canonicalizes unversioned and intermediate @rpath
// dep references (e.g. @rpath/libgit2.dylib or
// @rpath/libgit2.1.dylib) to the versioned real name the
// shared farm provides (@rpath/libgit2.1.9.dylib). The farm
// holds only the deepest real versioned dylib, so a
// dependent that recorded the unversioned name a build
// system left in LC_LOAD_DYLIB would otherwise fail to
// resolve at runtime (issue #124).
//
// FixupBinaries already adds @executable_path/../lib for
// the package's own libs. This handles EXTERNAL deps whose
// dylibs live in other store directories.
//
// # Invariant
//
// Link-time -Wl,-rpath injection (perDepEnv) provides
// rpaths during the build phase so intermediate binaries
// can find dep dylibs immediately (SIP strips DYLD_*
// from /bin/sh children). This function runs post-build
// as the authority — it catches cases where build systems
// strip or rewrite link-time rpaths, and existingRpaths()
// deduplicates.
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

	for _, file := range files {
		deps, err := otoolDeps(file)
		if err != nil {
			continue
		}

		// A Mach-O needs the shared-lib farm rpath when it
		// references at least one @rpath/ dep. Self-contained
		// binaries (system libs only) get nothing — adding an
		// rpath on stripped Mach-O headers would just produce
		// noise.
		hasRpathDep := false
		for _, dep := range deps {
			if !strings.HasPrefix(dep, "@rpath/") {
				// Warn on non-@rpath dep references that point
				// outside system locations. This usually means
				// a dep dylib was built without FixupBinaries,
				// which violates the invariant above.
				if isSuspiciousDepRef(dep, depStoreDirs) {
					fmt.Fprintf(os.Stderr,
						"warning: %s references %s with a"+
							" non-@rpath install name; rpath"+
							" cannot be added automatically\n",
						filepath.Base(file), dep)
				}
				continue
			}
			hasRpathDep = true
		}
		if !hasRpathDep {
			continue
		}

		// Plan the unversioned/intermediate -> versioned ref
		// rewrites the farm needs (issue #124). canonicalDepName
		// only stats/reads symlinks, so this is cheap and lets us
		// skip extractEntitlements for files we won't touch.
		type changeOp struct{ from, to string }
		var changes []changeOp
		for _, dep := range deps {
			if !strings.HasPrefix(dep, "@rpath/") {
				continue
			}
			refBase := strings.TrimPrefix(dep, "@rpath/")
			if real, ok := canonicalDepName(refBase, depStoreDirs); ok {
				changes = append(changes,
					changeOp{from: dep, to: "@rpath/" + real})
			}
		}

		// Add the farm rpath RELATIVE, so the shipped binary
		// resolves its deps via ~/.gale/lib at any gale-home
		// location and needs no install-time rewrite. This is
		// what lets `gale install` place the attested artifact
		// byte-for-byte instead of mutating + re-signing it.
		// See docs/dev/relocatable-binaries.md. dyld resolves
		// the @loader_path form to the farm dir even when a
		// dylib is reached through the farm's symlinks.
		rpath := relativeFarmRpath(prefixDir, file)
		needRpath := !existingRpaths(file)[rpath]
		if len(changes) == 0 && !needRpath {
			continue
		}

		// Capture entitlements BEFORE any mutation. When the
		// Mach-O header lacks padding, addRpathRetry strips the
		// signature (and with it the entitlements) to free space.
		// Extracting here, while the original signature is intact,
		// preserves an entitlement (e.g. qemu's HVF entitlement)
		// even when the strip-and-retry branch fires (issue #27,
		// point 2). resignWithEntitlements re-applies it.
		ent := extractEntitlements(file)
		mutated := false
		for _, c := range changes {
			if err := run("install_name_tool",
				"-change", c.from, c.to, file); err != nil {
				// A failed rewrite (e.g. no header space) is
				// non-fatal: leave the ref as-is rather than
				// abort the build, matching addRpathRetry.
				fmt.Fprintf(os.Stderr,
					"warning: cannot canonicalize %s -> %s"+
						" in %s: %v\n",
					c.from, c.to, filepath.Base(file), err)
				continue
			}
			mutated = true
		}

		if needRpath {
			if err := addRpathRetry(file, rpath); err != nil {
				// addRpathRetry warned and restored any signature
				// it stripped, but a -change above may have left
				// the file unsigned; re-sign before skipping.
				if mutated {
					if err := resignWithEntitlements(file, ent); err != nil {
						return fmt.Errorf("codesign %s: %w",
							filepath.Base(file), err)
					}
				}
				continue
			}
			mutated = true
		}

		if mutated {
			if err := resignWithEntitlements(file, ent); err != nil {
				return fmt.Errorf("codesign %s: %w",
					filepath.Base(file), err)
			}
		}
	}

	return nil
}

// canonicalDepName maps an @rpath dep basename (refBase, e.g.
// "libgit2.dylib" or the intermediate "libgit2.1.dylib") to the
// versioned real dylib basename the shared farm provides, by
// following the symlink chain in the dep store lib dirs. The farm
// holds only the deepest real versioned file (farm.Populate skips
// symlinks), so resolving refBase through the dep store yields
// exactly the farmed name.
//
// Returns ("", false) when no dep provides refBase, when refBase
// already names the real file, or when that real file is not a
// versioned name the farm would hold — leaving already-correct and
// out-of-scope refs (including the package's own libs, which live
// under prefixDir, not depStoreDirs) untouched.
func canonicalDepName(refBase string, depStoreDirs []string) (string, bool) {
	for _, sd := range depStoreDirs {
		cand := filepath.Join(sd, "lib", refBase)
		if _, err := os.Lstat(cand); err != nil {
			continue
		}
		real, err := filepath.EvalSymlinks(cand)
		if err != nil {
			continue
		}
		realBase := filepath.Base(real)
		if realBase == refBase {
			return "", false
		}
		if !farm.IsVersionedDylib(realBase) {
			return "", false
		}
		return realBase, true
	}
	return "", false
}

// relativeFarmRpath returns the @loader_path/@executable_path-
// anchored rpath that points from a Mach-O at `file` (under the
// build prefix `prefixDir`) to the shared-lib farm at
// <galeDir>/lib. A package prefix maps to
// <galeDir>/pkg/<name>/<version-revision>, a fixed three levels
// below <galeDir>, so the farm is reachable with no absolute
// path baked in — the binary relocates to any gale home.
//
// Executables are loaded by their real store path and anchor on
// @executable_path. Dylibs anchor on @loader_path; this covers
// loads via the dylib's own store path, complementing the bare
// @loader_path FixupBinaries adds for loads via the farm
// symlink.
func relativeFarmRpath(prefixDir, file string) string {
	anchor := "@executable_path"
	if strings.HasPrefix(file,
		filepath.Join(prefixDir, "lib")+string(filepath.Separator)) {
		anchor = "@loader_path"
	}
	rel, err := filepath.Rel(prefixDir, file)
	if err != nil {
		rel = filepath.Base(file)
	}
	// Directory components between the prefix root and the file
	// (bin/exe → 1; libexec/git-core/git → 2).
	n := 0
	if dir := filepath.Dir(rel); dir != "." {
		n = len(strings.Split(dir, string(filepath.Separator)))
	}
	// 3 levels (pkg/<name>/<ver-rev>) + n up to <galeDir>, +lib.
	return anchor + "/" + strings.Repeat("../", 3+n) + "lib"
}

// addRpathRetry adds an LC_RPATH to a Mach-O, stripping the
// ad-hoc signature and retrying once if the header lacks space.
// Returns an error (after warning) only if the rpath still won't
// fit, so callers can skip the file instead of aborting. The
// caller re-signs after a successful add; on failure this
// function re-signs the file itself, restoring the signature it
// stripped — a missing farm rpath degrades the binary, but an
// unsigned Mach-O is SIGKILLed on exec on Apple Silicon.
func addRpathRetry(file, rpath string) error {
	if err := run("install_name_tool",
		"-add_rpath", rpath, file); err == nil {
		return nil
	}
	// Header too small — strip signature to free space, retry.
	_ = run("codesign", "--remove-signature", file)
	if err := run("install_name_tool",
		"-add_rpath", rpath, file); err != nil {
		fmt.Fprintf(os.Stderr,
			"warning: cannot add rpath %s to %s: not enough "+
				"header space (link with "+
				"-Wl,-headerpad_max_install_names)\n",
			rpath, filepath.Base(file))
		// Restore the signature stripped above so the file is
		// never left unsigned in the build prefix — the
		// source-build install path never re-signs.
		clearSigningDetritus(file)
		if serr := run("codesign", "--force", "--sign",
			"-", file); serr != nil {
			return fmt.Errorf("re-sign %s after failed rpath"+
				" add: %w", filepath.Base(file), serr)
		}
		return err
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

// RelocateStaleRpaths walks Mach-O files under prefixDir
// and rewrites any LC_RPATH entries that look like they
// reference a foreign gale store root (e.g. CI-baked paths
// like /Users/runner/.gale/pkg/...) to point at the current
// store root.
//
// This is needed because prebuilt binaries published to
// GHCR may have been built on CI with the old gale that
// injected -Wl,-rpath,<libdir> at link time, baking the
// CI user's HOME path into the Mach-O. Without this fixup,
// the binary would fail to load its dep dylibs at runtime.
//
// An rpath is considered stale if it contains a
// ".gale/pkg/" path component whose prefix is not the
// current store root. The prefix is rewritten while the
// suffix (<pkg>/<version>/lib) is preserved.
func RelocateStaleRpaths(prefixDir, currentStoreRoot string) error {
	var files []string
	err := filepath.Walk(prefixDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !info.Mode().IsRegular() {
			return nil
		}
		if isMachO(path) && !isObjectFile(path) && !isDSYMBundle(path) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk prefix: %w", err)
	}

	sep := string(filepath.Separator)
	pkgMarker := ".gale" + sep + "pkg" + sep
	libMarker := ".gale" + sep + "lib"
	currentStoreRoot = filepath.Clean(currentStoreRoot)
	// galeDir = parent of <galeDir>/pkg/ — used to compute
	// the local farm dir for .gale/lib rpath relocation.
	galeDir := filepath.Dir(currentStoreRoot)
	currentFarmDir := filepath.Join(galeDir, "lib")

	for _, file := range files {
		rpaths := existingRpaths(file)
		rewrote := false
		for rpath := range rpaths {
			var newPath string
			switch {
			case strings.Contains(rpath, pkgMarker):
				idx := strings.Index(rpath, pkgMarker)
				suffix := rpath[idx+len(pkgMarker):]
				newPath = filepath.Join(currentStoreRoot, suffix)
			case strings.HasSuffix(rpath, libMarker) ||
				strings.Contains(rpath, libMarker+sep):
				// Farm rpath from CI: /Users/runner/.gale/lib
				// → <local galeDir>/lib. Suffix (if any) past
				// the marker is preserved.
				idx := strings.Index(rpath, libMarker)
				suffix := rpath[idx+len(libMarker):]
				newPath = currentFarmDir + suffix
			default:
				continue
			}
			// Check if this rpath already has the current
			// store root as its prefix; if so, leave alone.
			if rpath == newPath {
				continue
			}
			if err := run("install_name_tool", "-rpath",
				rpath, newPath, file); err != nil {
				return fmt.Errorf("rewrite rpath %s in %s: %w",
					rpath, file, err)
			}
			rewrote = true
		}
		// Re-sign ONLY when an rpath was actually rewritten. A
		// file with no stale rpath is left byte-identical so the
		// installed artifact still matches what was attested.
		if rewrote {
			if err := resign(file); err != nil {
				return fmt.Errorf("codesign %s: %w",
					filepath.Base(file), err)
			}
		}
	}
	return nil
}

// EnsureCodeSigned walks Mach-O files under prefixDir and
// applies an ad-hoc code signature to any that lack one. On
// Apple Silicon the kernel SIGKILLs unsigned Mach-O binaries
// on exec, so this guards against CI tarballs that shipped
// without signatures (or had them stripped in transit).
//
// Object files (.o) and files inside .dSYM debug bundles are
// skipped — they are never executed and install_name_tool
// cannot modify them either.
func EnsureCodeSigned(prefixDir string) error {
	return filepath.Walk(prefixDir,
		func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil //nolint:nilerr // skip unreadable files
			}
			if !isMachO(path) || isObjectFile(path) || isDSYMBundle(path) {
				return nil
			}
			// codesign -v exits 0 on any valid signature
			// (including ad-hoc) and non-zero otherwise.
			if exec.Command("codesign", "-v", path).Run() == nil {
				return nil
			}
			clearSigningDetritus(path)
			if err := run("codesign", "--force", "--sign",
				"-", path); err != nil {
				return fmt.Errorf("codesign %s: %w",
					filepath.Base(path), err)
			}
			return nil
		})
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

// resign applies a fresh ad-hoc signature to a Mach-O after gale
// has modified it (e.g. added a farm rpath). It strips any
// existing signature first (mirroring addRpathRetry) so codesign
// --sign starts from a clean header: re-signing a binary that
// already carries an ad-hoc signature + entitlements can trip a
// codesign edge case whose error a plain --force --sign would
// surface as a build failure (issue #27). --remove-signature is a
// no-op on an unsigned file, so this is safe to call
// unconditionally.
//
// Entitlements are PRESERVED across the re-sign. gale must add a
// farm rpath to binaries like qemu-system-aarch64, which has
// runtime deps (glib, pixman) referenced via @rpath/ — so the
// binary genuinely changes and cannot be left byte-identical. But
// qemu self-signs its mains with the Hypervisor.framework
// entitlement it needs for HVF acceleration. A plain ad-hoc
// re-sign would drop that entitlement (issue #27, point 2), so we
// extract the existing entitlements first and re-apply them.
func resign(file string) error {
	return resignWithEntitlements(file, extractEntitlements(file))
}

// clearSigningDetritus strips the extended attributes codesign
// rejects on a flat Mach-O. qemu's scripts/entitlement.sh attaches an
// icon resource fork (`Rez -append`, com.apple.ResourceFork) and sets
// the custom-icon Finder flag (`SetFile -a C`, com.apple.FinderInfo)
// on its emulator mains; codesign then fails with "resource fork,
// Finder information, or similar detritus not allowed", and
// `codesign --remove-signature` does NOT clear xattrs. So gale clears
// them before re-signing any binary it modified (issue #27). `xattr
// -c` drops all xattrs — the icon is cosmetic (Homebrew does the
// equivalent). Best-effort: a stubborn xattr resurfaces as a loud
// codesign failure at the actual sign call, never a silent drop.
func clearSigningDetritus(file string) {
	_ = run("xattr", "-c", file)
}

// resignWithEntitlements re-signs a Mach-O, re-applying the given
// entitlements XML (pass "" for none). Callers that modify a binary
// in a way that may strip its signature first (e.g. addRpathRetry's
// header-too-small branch runs `codesign --remove-signature`) MUST
// capture entitlements via extractEntitlements BEFORE that
// modification and pass them here — by the time resign runs the
// original entitlements are already gone (issue #27, point 2).
func resignWithEntitlements(file, ent string) error {
	_ = run("codesign", "--remove-signature", file)
	clearSigningDetritus(file)
	args := []string{"--force", "--sign", "-"}
	if ent != "" {
		// Write the recovered entitlements to a temp plist and pass
		// it to codesign so the new signature carries them too.
		// Staging the plist MUST succeed: silently signing without
		// --entitlements would drop the entitlement (e.g. qemu's HVF)
		// while the build still "succeeds" — the exact failure mode
		// issue #27 guards against. So a staging failure is fatal.
		f, err := os.CreateTemp("", "gale-entitlements-*.plist")
		if err != nil {
			return fmt.Errorf("stage entitlements for %s: %w",
				filepath.Base(file), err)
		}
		path := f.Name()
		defer os.Remove(path)
		_, werr := f.WriteString(ent)
		cerr := f.Close()
		if werr != nil {
			return fmt.Errorf("write entitlements for %s: %w",
				filepath.Base(file), werr)
		}
		if cerr != nil {
			return fmt.Errorf("write entitlements for %s: %w",
				filepath.Base(file), cerr)
		}
		args = append(args, "--entitlements", path)
	}
	args = append(args, file)
	return run("codesign", args...)
}

// extractEntitlements returns the embedded entitlements plist (XML)
// of a Mach-O, or "" if it carries none (or codesign fails). The
// returned XML is suitable for re-applying via codesign
// --entitlements.
//
// `codesign --display --entitlements - --xml` writes the plist as
// clean XML to stdout (macOS 12+). On older toolchains --xml is
// absent and the raw blob is emitted with an 8-byte binary magic
// header (0xfade7171 + length) before the <?xml prologue; we strip
// any bytes preceding the XML prologue so either format works. An
// unsigned or entitlement-free binary yields no <key> elements,
// which we treat as "none" so we don't pass an empty entitlements
// file (which codesign rejects).
func extractEntitlements(file string) string {
	cmd := exec.Command("codesign", "--display", "--entitlements",
		"-", "--xml", file)
	out, err := cmd.Output()
	if err != nil || !strings.Contains(string(out), "<?xml") {
		// Retry without --xml for older codesign; the raw blob form
		// carries the same XML after a binary magic header.
		cmd = exec.Command("codesign", "--display",
			"--entitlements", "-", file)
		out, err = cmd.Output()
		if err != nil {
			return ""
		}
	}
	s := string(out)
	// Strip any binary magic header preceding the XML prologue.
	if i := strings.Index(s, "<?xml"); i > 0 {
		s = s[i:]
	}
	s = strings.TrimSpace(s)
	// An entitlement-bearing binary emits a plist with at least one
	// <key>. A signed-but-entitlement-free binary emits nothing or
	// an empty document; re-applying that would be a no-op at best
	// and a codesign error at worst, so skip it.
	if !strings.Contains(s, "<key>") {
		return ""
	}
	return s
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
