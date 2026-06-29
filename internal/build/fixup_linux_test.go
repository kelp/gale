//go:build linux

package build

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// readelfRunpath returns the RUNPATH/RPATH of an ELF file via
// readelf, so the assertion does not depend on patchelf being
// available (the very tool issue #24 says installs must not need).
// readelfBin may be an absolute path so the call works even after
// PATH is cleared to simulate a fresh box.
func readelfRunpath(t *testing.T, readelfBin, path string) string {
	t.Helper()
	out, err := exec.Command(readelfBin, "-d", path).Output()
	if err != nil {
		t.Fatalf("readelf -d %s: %v", path, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "RUNPATH") ||
			strings.Contains(line, "RPATH") {
			if i := strings.Index(line, "["); i >= 0 {
				if j := strings.Index(line[i:], "]"); j >= 0 {
					return line[i+1 : i+j]
				}
			}
		}
	}
	return ""
}

// elfRunpath returns the DT_RUNPATH/DT_RPATH of an ELF file
// via patchelf, for assertions.
func elfRunpath(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.Command("patchelf", "--print-rpath", path).Output()
	if err != nil {
		t.Fatalf("patchelf --print-rpath %s: %v", path, err)
	}
	return strings.TrimSpace(string(out))
}

// TestAddDepRpathsLinuxUsesRelativeFarmRpath is the Linux
// mirror of the darwin TestAddDepRpathsAddsRpathForDepLib.
// A shipped binary must resolve its dep dylibs via a RELATIVE
// ($ORIGIN-anchored) farm rpath so the attested artifact is
// installed byte-for-byte with no rpath rewrite. It must NOT
// bake any absolute .gale/pkg or .gale/lib store path.
// See docs/dev/relocatable-binaries.md.
func TestAddDepRpathsLinuxUsesRelativeFarmRpath(t *testing.T) {
	if _, err := exec.LookPath("patchelf"); err != nil {
		t.Skip("patchelf not available")
	}
	if _, err := exec.LookPath("cc"); err != nil {
		t.Skip("cc not available")
	}

	// "dep" store dir with a shared lib.
	depDir := t.TempDir()
	depLib := filepath.Join(depDir, "lib")
	if err := os.MkdirAll(depLib, 0o755); err != nil {
		t.Fatal(err)
	}
	libSrc := filepath.Join(depDir, "dep.c")
	if err := os.WriteFile(libSrc,
		[]byte("int dep_func(void){return 7;}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dylib := filepath.Join(depLib, "libdep.so")
	if out, err := exec.Command(
		"cc", "-shared", "-fPIC",
		"-Wl,-soname,libdep.so", "-o", dylib, libSrc,
	).CombinedOutput(); err != nil {
		t.Skipf("cc -shared failed: %v\n%s", err, out)
	}

	// "package" prefix with a binary linking the dep.
	pkgDir := t.TempDir()
	binDir := filepath.Join(pkgDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mainSrc := filepath.Join(pkgDir, "main.c")
	if err := os.WriteFile(mainSrc,
		[]byte("extern int dep_func(void);\nint main(){return dep_func();}\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(binDir, "app")
	if out, err := exec.Command(
		"cc", "-o", binPath, mainSrc,
		"-L"+depLib, "-ldep",
	).CombinedOutput(); err != nil {
		t.Skipf("cc link failed: %v\n%s", err, out)
	}

	if err := AddDepRpaths(pkgDir, []string{depDir}); err != nil {
		t.Fatalf("AddDepRpaths: %v", err)
	}

	rp := elfRunpath(t, binPath)
	// An executable in bin/ sits four levels above the farm
	// (<galeDir>/pkg/<name>/<ver-rev>/bin -> <galeDir>/lib).
	if !strings.Contains(rp, "$ORIGIN/../../../../lib") {
		t.Errorf("expected relative farm rpath "+
			"$ORIGIN/../../../../lib, got: %q", rp)
	}
	// Must NOT bake the absolute dep store lib dir.
	if strings.Contains(rp, depLib) {
		t.Errorf("baked absolute dep path %q in rpath: %q",
			depLib, rp)
	}
	// Must NOT bake any absolute gale store path.
	if strings.Contains(rp, ".gale/pkg/") ||
		strings.Contains(rp, ".gale/lib") {
		t.Errorf("baked absolute gale store path in rpath: %q", rp)
	}
}

// TestAddDepRpathsOverwritesAbsoluteToolchainRpath guards the
// remaining issue #24 gap: on Linux, llvmToolchainFlags injects
// -Wl,-rpath,<absolute llvm libDir> into LDFLAGS, so a freshly
// linked ELF carries the CI runner's ABSOLUTE store path in its
// RUNPATH. The post-build fixup pass (FixupBinaries then
// AddDepRpaths, in that order — see buildFromDir in build.go)
// must OVERWRITE that RUNPATH with $ORIGIN-relative entries so
// the absolute path never reaches a shipped artifact. If the
// fixup steps were reordered or AddDepRpaths appended instead of
// set, runner-absolute rpaths would silently ship again.
func TestAddDepRpathsOverwritesAbsoluteToolchainRpath(t *testing.T) {
	if _, err := exec.LookPath("patchelf"); err != nil {
		t.Skip("patchelf not available")
	}
	if _, err := exec.LookPath("cc"); err != nil {
		t.Skip("cc not available")
	}

	// "dep" store dir with a shared lib, so AddDepRpaths has a
	// dep lib to point the farm rpath at (it no-ops otherwise).
	depDir := t.TempDir()
	depLib := filepath.Join(depDir, "lib")
	if err := os.MkdirAll(depLib, 0o755); err != nil {
		t.Fatal(err)
	}
	libSrc := filepath.Join(depDir, "dep.c")
	if err := os.WriteFile(libSrc,
		[]byte("int dep_func(void){return 7;}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dylib := filepath.Join(depLib, "libdep.so")
	if out, err := exec.Command(
		"cc", "-shared", "-fPIC",
		"-Wl,-soname,libdep.so", "-o", dylib, libSrc,
	).CombinedOutput(); err != nil {
		t.Skipf("cc -shared failed: %v\n%s", err, out)
	}

	// "package" prefix with a binary linking the dep, built with
	// the absolute toolchain rpath llvmToolchainFlags would emit.
	const toolchainRpath = "/home/runner/.gale/pkg/llvm/18.1.6-1/lib"
	pkgDir := t.TempDir()
	binDir := filepath.Join(pkgDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mainSrc := filepath.Join(pkgDir, "main.c")
	if err := os.WriteFile(mainSrc,
		[]byte("extern int dep_func(void);\nint main(){return dep_func();}\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(binDir, "app")
	if out, err := exec.Command(
		"cc", "-o", binPath, mainSrc,
		"-L"+depLib, "-ldep",
		"-Wl,-rpath,"+toolchainRpath,
		"-Wl,--enable-new-dtags",
	).CombinedOutput(); err != nil {
		t.Skipf("cc link failed: %v\n%s", err, out)
	}

	// Sanity: the absolute toolchain rpath IS baked pre-fixup.
	// Without this the test could silently assert nothing.
	rpBefore := elfRunpath(t, binPath)
	if !strings.Contains(rpBefore, toolchainRpath) {
		t.Fatalf("setup: expected absolute toolchain rpath %q "+
			"baked pre-fixup, got %q", toolchainRpath, rpBefore)
	}

	// The exact post-build sequence buildFromDir runs.
	if err := FixupBinaries(pkgDir); err != nil {
		t.Fatalf("FixupBinaries: %v", err)
	}
	if err := AddDepRpaths(pkgDir, []string{depDir}); err != nil {
		t.Fatalf("AddDepRpaths: %v", err)
	}

	rp := elfRunpath(t, binPath)
	// $ORIGIN-relative own-lib and farm entries must be present.
	if !strings.Contains(rp, "$ORIGIN/../lib") {
		t.Errorf("expected relative own-lib rpath "+
			"$ORIGIN/../lib, got: %q", rp)
	}
	if !strings.Contains(rp, "$ORIGIN/../../../../lib") {
		t.Errorf("expected relative farm rpath "+
			"$ORIGIN/../../../../lib, got: %q", rp)
	}
	// The CI runner's absolute path must be gone.
	if strings.Contains(rp, "/home/runner") {
		t.Errorf("absolute CI runner rpath survived fixup: %q", rp)
	}
	// Must NOT bake any absolute gale store path at all.
	for _, part := range strings.Split(rp, ":") {
		if strings.Contains(part, ".gale") &&
			filepath.IsAbs(part) {
			t.Errorf("absolute .gale rpath component %q "+
				"survived fixup: %q", part, rp)
		}
	}
}

// TestRelativeFarmRpathLinuxDepth checks the $ORIGIN prefix
// scales with how deep a file sits under the prefix.
func TestRelativeFarmRpathLinuxDepth(t *testing.T) {
	prefix := "/tmp/pkg"
	cases := map[string]string{
		filepath.Join(prefix, "bin", "app"):                 "$ORIGIN/../../../../lib",
		filepath.Join(prefix, "lib", "libfoo.so"):           "$ORIGIN/../../../../lib",
		filepath.Join(prefix, "libexec", "git-core", "git"): "$ORIGIN/../../../../../lib",
	}
	for file, want := range cases {
		got := relativeFarmRpathLinux(prefix, file)
		if got != want {
			t.Errorf("relativeFarmRpathLinux(%q)=%q, want %q",
				file, got, want)
		}
	}
}

// buildBinaryWithRpath links a trivial ELF with the given
// RUNPATH baked in at link time (no patchelf), returning the
// binary's path under a fresh pkg prefix. It is the shared setup
// for the RelocateStaleRpaths invariants below.
func buildBinaryWithRpath(t *testing.T, rpath string) string {
	t.Helper()
	pkgDir := t.TempDir()
	binDir := filepath.Join(pkgDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mainSrc := filepath.Join(pkgDir, "main.c")
	if err := os.WriteFile(mainSrc,
		[]byte("int main(void){return 0;}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(binDir, "app")
	if out, err := exec.Command(
		"cc", "-o", binPath, mainSrc,
		"-Wl,-rpath,"+rpath,
		"-Wl,--enable-new-dtags",
	).CombinedOutput(); err != nil {
		t.Skipf("cc link failed: %v\n%s", err, out)
	}
	return binPath
}

// TestRelocateStaleRpathsLeavesRelativeRpathIntact is the
// regression guard for issue #24. A Linux prebuilt that already
// carries an $ORIGIN-relative rpath (baked at CI build time since
// #26) must survive the install-time RelocateStaleRpaths step
// BYTE-FOR-BYTE, with patchelf PRESENT. The relocation must touch
// only foreign absolute .gale/pkg and .gale/lib parts; a relative
// $ORIGIN entry matches neither marker, so the relocation must
// leave the binary byte-identical and never rewrite the rpath to
// an absolute /home/.../.gale store path.
//
// Critically this runs WITH patchelf on PATH so the marker-
// matching loop (not the patchelf-absent short-circuit) is the
// code under test. If someone later broadened the markers to
// catch $ORIGIN, this test fails. See
// docs/dev/relocatable-binaries.md.
func TestRelocateStaleRpathsLeavesRelativeRpathIntact(t *testing.T) {
	if _, err := exec.LookPath("patchelf"); err != nil {
		t.Skip("patchelf not available")
	}
	if _, err := exec.LookPath("cc"); err != nil {
		t.Skip("cc not available")
	}
	readelfBin, err := exec.LookPath("readelf")
	if err != nil {
		t.Skip("readelf not available")
	}

	relRpath := "$ORIGIN/../lib:$ORIGIN/../../../../lib"
	binPath := buildBinaryWithRpath(t, relRpath)
	pkgDir := filepath.Dir(filepath.Dir(binPath))

	rpBefore := readelfRunpath(t, readelfBin, binPath)
	if !strings.Contains(rpBefore, "$ORIGIN") {
		t.Fatalf("setup: expected $ORIGIN-relative rpath, got %q", rpBefore)
	}
	if strings.Contains(rpBefore, ".gale/pkg/") ||
		strings.Contains(rpBefore, ".gale/lib") {
		t.Fatalf("setup: rpath leaked absolute gale path: %q", rpBefore)
	}

	before, errRead := os.ReadFile(binPath)
	if errRead != nil {
		t.Fatal(errRead)
	}

	// patchelf IS available here: the relocation walks the rpath
	// parts and must find nothing to rewrite in a relative rpath.
	storeRoot := filepath.Join(t.TempDir(), ".gale", "pkg")
	if err := RelocateStaleRpaths(pkgDir, storeRoot); err != nil {
		t.Fatalf("RelocateStaleRpaths: %v", err)
	}

	after, errRead2 := os.ReadFile(binPath)
	if errRead2 != nil {
		t.Fatal(errRead2)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("relocation mutated a relative-rpath prebuilt "+
			"(%d -> %d bytes) with patchelf present; a $ORIGIN rpath "+
			"must pass through byte-for-byte", len(before), len(after))
	}

	rpAfter := readelfRunpath(t, readelfBin, binPath)
	if rpAfter != rpBefore {
		t.Errorf("relative rpath was rewritten: %q -> %q", rpBefore, rpAfter)
	}
	if strings.Contains(rpAfter, storeRoot) ||
		strings.Contains(rpAfter, ".gale/pkg/") ||
		strings.Contains(rpAfter, ".gale/lib") {
		t.Errorf("relocation baked an absolute store path: %q", rpAfter)
	}
}

// TestRelocateStaleRpathsNoPatchelfIsByteForByteNoError is the
// direct test of issue #24's core promise: on a FRESH Linux box
// with NO patchelf installed, the install-time RelocateStaleRpaths
// step must NOT error (which would trigger a source rebuild) and
// must leave the prebuilt binary byte-for-byte. We simulate the
// fresh box by clearing PATH so patchelf cannot be found, then
// assert nil error and an unmodified binary even for the worst
// case: a binary that DOES carry a stale absolute store rpath
// (the only input this step would otherwise rewrite). Without
// patchelf there is nothing to rewrite, so the install proceeds
// with the artifact untouched rather than failing.
//
// readelf is resolved to an absolute path BEFORE PATH is cleared
// so the post-clear rpath assertion still works on the fresh box.
func TestRelocateStaleRpathsNoPatchelfIsByteForByteNoError(t *testing.T) {
	if _, err := exec.LookPath("cc"); err != nil {
		t.Skip("cc not available")
	}
	readelfBin, err := exec.LookPath("readelf")
	if err != nil {
		t.Skip("readelf not available")
	}

	// Build with a stale absolute store rpath: the one input
	// RelocateStaleRpaths exists to rewrite. If the no-patchelf
	// path is byte-for-byte even for THIS input, it is for all.
	const relPart = "openssl/3.6.1-2/lib"
	foreignRpath := "/home/runner/.gale/pkg/" + relPart
	binPath := buildBinaryWithRpath(t, foreignRpath)
	pkgDir := filepath.Dir(filepath.Dir(binPath))

	rpBefore := readelfRunpath(t, readelfBin, binPath)
	if !strings.Contains(rpBefore, "/home/runner/.gale/pkg/") {
		t.Fatalf("setup: expected foreign absolute rpath, got %q", rpBefore)
	}

	before, errRead := os.ReadFile(binPath)
	if errRead != nil {
		t.Fatal(errRead)
	}

	// Simulate a fresh box with no patchelf: clear PATH so the
	// patchelf lookup inside RelocateStaleRpaths fails.
	t.Setenv("PATH", "")
	if _, err := exec.LookPath("patchelf"); err == nil {
		t.Fatal("setup: patchelf still resolvable after clearing PATH")
	}

	storeRoot := filepath.Join(t.TempDir(), ".gale", "pkg")
	if err := RelocateStaleRpaths(pkgDir, storeRoot); err != nil {
		t.Fatalf("RelocateStaleRpaths must no-op (not error) when "+
			"patchelf is absent, so the install does not fall back to a "+
			"source rebuild; got: %v", err)
	}

	after, errRead2 := os.ReadFile(binPath)
	if errRead2 != nil {
		t.Fatal(errRead2)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("relocation mutated the prebuilt (%d -> %d bytes) "+
			"with patchelf absent; a fresh-box install must leave the "+
			"artifact byte-for-byte", len(before), len(after))
	}

	rpAfter := readelfRunpath(t, readelfBin, binPath)
	if rpAfter != rpBefore {
		t.Errorf("rpath changed with patchelf absent: %q -> %q",
			rpBefore, rpAfter)
	}
}

// TestRelocateStaleRpathsRewritesAbsoluteStoreRpath models the
// actual issue #24 failure mode: an obsolete pre-#26 prebuilt
// whose RUNPATH carries the CI runner's ABSOLUTE .gale/pkg store
// path (the one that broke the loader, e.g. the libpcre2 case).
// With patchelf present the relocation MUST rewrite that foreign
// absolute path to the local store root. This is the companion to
// the relative-survives test: it proves the relocation logic is
// genuinely exercised here (not short-circuited), so "relative
// survives" means the markers selectively spared a relative rpath,
// not that the loop never ran.
func TestRelocateStaleRpathsRewritesAbsoluteStoreRpath(t *testing.T) {
	if _, err := exec.LookPath("patchelf"); err != nil {
		t.Skip("patchelf not available")
	}
	if _, err := exec.LookPath("cc"); err != nil {
		t.Skip("cc not available")
	}
	readelfBin, err := exec.LookPath("readelf")
	if err != nil {
		t.Skip("readelf not available")
	}

	// A foreign CI-baked absolute store path, as pre-#26 prebuilts
	// shipped. relPart records the suffix the relocation keeps.
	const relPart = "openssl/3.6.1-2/lib"
	foreignRpath := "/home/runner/.gale/pkg/" + relPart
	binPath := buildBinaryWithRpath(t, foreignRpath)
	pkgDir := filepath.Dir(filepath.Dir(binPath))

	rpBefore := readelfRunpath(t, readelfBin, binPath)
	if !strings.Contains(rpBefore, "/home/runner/.gale/pkg/") {
		t.Fatalf("setup: expected foreign absolute rpath, got %q", rpBefore)
	}

	storeRoot := filepath.Join(t.TempDir(), ".gale", "pkg")
	if err := RelocateStaleRpaths(pkgDir, storeRoot); err != nil {
		t.Fatalf("RelocateStaleRpaths: %v", err)
	}

	rpAfter := readelfRunpath(t, readelfBin, binPath)
	// The foreign runner home must be gone, replaced by the local
	// store root, with the package/version suffix preserved.
	if strings.Contains(rpAfter, "/home/runner/.gale/pkg/") {
		t.Errorf("foreign store path survived relocation: %q", rpAfter)
	}
	want := filepath.Join(storeRoot, relPart)
	if !strings.Contains(rpAfter, want) {
		t.Errorf("expected local store path %q in rpath, got %q",
			want, rpAfter)
	}
}

// buildStaticBinary links a trivial statically-linked ELF under
// pkgDir/bin/ and returns its path.
func buildStaticBinary(t *testing.T, pkgDir string) string {
	t.Helper()
	binDir := filepath.Join(pkgDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mainSrc := filepath.Join(pkgDir, "main.c")
	if err := os.WriteFile(mainSrc,
		[]byte("int main(void){return 0;}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(binDir, "app")
	if out, err := exec.Command(
		"cc", "-static", "-o", binPath, mainSrc,
	).CombinedOutput(); err != nil {
		t.Skipf("cc -static failed: %v\n%s", err, out)
	}
	return binPath
}

// TestFixupBinariesSkipsStaticELF guards issue #134: patchelf can
// exit 0 while corrupting a static Go (or cc -static) binary. ELFs
// with no DT_NEEDED must pass through FixupBinaries byte-for-byte
// and still execute.
func TestFixupBinariesSkipsStaticELF(t *testing.T) {
	if _, err := exec.LookPath("cc"); err != nil {
		t.Skip("cc not available")
	}

	pkgDir := t.TempDir()
	binPath := buildStaticBinary(t, pkgDir)

	before, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := FixupBinaries(pkgDir); err != nil {
		t.Fatalf("FixupBinaries: %v", err)
	}

	after, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("FixupBinaries mutated a static ELF (%d -> %d bytes)",
			len(before), len(after))
	}

	out, err := exec.Command(binPath).CombinedOutput()
	if err != nil {
		t.Fatalf("static binary failed after FixupBinaries: %v\n%s",
			err, out)
	}
}

// TestAddDepRpathsSkipsStaticELF is the AddDepRpaths mirror of
// issue #134: a static binary under a prefix with dep libs present
// must not be touched.
func TestAddDepRpathsSkipsStaticELF(t *testing.T) {
	if _, err := exec.LookPath("patchelf"); err != nil {
		t.Skip("patchelf not available")
	}
	if _, err := exec.LookPath("cc"); err != nil {
		t.Skip("cc not available")
	}

	depDir := t.TempDir()
	depLib := filepath.Join(depDir, "lib")
	if err := os.MkdirAll(depLib, 0o755); err != nil {
		t.Fatal(err)
	}
	libSrc := filepath.Join(depDir, "dep.c")
	if err := os.WriteFile(libSrc,
		[]byte("int dep_func(void){return 7;}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dylib := filepath.Join(depLib, "libdep.so")
	if out, err := exec.Command(
		"cc", "-shared", "-fPIC",
		"-Wl,-soname,libdep.so", "-o", dylib, libSrc,
	).CombinedOutput(); err != nil {
		t.Skipf("cc -shared failed: %v\n%s", err, out)
	}

	pkgDir := t.TempDir()
	binPath := buildStaticBinary(t, pkgDir)

	before, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := AddDepRpaths(pkgDir, []string{depDir}); err != nil {
		t.Fatalf("AddDepRpaths: %v", err)
	}

	after, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("AddDepRpaths mutated a static ELF (%d -> %d bytes)",
			len(before), len(after))
	}

	out, err := exec.Command(binPath).CombinedOutput()
	if err != nil {
		t.Fatalf("static binary failed after AddDepRpaths: %v\n%s",
			err, out)
	}
}
