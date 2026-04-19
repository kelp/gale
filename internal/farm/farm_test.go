package farm

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestIsVersionedDylib(t *testing.T) {
	type tc struct {
		os   string
		name string
		want bool
	}
	cases := []tc{
		// darwin
		{"darwin", "libcurl.4.dylib", true},
		{"darwin", "libssl.3.dylib", true},
		{"darwin", "libfoo.1.2.3.dylib", true},
		{"darwin", "libcurl.dylib", false},
		{"darwin", "libc++.dylib", false},
		{"darwin", "libc++.1.dylib", true},
		{"darwin", "libfoo.a", false},
		{"darwin", "random.txt", false},
		// linux
		{"linux", "libcurl.so.4", true},
		{"linux", "libssl.so.3.1.4", true},
		{"linux", "libcurl.so", false},
		{"linux", "libfoo.a", false},
	}
	for _, c := range cases {
		if runtime.GOOS != c.os {
			continue
		}
		got := IsVersionedDylib(c.name)
		if got != c.want {
			t.Errorf("IsVersionedDylib(%q) = %v, want %v",
				c.name, got, c.want)
		}
	}
}

// storeLayout creates a fake .gale/pkg/<name>/<ver>/lib
// under root and writes the given filenames as empty
// files. Returns the absolute storeDir path.
func storeLayout(
	t *testing.T, root, name, version string, files []string,
) string {
	t.Helper()
	storeDir := filepath.Join(root, "pkg", name, version)
	libDir := filepath.Join(storeDir, "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		p := filepath.Join(libDir, f)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return storeDir
}

func TestPopulateAddsVersionedDylibs(t *testing.T) {
	root := t.TempDir()
	farmDir := filepath.Join(root, "lib")
	storeDir := storeLayout(t, root, "curl", "8.19.0",
		[]string{versionedName("libcurl", "4"), aliasName("libcurl")})

	if err := Populate(storeDir, farmDir); err != nil {
		t.Fatal(err)
	}

	versioned := filepath.Join(farmDir, versionedName("libcurl", "4"))
	target, err := os.Readlink(versioned)
	if err != nil {
		t.Fatalf("expected symlink at %s: %v", versioned, err)
	}
	wantTarget := filepath.Join(
		storeDir, "lib", versionedName("libcurl", "4"))
	if target != wantTarget {
		t.Errorf("target = %q, want %q", target, wantTarget)
	}

	// Unversioned basename must NOT be farmed.
	aliased := filepath.Join(farmDir, aliasName("libcurl"))
	if _, err := os.Lstat(aliased); err == nil {
		t.Errorf("unversioned %s should not be in farm",
			aliasName("libcurl"))
	}
}

func TestPopulateSkipsPackagesWithoutVersionedDylibs(t *testing.T) {
	root := t.TempDir()
	farmDir := filepath.Join(root, "lib")
	storeDir := storeLayout(t, root, "foo", "1.0",
		[]string{aliasName("libfoo")})

	if err := Populate(storeDir, farmDir); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(farmDir)
	if len(entries) != 0 {
		t.Errorf("farm should be empty, got %d entries", len(entries))
	}
}

func TestPopulateConflictSamePackageOverwrites(t *testing.T) {
	root := t.TempDir()
	farmDir := filepath.Join(root, "lib")
	storeOld := storeLayout(t, root, "curl", "8.18.0",
		[]string{versionedName("libcurl", "4")})
	storeNew := storeLayout(t, root, "curl", "8.19.0",
		[]string{versionedName("libcurl", "4")})

	if err := Populate(storeOld, farmDir); err != nil {
		t.Fatal(err)
	}
	if err := Populate(storeNew, farmDir); err != nil {
		t.Fatalf("newer same-package install should succeed: %v",
			err)
	}

	got, err := os.Readlink(filepath.Join(
		farmDir, versionedName("libcurl", "4")))
	if err != nil {
		t.Fatal(err)
	}
	wantTarget := filepath.Join(
		storeNew, "lib", versionedName("libcurl", "4"))
	if got != wantTarget {
		t.Errorf("target = %q, want %q (newer should win)",
			got, wantTarget)
	}
}

func TestPopulateConflictDifferentPackageErrors(t *testing.T) {
	root := t.TempDir()
	farmDir := filepath.Join(root, "lib")
	storeA := storeLayout(t, root, "aaa", "1.0",
		[]string{versionedName("libshared", "1")})
	storeB := storeLayout(t, root, "bbb", "1.0",
		[]string{versionedName("libshared", "1")})

	if err := Populate(storeA, farmDir); err != nil {
		t.Fatal(err)
	}
	if err := Populate(storeB, farmDir); err == nil {
		t.Fatal("expected conflict error, got nil")
	}
}

func TestDepopulateRemovesOnlyMatchingPackage(t *testing.T) {
	root := t.TempDir()
	farmDir := filepath.Join(root, "lib")
	storeA := storeLayout(t, root, "aaa", "1.0",
		[]string{versionedName("libaaa", "1")})
	storeB := storeLayout(t, root, "bbb", "1.0",
		[]string{versionedName("libbbb", "1")})
	if err := Populate(storeA, farmDir); err != nil {
		t.Fatal(err)
	}
	if err := Populate(storeB, farmDir); err != nil {
		t.Fatal(err)
	}

	if err := Depopulate(storeA, farmDir); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Lstat(filepath.Join(
		farmDir, versionedName("libaaa", "1"))); err == nil {
		t.Errorf("aaa symlink should have been removed")
	}
	if _, err := os.Lstat(filepath.Join(
		farmDir, versionedName("libbbb", "1"))); err != nil {
		t.Errorf("bbb symlink should still exist: %v", err)
	}
}

func TestRebuildFromActiveSet(t *testing.T) {
	root := t.TempDir()
	farmDir := filepath.Join(root, "lib")
	storeAaa := storeLayout(t, root, "aaa", "1.0",
		[]string{versionedName("libaaa", "1")})
	storeBbb := storeLayout(t, root, "bbb", "2.0",
		[]string{versionedName("libbbb", "2")})
	// Older revision of aaa on disk but NOT in the active
	// set. Rebuild must not farm it — otherwise it would
	// print a spurious "replacing" line every gen swap.
	storeLayout(t, root, "aaa", "0.9",
		[]string{versionedName("libaaa", "1")})

	// Also create a stale entry in the farm that's not
	// in the active set; Rebuild should clear it.
	if err := os.MkdirAll(farmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(farmDir, "libstale.1.dylib")
	if err := os.Symlink("/nowhere", stale); err != nil {
		t.Fatal(err)
	}

	// Capture stderr to confirm Rebuild does not emit any
	// "replacing" lines when the active set has no
	// intra-package duplicates.
	captured := captureStderr(t, func() {
		if err := Rebuild(
			[]string{storeAaa, storeBbb}, farmDir); err != nil {
			t.Fatal(err)
		}
	})

	if _, err := os.Lstat(stale); err == nil {
		t.Errorf("stale entry should have been cleared")
	}
	if _, err := os.Lstat(filepath.Join(
		farmDir, versionedName("libaaa", "1"))); err != nil {
		t.Errorf("aaa symlink missing: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(
		farmDir, versionedName("libbbb", "2"))); err != nil {
		t.Errorf("bbb symlink missing: %v", err)
	}
	// The farm's libaaa entry must point at the active
	// revision (1.0), not the older 0.9 still on disk.
	got, err := os.Readlink(filepath.Join(
		farmDir, versionedName("libaaa", "1")))
	if err != nil {
		t.Fatal(err)
	}
	wantTarget := filepath.Join(
		storeAaa, "lib", versionedName("libaaa", "1"))
	if got != wantTarget {
		t.Errorf("aaa target = %q, want %q", got, wantTarget)
	}

	if strings.Contains(captured, "replacing") {
		t.Errorf("unexpected 'replacing' output: %q",
			captured)
	}
}

func TestCheckDriftReportsMissingAndBroken(t *testing.T) {
	root := t.TempDir()
	farmDir := filepath.Join(root, "lib")
	// Install two pkgs, only populate the farm with one —
	// the other's versioned dylib shows up as "missing".
	storeA := storeLayout(t, root, "aaa", "1.0",
		[]string{versionedName("libaaa", "1")})
	storeLayout(t, root, "bbb", "1.0",
		[]string{versionedName("libbbb", "1")})
	if err := Populate(storeA, farmDir); err != nil {
		t.Fatal(err)
	}
	// Add a broken symlink to simulate drift.
	broken := filepath.Join(farmDir, "libghost.1.dylib")
	if err := os.Symlink("/nowhere", broken); err != nil {
		t.Fatal(err)
	}

	issues, err := CheckDrift(filepath.Join(root, "pkg"), farmDir)
	if err != nil {
		t.Fatal(err)
	}

	var sawBroken, sawMissing bool
	for _, iss := range issues {
		if strings.Contains(iss, "broken symlink") &&
			strings.Contains(iss, "libghost") {
			sawBroken = true
		}
		if strings.Contains(iss, "missing farm entry") &&
			strings.Contains(iss, "bbb") {
			sawMissing = true
		}
	}
	if !sawBroken {
		t.Errorf("missing broken-symlink issue: %v", issues)
	}
	if !sawMissing {
		t.Errorf("missing missing-entry issue: %v", issues)
	}
}

func TestCheckDriftCleanFarmReportsNoIssues(t *testing.T) {
	root := t.TempDir()
	farmDir := filepath.Join(root, "lib")
	storeA := storeLayout(t, root, "aaa", "1.0",
		[]string{versionedName("libaaa", "1")})
	if err := Populate(storeA, farmDir); err != nil {
		t.Fatal(err)
	}
	issues, err := CheckDrift(filepath.Join(root, "pkg"), farmDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Errorf("expected no issues, got %v", issues)
	}
}

// captureStderr redirects os.Stderr across fn and returns
// whatever was written.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	fn()

	os.Stderr = orig
	w.Close()
	return <-done
}

// versionedName builds a basename in the OS's versioned
// dylib convention so tests work cross-platform.
func versionedName(stem, ver string) string {
	if runtime.GOOS == "linux" {
		return stem + ".so." + ver
	}
	return stem + "." + ver + ".dylib"
}

// aliasName builds the unversioned alias basename.
func aliasName(stem string) string {
	if runtime.GOOS == "linux" {
		return stem + ".so"
	}
	return stem + ".dylib"
}
