package inspect

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/kelp/gale/internal/recipe"
)

// TestStoreNameVersion covers the path parser across the
// variety of paths inspect will actually see.
func TestStoreNameVersion(t *testing.T) {
	cases := []struct {
		in            string
		wantName      string
		wantVersion   string
		wantOK        bool
	}{
		{
			in:          "/Users/tcole/.gale/pkg/curl/8.19.0/lib/libcurl.dylib",
			wantName:    "curl",
			wantVersion: "8.19.0",
			wantOK:      true,
		},
		{
			in:          "/home/runner/.gale/pkg/openssl/3.6.1/lib",
			wantName:    "openssl",
			wantVersion: "3.6.1",
			wantOK:      true,
		},
		{
			in:     "/usr/lib/libSystem.B.dylib",
			wantOK: false,
		},
	}
	for _, tc := range cases {
		n, v, ok := storeNameVersion(tc.in)
		if ok != tc.wantOK {
			t.Errorf("%q: ok=%v want %v", tc.in, ok, tc.wantOK)
			continue
		}
		if n != tc.wantName || v != tc.wantVersion {
			t.Errorf("%q: got (%q,%q) want (%q,%q)",
				tc.in, n, v, tc.wantName, tc.wantVersion)
		}
	}
}

// TestResolveRef covers rpath resolution against a real
// filesystem layout.
func TestResolveRef(t *testing.T) {
	dir := t.TempDir()
	libDir := filepath.Join(dir, "store", "curl", "8.19.0", "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	libPath := filepath.Join(libDir, "libcurl.dylib")
	if err := os.WriteFile(libPath, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Resolves: rpath contains the lib.
	got, ok := resolveRef("@rpath/libcurl.dylib", []string{libDir})
	if !ok || got != libPath {
		t.Errorf("resolveRef existing: got (%q,%v) want (%q,true)",
			got, ok, libPath)
	}

	// Doesn't resolve: missing lib.
	_, ok = resolveRef("@rpath/libmissing.dylib", []string{libDir})
	if ok {
		t.Errorf("resolveRef missing: expected no resolution")
	}

	// Skips @-prefixed rpaths as non-absolute paths.
	_, ok = resolveRef(
		"@rpath/libcurl.dylib",
		[]string{"@loader_path/../lib"},
	)
	if ok {
		t.Errorf("resolveRef with only @-rpath: expected no resolution")
	}
}

// TestScanOverDeclaredDep verifies we detect a dep in the
// recipe that no binary under the install references.
// Cross-platform: no Mach-O/ELF parsing needed when there
// are simply no binary files under the prefix.
func TestScanOverDeclaredDep(t *testing.T) {
	prefix := t.TempDir()
	// No binaries. Recipe declares 'zlib' as a runtime dep.

	r := &recipe.Recipe{
		Package: recipe.Package{
			Name: "example", Version: "1.0",
		},
		Dependencies: recipe.Dependencies{
			Runtime: []string{"zlib"},
		},
	}

	issues, err := ScanInstalled(prefix, "example", "1.0", r)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 {
		t.Fatalf("got %d issues, want 1: %v", len(issues), issues)
	}
	got := issues[0]
	if got.Kind != KindOverDeclaredDep {
		t.Errorf("kind = %v, want %v", got.Kind, KindOverDeclaredDep)
	}
	if got.Details != "zlib" {
		t.Errorf("details = %q, want %q", got.Details, "zlib")
	}
}

// TestScanCleanPackageReportsNoIssues verifies that a
// prefix with no binaries and a recipe with no runtime
// deps produces zero issues.
func TestScanCleanPackageReportsNoIssues(t *testing.T) {
	prefix := t.TempDir()
	r := &recipe.Recipe{
		Package: recipe.Package{
			Name: "example", Version: "1.0",
		},
	}

	issues, err := ScanInstalled(prefix, "example", "1.0", r)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Errorf("got %d issues, want 0: %v", len(issues), issues)
	}
}

// TestScanReportsUnresolvableRef exercises the full
// Mach-O-reading path on darwin with a real compiled
// binary that references @rpath/libfake.dylib through an
// install_name_tool rewrite. Darwin-only because the fixture
// requires install_name_tool.
func TestScanReportsUnresolvableRef(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin only: requires install_name_tool")
	}

	prefix := t.TempDir()
	binDir := filepath.Join(prefix, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	bin := compileBinary(t, binDir)

	// Add an rpath that points somewhere that won't have
	// libfake.dylib.
	if err := exec.Command("install_name_tool",
		"-add_rpath", "/nonexistent/lib", bin).Run(); err != nil {
		t.Fatalf("add_rpath: %v", err)
	}

	// Now add a fake LC_LOAD_DYLIB reference by renaming
	// the existing libSystem reference — this makes the
	// binary reference @rpath/libfake.dylib which cannot
	// be resolved against our rpaths.
	//
	// Find an existing dep to swap.
	refs, err := readBinary(bin)
	if err != nil {
		t.Fatal(err)
	}
	if refs == nil || len(refs.deps) == 0 {
		t.Fatal("binary has no deps to rewrite")
	}
	orig := refs.deps[0]
	if err := exec.Command("install_name_tool",
		"-change", orig, "@rpath/libfake.dylib", bin).Run(); err != nil {
		t.Fatalf("change dep: %v", err)
	}

	// Codesign to keep macOS happy.
	_ = exec.Command("codesign", "--force",
		"--sign", "-", bin).Run()

	issues, err := ScanInstalled(prefix, "example", "1.0", nil)
	if err != nil {
		t.Fatal(err)
	}

	var sawUnresolvable, sawStale bool
	for _, iss := range issues {
		if iss.Kind == KindUnresolvableRef &&
			iss.Details == "@rpath/libfake.dylib" {
			sawUnresolvable = true
		}
		if iss.Kind == KindStaleRpath &&
			iss.Details == "/nonexistent/lib" {
			sawStale = true
		}
	}

	if !sawUnresolvable {
		t.Errorf("expected unresolvable-ref, got %v", issues)
	}
	if !sawStale {
		t.Errorf("expected stale-rpath, got %v", issues)
	}
}

func compileBinary(t *testing.T, dir string) string {
	t.Helper()
	src := filepath.Join(dir, "dummy.c")
	bin := filepath.Join(dir, "dummy")
	if err := os.WriteFile(src,
		[]byte("int main() { return 0; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cc",
		"-Wl,-headerpad_max_install_names",
		"-o", bin, src)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Skipf("cc not available: %v\n%s", err, out)
	}
	return bin
}
