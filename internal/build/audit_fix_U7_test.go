//go:build darwin

package build

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for audit unit U7 (issues #52, #53, #54). Helpers
// compileTinyBinary, compileWithHeaderpad, isCodeSigned,
// otoolOutput, and newUnversionedRefFixture live in
// fixup_darwin_test.go.

// --- #53: libexec/ and lib64/ are not lib/ ---

func TestFixupBinariesSkipsLibexecExecutable(t *testing.T) {
	// An executable under libexec/ must not be classified as
	// "in lib/": that applies the dylib-only -id operation
	// and adds @loader_path instead of an @executable_path-
	// anchored rpath, breaking dylib resolution at runtime.
	dir := t.TempDir()
	sub := filepath.Join(dir, "libexec", "git-core")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := compileWithHeaderpad(t, sub, "git-remote-https")

	if err := FixupBinaries(dir); err != nil {
		t.Fatalf("FixupBinaries error: %v", err)
	}

	if existingRpaths(bin)["@loader_path"] {
		t.Errorf("libexec executable got dylib-only "+
			"@loader_path rpath; misclassified as lib/:\n%s",
			otoolOutput(t, bin))
	}
}

func TestFixupBinariesSkipsLib64(t *testing.T) {
	// lib64/ shares the "lib" prefix but is not lib/.
	dir := t.TempDir()
	sub := filepath.Join(dir, "lib64")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := compileWithHeaderpad(t, sub, "tool")

	if err := FixupBinaries(dir); err != nil {
		t.Fatalf("FixupBinaries error: %v", err)
	}

	if existingRpaths(bin)["@loader_path"] {
		t.Errorf("lib64/ file got dylib-only @loader_path "+
			"rpath; misclassified as lib/:\n%s",
			otoolOutput(t, bin))
	}
}

func TestFixupBinariesStillTreatsLibAsLib(t *testing.T) {
	// Guard against over-correcting the prefix check: a
	// dylib directly under lib/ keeps the dylib fixups.
	dir := t.TempDir()
	libDir := filepath.Join(dir, "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "lib.c")
	if err := os.WriteFile(src,
		[]byte("int libfunc(void) { return 1; }\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	libPath := filepath.Join(libDir, "libtest.dylib")
	cmd := exec.Command("cc", "-shared",
		"-install_name", "/tmp/fake/lib/libtest.dylib",
		"-o", libPath, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cc -shared failed: %v\n%s", err, out)
	}

	if err := FixupBinaries(dir); err != nil {
		t.Fatalf("FixupBinaries error: %v", err)
	}

	if !existingRpaths(libPath)["@loader_path"] {
		t.Errorf("dylib in lib/ lost its @loader_path "+
			"rpath:\n%s", otoolOutput(t, libPath))
	}
}

// --- #52: failed rpath add must not leave the file unsigned ---

func TestAddRpathRetryRestoresSignatureWhenAddFails(t *testing.T) {
	// addRpathRetry strips the ad-hoc signature before its
	// retry. When the retry also fails, it must re-sign the
	// file — the source-build install path never re-signs,
	// and Apple Silicon SIGKILLs unsigned Mach-Os on exec.
	dir := t.TempDir()
	bin := compileWithHeaderpad(t, dir, "app")

	// Pre-add the rpath so both add attempts fail with
	// "already has LC_RPATH" — a deterministic failure.
	rpath := "/gale/test/lib"
	if err := exec.Command("install_name_tool",
		"-add_rpath", rpath, bin).Run(); err != nil {
		t.Fatalf("setup add_rpath: %v", err)
	}
	if err := exec.Command("codesign", "--force", "--sign",
		"-", bin).Run(); err != nil {
		t.Fatalf("setup codesign: %v", err)
	}
	if !isCodeSigned(t, bin) {
		t.Fatal("setup: binary not signed")
	}

	if err := addRpathRetry(bin, rpath); err == nil {
		t.Fatal("expected addRpathRetry to fail on duplicate rpath")
	}
	if !isCodeSigned(t, bin) {
		t.Error("binary left unsigned after failed rpath add")
	}
}

// --- #205: a provable canonicalization failure aborts the build ---

// makeUnwritable strips write permission from a Mach-O and its
// directory so install_name_tool cannot rewrite it, whichever of
// in-place or temp-file-and-rename the tool chooses. Restores both
// on cleanup — t.TempDir's own cleanup cannot remove a read-only
// directory.
func makeUnwritable(t *testing.T, file string) {
	t.Helper()
	dir := filepath.Dir(file)
	if err := os.Chmod(file, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(dir, 0o755)
		_ = os.Chmod(file, 0o755)
	})
}

func TestAddDepRpathsFailsWhenCanonicalizationCannotBeApplied(t *testing.T) {
	// A ref that gale PROVED needs canonicalization — a dep store
	// provides the name and it resolves to a versioned real dylib
	// the farm holds — must abort the build when the rewrite
	// cannot be applied. Warning and shipping the artifact turns a
	// build-time notice nobody reads into a dyld abort on a user's
	// machine (issue #205).
	if os.Geteuid() == 0 {
		t.Skip("running as root: read-only files are still writable")
	}
	f := newUnversionedRefFixture(t)
	makeUnwritable(t, f.binPath)

	err := AddDepRpaths(f.pkgDir, []string{f.depDir})
	if err == nil {
		t.Fatal("AddDepRpaths returned nil for a canonicalization " +
			"it could not apply")
	}
	if !strings.Contains(err.Error(), "canonicalize") {
		t.Errorf("got error %q, want one naming the canonicalize step", err)
	}
}

func TestAddDepRpathsSucceedsWhenNoRefIsCanonicalizable(t *testing.T) {
	// The blast radius of the fatal path, made explicit: only a
	// ref canonicalDepName resolved can abort. Here no dep store
	// provides libdep, so nothing enters the rewrite plan — and an
	// unwritable binary that merely misses its farm rpath stays
	// non-fatal, exactly as before (addRpathRetry warns and skips).
	f := newUnversionedRefFixture(t)
	makeUnwritable(t, f.binPath)

	// A dep store dir that provides no dylib at all.
	emptyDep := t.TempDir()
	if err := os.MkdirAll(filepath.Join(emptyDep, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := AddDepRpaths(f.pkgDir, []string{emptyDep}); err != nil {
		t.Fatalf("AddDepRpaths must not fail when no ref is "+
			"canonicalizable: %v", err)
	}
}

// --- #54: RelocateStaleRpaths re-signs only rewritten files ---

func TestRelocateStaleRpathsLeavesUntouchedMachOByteIdentical(t *testing.T) {
	// A binary with no rpaths has nothing to rewrite, so
	// RelocateStaleRpaths must leave it byte-for-byte
	// identical to the attested archive — no gratuitous
	// re-sign. The freshly compiled binary carries the
	// linker-signed flag that codesign re-signing removes,
	// so any unconditional codesign changes the bytes.
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := compileTinyBinary(t, binDir, "clean")

	before, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}

	if err := RelocateStaleRpaths(dir, "/Users/tcole/.gale/pkg"); err != nil {
		t.Fatalf("RelocateStaleRpaths: %v", err)
	}

	after, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("RelocateStaleRpaths mutated a Mach-O with "+
			"no stale rpaths (%d -> %d bytes)",
			len(before), len(after))
	}
}

func TestRelocateStaleRpathsSkipsObjectFiles(t *testing.T) {
	// Shipped .o files are never executed and codesign can
	// fail on them; the walk must filter them like every
	// sibling pass (FixupBinaries, EnsureCodeSigned).
	dir := t.TempDir()
	libDir := filepath.Join(dir, "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "obj.c")
	if err := os.WriteFile(src,
		[]byte("int objfunc(void) { return 1; }\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	objPath := filepath.Join(libDir, "python.o")
	cmd := exec.Command("cc", "-c", "-o", objPath, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cc -c failed: %v\n%s", err, out)
	}

	before, err := os.ReadFile(objPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := RelocateStaleRpaths(dir, "/Users/tcole/.gale/pkg"); err != nil {
		t.Fatalf("RelocateStaleRpaths errored on .o file: %v", err)
	}

	after, err := os.ReadFile(objPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("RelocateStaleRpaths mutated a .o file")
	}
}

func TestRelocateStaleRpathsSkipsDSYMBundles(t *testing.T) {
	// Mach-O DWARF data inside .dSYM bundles must be
	// filtered, matching FixupBinaries and EnsureCodeSigned.
	dir := t.TempDir()
	bin := compileTinyBinary(t, dir, "app")

	dwarfDir := filepath.Join(dir, "lib", "app.dSYM",
		"Contents", "Resources", "DWARF")
	if err := os.MkdirAll(dwarfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	dwarf := filepath.Join(dwarfDir, "app")
	if err := os.WriteFile(dwarf, data, 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}

	if err := RelocateStaleRpaths(dir, "/Users/tcole/.gale/pkg"); err != nil {
		t.Fatalf("RelocateStaleRpaths errored on .dSYM: %v", err)
	}

	after, err := os.ReadFile(dwarf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, after) {
		t.Error("RelocateStaleRpaths mutated a .dSYM Mach-O")
	}
}

func TestRelocateStaleRpathsResignsRewrittenFile(t *testing.T) {
	// Guard the positive path: a file whose rpath WAS
	// rewritten must still be re-signed afterwards.
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := compileWithHeaderpad(t, binDir, "dummy")

	staleRpath := "/Users/runner/.gale/pkg/openssl/3.6.1/lib"
	if err := exec.Command("install_name_tool",
		"-add_rpath", staleRpath, bin).Run(); err != nil {
		t.Fatalf("add stale rpath: %v", err)
	}
	if err := exec.Command("codesign", "--force", "--sign",
		"-", bin).Run(); err != nil {
		t.Fatalf("setup codesign: %v", err)
	}

	currentStoreRoot := "/Users/tcole/.gale/pkg"
	if err := RelocateStaleRpaths(dir, currentStoreRoot); err != nil {
		t.Fatalf("RelocateStaleRpaths: %v", err)
	}

	if !existingRpaths(bin)[currentStoreRoot+"/openssl/3.6.1/lib"] {
		t.Errorf("stale rpath not rewritten:\n%s",
			otoolOutput(t, bin))
	}
	if !isCodeSigned(t, bin) {
		t.Error("rewritten binary left unsigned")
	}
}
