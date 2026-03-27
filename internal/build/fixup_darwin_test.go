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
