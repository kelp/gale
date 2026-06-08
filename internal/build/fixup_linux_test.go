//go:build linux

package build

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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
