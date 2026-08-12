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
		// darwin: non-numeric dotted stems must match — the
		// stem carries a dotted, non-numeric segment before
		// the final numeric version tail (gh#168).
		{"darwin", "libMagick++-7.Q16HDRI.5.dylib", true},
		{"darwin", "libFoo-1.Bar.2.dylib", true},
		// darwin: numeric dotted stems keep matching.
		{"darwin", "libpython3.14.dylib", true},
		// darwin: the version tail stays required — a dotted
		// non-numeric segment with no trailing numeric tail
		// must NOT match, or the farm would swallow
		// unversioned libs (gh#168 over-match guard).
		{"darwin", "libMagick++-7.Q16HDRI.dylib", false},
		{"darwin", "libbar.dylib", false},
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

// populateAndReadFarmLink populates the farm from storeDir
// and returns the resolved target of the farm symlink for
// dylib. Fails the test if the symlink is absent.
func populateAndReadFarmLink(
	t *testing.T, storeDir, farmDir, dylib string,
) string {
	t.Helper()
	if err := Populate(storeDir, farmDir); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(farmDir, dylib))
	if err != nil {
		t.Fatalf("expected farm symlink for %s: %v", dylib, err)
	}
	return target
}

// TestPopulateNonNumericDottedStem confirms the gh#168 fix end
// to end: a dylib whose stem carries a non-numeric dotted
// segment (libMagick++-7.Q16HDRI.5.dylib) lands in the farm,
// not just that the regex matches.
func TestPopulateNonNumericDottedStem(t *testing.T) {
	root := t.TempDir()
	farmDir := filepath.Join(root, "lib")
	stem := "libMagick++-7.Q16HDRI"
	dylib := versionedName(stem, "5")
	storeDir := storeLayout(t, root, "imagemagick", "7.1.1",
		[]string{dylib, aliasName(stem)})

	target := populateAndReadFarmLink(t, storeDir, farmDir, dylib)
	if want := filepath.Join(storeDir, "lib", dylib); target != want {
		t.Errorf("target = %q, want %q", target, want)
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
		storeDir, "lib", versionedName("libcurl", "4"),
	)
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

// TestPopulateFarmsVersionedSymlinkAliases covers the libtool
// install layout: the real file is libusb-1.0.so.0.6.0 and the
// soname libusb-1.0.so.0 is a symlink to it. ELF DT_NEEDED (and
// Mach-O install names) reference the soname, so the farm must
// hold the versioned alias too — otherwise ld.so on a machine
// without a distro copy of the lib fails to resolve it even
// though the dependent's RUNPATH points at the farm (openocd on
// Linux was the first dynamic dep-linker to hit this).
func TestPopulateFarmsVersionedSymlinkAliases(t *testing.T) {
	root := t.TempDir()
	farmDir := filepath.Join(root, "lib")
	realFile := versionedName("libusb", "0.6.0")
	soname := versionedName("libusb", "0")
	storeDir := storeLayout(t, root, "libusb", "1.0.30-1",
		[]string{realFile})
	libDir := filepath.Join(storeDir, "lib")
	if err := os.Symlink(
		realFile, filepath.Join(libDir, soname),
	); err != nil {
		t.Fatal(err)
	}
	// Unversioned alias symlink must stay un-farmed.
	if err := os.Symlink(
		realFile, filepath.Join(libDir, aliasName("libusb")),
	); err != nil {
		t.Fatal(err)
	}

	if err := Populate(storeDir, farmDir); err != nil {
		t.Fatal(err)
	}

	target, err := os.Readlink(filepath.Join(farmDir, soname))
	if err != nil {
		t.Fatalf("soname alias %s not farmed: %v", soname, err)
	}
	if want := filepath.Join(libDir, soname); target != want {
		t.Errorf("soname target = %q, want %q", target, want)
	}
	if _, err := os.Readlink(
		filepath.Join(farmDir, realFile),
	); err != nil {
		t.Errorf("real file %s should still be farmed: %v",
			realFile, err)
	}
	if _, err := os.Lstat(filepath.Join(
		farmDir, aliasName("libusb"),
	)); err == nil {
		t.Errorf("unversioned alias should not be farmed")
	}
}

// TestPopulateSkipsDanglingVersionedSymlink: a versioned symlink
// whose target is gone must not be farmed — a farm entry chain
// ending nowhere would surface as a broken lib at runtime.
func TestPopulateSkipsDanglingVersionedSymlink(t *testing.T) {
	root := t.TempDir()
	farmDir := filepath.Join(root, "lib")
	storeDir := storeLayout(t, root, "foo", "1.0", nil)
	libDir := filepath.Join(storeDir, "lib")
	dangling := versionedName("libfoo", "1")
	if err := os.Symlink(
		"libfoo-gone", filepath.Join(libDir, dangling),
	); err != nil {
		t.Fatal(err)
	}

	if err := Populate(storeDir, farmDir); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Lstat(
		filepath.Join(farmDir, dangling),
	); err == nil {
		t.Errorf("dangling symlink %s should not be farmed",
			dangling)
	}
}

// TestCheckDriftReportsMissingSonameAlias keeps CheckDrift
// symmetric with Populate: a farm missing the versioned soname
// alias (e.g. built by an older gale) must be flagged so
// `gale doctor --repair` rebuilds it.
func TestCheckDriftReportsMissingSonameAlias(t *testing.T) {
	root := t.TempDir()
	farmDir := filepath.Join(root, "lib")
	realFile := versionedName("libusb", "0.6.0")
	soname := versionedName("libusb", "0")
	storeDir := storeLayout(t, root, "libusb", "1.0.30-1",
		[]string{realFile})
	libDir := filepath.Join(storeDir, "lib")
	if err := os.Symlink(
		realFile, filepath.Join(libDir, soname),
	); err != nil {
		t.Fatal(err)
	}
	// Simulate an old-gale farm: only the real file linked.
	if err := os.MkdirAll(farmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join(libDir, realFile),
		filepath.Join(farmDir, realFile),
	); err != nil {
		t.Fatal(err)
	}

	issues, err := CheckDrift([]string{storeDir}, farmDir)
	if err != nil {
		t.Fatal(err)
	}
	var sawMissing bool
	for _, iss := range issues {
		if strings.Contains(iss, "missing farm entry") &&
			strings.Contains(iss, soname) {
			sawMissing = true
		}
	}
	if !sawMissing {
		t.Errorf("expected missing-entry issue for %s, got %v",
			soname, issues)
	}

	// After a Populate (what --repair does via Rebuild), the
	// drift must clear.
	if err := Populate(storeDir, farmDir); err != nil {
		t.Fatal(err)
	}
	issues, err = CheckDrift([]string{storeDir}, farmDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Errorf("expected no drift after repopulate, got %v",
			issues)
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
	captured := captureStderr(t, func() {
		if err := Populate(storeNew, farmDir); err != nil {
			t.Fatalf("newer same-package install should succeed: %v",
				err)
		}
	})
	wantSummary := "farm: updated curl@8.19.0 (1 dylib)"
	if !strings.Contains(captured, wantSummary) {
		t.Errorf("stderr = %q, want substring %q",
			captured, wantSummary)
	}
	if strings.Contains(captured, "replacing") {
		t.Errorf("unexpected per-dylib output: %q", captured)
	}

	got, err := os.Readlink(filepath.Join(
		farmDir, versionedName("libcurl", "4"),
	))
	if err != nil {
		t.Fatal(err)
	}
	wantTarget := filepath.Join(
		storeNew, "lib", versionedName("libcurl", "4"),
	)
	if got != wantTarget {
		t.Errorf("target = %q, want %q (newer should win)",
			got, wantTarget)
	}
}

func TestPopulateReplaceSummaryCollapsesMultipleDylibs(t *testing.T) {
	root := t.TempDir()
	farmDir := filepath.Join(root, "lib")
	storeOld := storeLayout(t, root, "pgsql", "16.0-1",
		[]string{
			versionedName("libpq", "5"),
			versionedName("libecpg", "6"),
			versionedName("libpgtypes", "3"),
		})
	storeNew := storeLayout(t, root, "pgsql", "16.0-2",
		[]string{
			versionedName("libpq", "5"),
			versionedName("libecpg", "6"),
			versionedName("libpgtypes", "3"),
		})

	if err := Populate(storeOld, farmDir); err != nil {
		t.Fatal(err)
	}
	captured := captureStderr(t, func() {
		if err := Populate(storeNew, farmDir); err != nil {
			t.Fatal(err)
		}
	})
	wantSummary := "farm: updated pgsql@16.0-2 (3 dylibs)"
	if !strings.Contains(captured, wantSummary) {
		t.Errorf("stderr = %q, want substring %q",
			captured, wantSummary)
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
		farmDir, versionedName("libaaa", "1"),
	)); err == nil {
		t.Errorf("aaa symlink should have been removed")
	}
	if _, err := os.Lstat(filepath.Join(
		farmDir, versionedName("libbbb", "1"),
	)); err != nil {
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
			[]string{storeAaa, storeBbb}, farmDir,
		); err != nil {
			t.Fatal(err)
		}
	})

	if _, err := os.Lstat(stale); err == nil {
		t.Errorf("stale entry should have been cleared")
	}
	if _, err := os.Lstat(filepath.Join(
		farmDir, versionedName("libaaa", "1"),
	)); err != nil {
		t.Errorf("aaa symlink missing: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(
		farmDir, versionedName("libbbb", "2"),
	)); err != nil {
		t.Errorf("bbb symlink missing: %v", err)
	}
	// The farm's libaaa entry must point at the active
	// revision (1.0), not the older 0.9 still on disk.
	got, err := os.Readlink(filepath.Join(
		farmDir, versionedName("libaaa", "1"),
	))
	if err != nil {
		t.Fatal(err)
	}
	wantTarget := filepath.Join(
		storeAaa, "lib", versionedName("libaaa", "1"),
	)
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
	storeB := storeLayout(t, root, "bbb", "1.0",
		[]string{versionedName("libbbb", "1")})
	if err := Populate(storeA, farmDir); err != nil {
		t.Fatal(err)
	}
	// Add a broken symlink to simulate drift.
	broken := filepath.Join(farmDir, "libghost.1.dylib")
	if err := os.Symlink("/nowhere", broken); err != nil {
		t.Fatal(err)
	}

	issues, err := CheckDrift(
		[]string{storeA, storeB}, farmDir,
	)
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
	issues, err := CheckDrift([]string{storeA}, farmDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Errorf("expected no issues, got %v", issues)
	}
}

// TestCheckDriftIgnoresInactiveRevisions verifies that old
// revisions of a package still on disk but not in the active
// generation are not flagged as "missing farm entry". This
// matches farm.Rebuild, which only populates the farm from
// active-gen store dirs. Without this scoping, CheckDrift
// flagged old revisions forever and `gale doctor --repair`
// could never clear the drift.
func TestCheckDriftIgnoresInactiveRevisions(t *testing.T) {
	root := t.TempDir()
	farmDir := filepath.Join(root, "lib")
	// Active revision — this goes in the active set.
	storeActive := storeLayout(t, root, "jq", "1.8.1-3",
		[]string{versionedName("libjq", "1")})
	// Inactive older revision on disk but NOT in active set.
	// Its dylib must NOT be flagged as "missing farm entry".
	storeLayout(t, root, "jq", "1.8.1-2",
		[]string{versionedName("libjq", "1")})
	if err := Populate(storeActive, farmDir); err != nil {
		t.Fatal(err)
	}

	issues, err := CheckDrift([]string{storeActive}, farmDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, iss := range issues {
		if strings.Contains(iss, "1.8.1-2") {
			t.Errorf("CheckDrift should ignore inactive "+
				"revision jq/1.8.1-2, got: %v", issues)
		}
	}
	if len(issues) != 0 {
		t.Errorf("expected no drift, got: %v", issues)
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

// TestPackageName pins the packageName extraction logic.
// The function must return "" when the grandparent dir is
// not "pkg" — a malformed symlink target (e.g. "." or "/")
// would otherwise produce a spurious farm-conflict error.
func TestPackageName(t *testing.T) {
	cases := []struct {
		storeDir string
		want     string
	}{
		// Happy path: <root>/pkg/<name>/<version>.
		{"/home/user/.gale/pkg/curl/8.0.0", "curl"},
		// Grandparent is not "pkg" — malformed target.
		{"/home/user/.gale/other/curl/8.0.0", ""},
		// Only one level deep.
		{"/pkg/curl", ""},
		// Root.
		{"/", ""},
		// Empty string.
		{"", ""},
	}
	for _, c := range cases {
		got := packageName(c.storeDir)
		if got != c.want {
			t.Errorf("packageName(%q) = %q, want %q",
				c.storeDir, got, c.want)
		}
	}
}

// TestRebuildReportsPopulationFailures pins design revision 6's
// weakened-but-not-silent rule for section 6.
//
// Rebuild used to log each Populate failure and return success. The
// generation rebuild is the caller, and an unpopulated shared farm
// means binaries cannot load their dylibs, while a line on stderr
// inside a direnv hook is invisible. So every store dir is still
// attempted and every failure is still collected and returned —
// which is what makes one run name every unreadable package rather
// than one per re-run.
//
// What that batch leaves BEHIND changed with gh#184. It used to
// publish the half that succeeded, and this test pinned that; the
// image is now staged and published only in full, so a failure
// leaves the farm as it was. TestRebuildPublishesNothingOnA
// PopulationFailure carries the inverted assertion.
func TestRebuildReportsPopulationFailures(t *testing.T) {
	root := t.TempDir()
	farmDir := filepath.Join(root, "lib")

	good := storeLayout(t, root, "good", "1.0",
		[]string{versionedName("libgood", "1")})
	bad := unreadableLibStoreDir(t, root)

	err := Rebuild([]string{good, bad}, farmDir)
	if err == nil {
		t.Fatal("Rebuild swallowed a population failure")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Errorf("error does not name the failing package: %v", err)
	}
}
