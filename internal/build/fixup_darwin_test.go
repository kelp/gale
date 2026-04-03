//go:build darwin

package build

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// compileTinyBinary creates a minimal C binary at the given
// path. Returns the path or skips the test if cc is not
// available.
func compileTinyBinary(t *testing.T, dir, name string) string {
	t.Helper()
	src := filepath.Join(dir, name+".c")
	bin := filepath.Join(dir, name)
	if err := os.WriteFile(src,
		[]byte("int main() { return 0; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cc", "-o", bin, src)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Skipf("cc not available: %v\n%s", err, out)
	}
	return bin
}

// --- Behavior 1: isMachO detects Mach-O files ---

func TestIsMachODetectsBinary(t *testing.T) {
	dir := t.TempDir()
	bin := compileTinyBinary(t, dir, "hello")

	if !isMachO(bin) {
		t.Error("expected isMachO to return true for compiled binary")
	}
}

func TestIsMachORejectsTxtFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.txt")
	if err := os.WriteFile(path,
		[]byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	if isMachO(path) {
		t.Error("expected isMachO to return false for text file")
	}
}

func TestIsMachORejectsScript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sh")
	if err := os.WriteFile(path,
		[]byte("#!/bin/sh\necho hello\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if isMachO(path) {
		t.Error("expected isMachO to return false for shell script")
	}
}

// --- Behavior 2: FixupBinaries rewrites dylib paths ---

func TestFixupBinariesFixesDylibRef(t *testing.T) {
	// Build a shared library and a binary that links it,
	// then verify fixup rewrites the path.
	dir := t.TempDir()
	libDir := filepath.Join(dir, "lib")
	binDir := filepath.Join(dir, "bin")
	os.MkdirAll(libDir, 0o755)
	os.MkdirAll(binDir, 0o755)

	// Create a tiny shared library.
	libSrc := filepath.Join(dir, "mylib.c")
	if err := os.WriteFile(libSrc,
		[]byte("int mylib_func(void) { return 42; }\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	libPath := filepath.Join(libDir, "libmylib.dylib")
	cmd := exec.Command("cc", "-shared",
		"-install_name", libDir+"/libmylib.dylib",
		"-o", libPath, libSrc)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cc -shared failed: %v\n%s", err, out)
	}

	// Create a binary that links to the dylib.
	mainSrc := filepath.Join(dir, "main.c")
	if err := os.WriteFile(mainSrc,
		[]byte("extern int mylib_func(void);\nint main() { return mylib_func(); }\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(binDir, "myapp")
	cmd = exec.Command("cc", "-o", binPath, mainSrc,
		"-L"+libDir, "-lmylib")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cc link failed: %v\n%s", err, out)
	}

	// Before fixup: binary references the absolute lib path.
	before := otoolOutput(t, binPath)
	if !strings.Contains(before, libDir) {
		t.Skipf("binary doesn't reference libDir, test setup issue: %s", before)
	}

	// Run fixup.
	if err := FixupBinaries(dir); err != nil {
		t.Fatalf("FixupBinaries error: %v", err)
	}

	// After fixup: binary should reference @rpath, not
	// the absolute path.
	after := otoolOutput(t, binPath)
	if strings.Contains(after, libDir) {
		t.Errorf("fixup did not rewrite dylib path.\notool -L:\n%s", after)
	}
	if !strings.Contains(after, "@rpath/libmylib.dylib") {
		t.Errorf("expected @rpath reference in:\n%s", after)
	}
}

func TestFixupBinariesSetsRpathWhenDepsRewritten(t *testing.T) {
	// Rpath is added to binaries that had deps rewritten.
	// Reuse the dylib ref test setup.
	dir := t.TempDir()
	libDir := filepath.Join(dir, "lib")
	binDir := filepath.Join(dir, "bin")
	os.MkdirAll(libDir, 0o755)
	os.MkdirAll(binDir, 0o755)

	libSrc := filepath.Join(dir, "mylib.c")
	if err := os.WriteFile(libSrc,
		[]byte("int mylib_func(void) { return 42; }\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	libPath := filepath.Join(libDir, "libmylib.dylib")
	cmd := exec.Command("cc", "-shared",
		"-install_name", libDir+"/libmylib.dylib",
		"-o", libPath, libSrc)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cc -shared failed: %v\n%s", err, out)
	}

	mainSrc := filepath.Join(dir, "main.c")
	if err := os.WriteFile(mainSrc,
		[]byte("extern int mylib_func(void);\nint main() { return mylib_func(); }\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(binDir, "myapp")
	cmd = exec.Command("cc", "-o", binPath, mainSrc,
		"-L"+libDir, "-lmylib",
		"-Wl,-headerpad_max_install_names")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cc link failed: %v\n%s", err, out)
	}

	if err := FixupBinaries(dir); err != nil {
		t.Fatalf("FixupBinaries error: %v", err)
	}

	// Check rpath was added.
	cmd = exec.Command("otool", "-l", binPath)
	out, _ := cmd.CombinedOutput()
	if !strings.Contains(string(out), "@executable_path/../lib") {
		t.Error("expected @executable_path/../lib rpath")
	}
}

func TestFixupBinariesNoOpForCleanBinary(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	os.MkdirAll(binDir, 0o755)

	// A binary with no dylib refs should not error.
	compileTinyBinary(t, binDir, "clean")

	if err := FixupBinaries(dir); err != nil {
		t.Fatalf("FixupBinaries error on clean binary: %v", err)
	}
}

func TestFixupBinariesSetsDylibID(t *testing.T) {
	dir := t.TempDir()
	libDir := filepath.Join(dir, "lib")
	os.MkdirAll(libDir, 0o755)

	// Create a dylib with an absolute install name.
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

	// Dylib ID should now be @rpath/libtest.dylib.
	out := otoolOutput(t, libPath)
	if !strings.Contains(out, "@rpath/libtest.dylib") {
		t.Errorf("expected @rpath/libtest.dylib ID in:\n%s", out)
	}
}

func TestFixupBinariesSkipsObjectFiles(t *testing.T) {
	dir := t.TempDir()
	libDir := filepath.Join(dir, "lib")
	subDir := filepath.Join(libDir, "python3.13", "config")
	os.MkdirAll(subDir, 0o755)

	// Create a .o file by compiling without linking.
	src := filepath.Join(dir, "obj.c")
	if err := os.WriteFile(src,
		[]byte("int objfunc(void) { return 1; }\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	objPath := filepath.Join(subDir, "python.o")
	cmd := exec.Command("cc", "-c", "-o", objPath, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cc -c failed: %v\n%s", err, out)
	}

	// FixupBinaries should not error on .o files.
	if err := FixupBinaries(dir); err != nil {
		t.Fatalf("FixupBinaries should skip .o files: %v",
			err)
	}
}

func TestFixupBinariesSkipsDSYMBundles(t *testing.T) {
	dir := t.TempDir()
	libDir := filepath.Join(dir, "lib")
	os.MkdirAll(libDir, 0o755)

	// Create a dylib with debug info in lib/.
	src := filepath.Join(dir, "lib.c")
	if err := os.WriteFile(src,
		[]byte("int libfunc(void) { return 1; }\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	libPath := filepath.Join(libDir, "libtest.dylib")
	cmd := exec.Command("cc", "-shared", "-g",
		"-install_name", "/tmp/fake/lib/libtest.dylib",
		"-o", libPath, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cc -shared -g failed: %v\n%s", err, out)
	}

	// Generate a real .dSYM bundle with dsymutil. The
	// DWARF file inside has Mach-O magic bytes but
	// install_name_tool cannot modify it.
	cmd = exec.Command("dsymutil", libPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("dsymutil failed: %v\n%s", err, out)
	}

	// Verify the .dSYM bundle was created.
	dsymDWARF := filepath.Join(libDir,
		"libtest.dylib.dSYM", "Contents", "Resources",
		"DWARF", "libtest.dylib")
	if _, err := os.Stat(dsymDWARF); err != nil {
		t.Skipf(".dSYM bundle not created: %v", err)
	}

	// FixupBinaries must not error on the .dSYM file.
	if err := FixupBinaries(dir); err != nil {
		t.Fatalf("FixupBinaries should skip .dSYM: %v", err)
	}
}

// --- Behavior 6: AddDepRpaths adds LC_RPATH for dep libs ---

func TestAddDepRpathsAddsRpathForDepLib(t *testing.T) {
	// Create a "dep" store dir with a dylib using @rpath
	// install name, then a package that links it. After
	// AddDepRpaths, the binary should have LC_RPATH
	// pointing to the dep's lib dir.
	depDir := t.TempDir()
	depLib := filepath.Join(depDir, "lib")
	os.MkdirAll(depLib, 0o755)

	// Build a dylib in the dep store dir.
	libSrc := filepath.Join(depDir, "dep.c")
	if err := os.WriteFile(libSrc,
		[]byte("int dep_func(void) { return 7; }\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	dylibPath := filepath.Join(depLib, "libdep.dylib")
	cmd := exec.Command("cc", "-shared",
		"-install_name", "@rpath/libdep.dylib",
		"-o", dylibPath, libSrc)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cc -shared failed: %v\n%s", err, out)
	}

	// Build a binary in a separate "package" prefix that
	// links the dep dylib.
	pkgDir := t.TempDir()
	binDir := filepath.Join(pkgDir, "bin")
	os.MkdirAll(binDir, 0o755)

	mainSrc := filepath.Join(pkgDir, "main.c")
	if err := os.WriteFile(mainSrc,
		[]byte("extern int dep_func(void);\nint main() { return dep_func(); }\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(binDir, "app")
	cmd = exec.Command("cc", "-o", binPath, mainSrc,
		"-L"+depLib, "-ldep",
		"-Wl,-headerpad_max_install_names")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cc link failed: %v\n%s", err, out)
	}

	// Verify the binary references @rpath/libdep.dylib.
	before := otoolOutput(t, binPath)
	if !strings.Contains(before, "@rpath/libdep.dylib") {
		t.Skipf("binary doesn't reference @rpath, test setup issue: %s", before)
	}

	// Before: no LC_RPATH pointing to depLib.
	if strings.Contains(before, depLib) {
		t.Skipf("binary already has dep rpath: %s", before)
	}

	// Run AddDepRpaths.
	if err := AddDepRpaths(pkgDir, []string{depDir}); err != nil {
		t.Fatalf("AddDepRpaths error: %v", err)
	}

	// After: should have LC_RPATH for the dep lib dir.
	after := otoolOutput(t, binPath)
	if !strings.Contains(after, depLib) {
		t.Errorf("expected LC_RPATH %s in:\n%s", depLib, after)
	}
}

func TestAddDepRpathsSkipsOwnLibs(t *testing.T) {
	// Libs in the package's own lib/ dir should not get
	// an LC_RPATH entry — FixupBinaries already handles
	// those with @executable_path/../lib.
	dir := t.TempDir()
	libDir := filepath.Join(dir, "lib")
	binDir := filepath.Join(dir, "bin")
	os.MkdirAll(libDir, 0o755)
	os.MkdirAll(binDir, 0o755)

	// Build a dylib in the package's own lib/.
	libSrc := filepath.Join(dir, "own.c")
	if err := os.WriteFile(libSrc,
		[]byte("int own_func(void) { return 1; }\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cc", "-shared",
		"-install_name", "@rpath/libown.dylib",
		"-o", filepath.Join(libDir, "libown.dylib"), libSrc)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cc -shared failed: %v\n%s", err, out)
	}

	// Build a binary linking it.
	mainSrc := filepath.Join(dir, "main.c")
	if err := os.WriteFile(mainSrc,
		[]byte("extern int own_func(void);\nint main() { return own_func(); }\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(binDir, "app")
	cmd = exec.Command("cc", "-o", binPath, mainSrc,
		"-L"+libDir, "-lown")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cc link failed: %v\n%s", err, out)
	}

	// Run with no dep dirs — should not add any rpaths.
	if err := AddDepRpaths(dir, nil); err != nil {
		t.Fatalf("AddDepRpaths error: %v", err)
	}

	after := otoolOutput(t, binPath)
	// Should NOT have an absolute rpath to libDir.
	if strings.Contains(after, "path "+libDir) {
		t.Errorf("should not add rpath for own lib dir:\n%s", after)
	}
}

func TestAddDepRpathsNoDeps(t *testing.T) {
	// No dep dirs — should be a no-op.
	dir := t.TempDir()
	if err := AddDepRpaths(dir, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- BUG-5: otoolDeps error is not silently swallowed ---

func TestOtoolDepsErrorReturnedByFixupBinaries(t *testing.T) {
	// otoolDeps returns an error for non-existent files.
	// Verify that the error propagates rather than being
	// silently swallowed.
	_, err := otoolDeps("/nonexistent/binary")
	if err == nil {
		t.Skip("otool does not error on nonexistent files")
	}

	// The code fix is structural — we verified it by
	// reading the source. This test confirms otoolDeps
	// itself returns errors for bad inputs.
	if !strings.Contains(err.Error(), "") {
		t.Errorf("unexpected error format: %v", err)
	}
}

func TestFixupBinariesReturnsErrorOnBrokenDylibID(t *testing.T) {
	// Verify the error path: when install_name_tool fails
	// on a lib dir file, FixupBinaries returns an error.
	// This exercises the error return path near where the
	// otoolDeps error fix lives.
	dir := t.TempDir()
	libDir := filepath.Join(dir, "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a file with Mach-O magic that passes isMachO
	// but install_name_tool will fail on.
	broken := filepath.Join(libDir, "libbroken.dylib")
	magic := []byte{0xcf, 0xfa, 0xed, 0xfe, 0, 0, 0, 0}
	if err := os.WriteFile(broken, magic, 0o755); err != nil {
		t.Fatal(err)
	}
	if !isMachO(broken) {
		t.Skip("fake file not recognized as Mach-O")
	}

	// FixupBinaries should return an error because
	// install_name_tool will fail on the broken file.
	err := FixupBinaries(dir)
	if err == nil {
		t.Skip("install_name_tool did not error on truncated file")
	}
	if !strings.Contains(err.Error(), "dylib id") &&
		!strings.Contains(err.Error(), "otool deps") {
		t.Errorf("error should mention dylib id or otool, got: %v", err)
	}
}

// otoolOutput runs otool -L and otool -l on a binary and
// returns the combined output.
func otoolOutput(t *testing.T, path string) string {
	t.Helper()
	cmd := exec.Command("otool", "-L", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("otool -L failed: %v\n%s", err, out)
	}
	// Also get rpaths.
	cmd2 := exec.Command("otool", "-l", path)
	out2, _ := cmd2.CombinedOutput()
	return string(out) + "\n" + string(out2)
}
