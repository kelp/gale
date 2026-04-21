package store

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// --- Behavior 1: Create store directory ---

func TestCreateReturnsVersionDirPath(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	got, err := s.Create("jq", "1.7.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := filepath.Join(root, "jq", "1.7.1")
	if got != want {
		t.Errorf("Create path = %q, want %q", got, want)
	}
}

func TestCreateMakesDirectoryOnDisk(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	path, err := s.Create("jq", "1.7.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("directory does not exist: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("expected %q to be a directory", path)
	}
}

func TestCreateIdempotent(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	_, err := s.Create("jq", "1.7.1")
	if err != nil {
		t.Fatalf("first Create: unexpected error: %v", err)
	}

	_, err = s.Create("jq", "1.7.1")
	if err != nil {
		t.Fatalf("second Create: unexpected error: %v", err)
	}
}

// --- Behavior 2: Check if package is installed ---

func TestIsInstalledReturnsTrueWhenExists(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	dir := filepath.Join(root, "jq", "1.7.1")
	if err := os.MkdirAll(
		filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}

	if !s.IsInstalled("jq", "1.7.1") {
		t.Error("IsInstalled = false, want true")
	}
}

func TestIsInstalledReturnsFalseWhenMissing(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	if s.IsInstalled("jq", "1.7.1") {
		t.Error("IsInstalled = true, want false")
	}
}

func TestIsInstalledReturnsFalseForDifferentVersion(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	dir := filepath.Join(root, "jq", "1.7.1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}

	if s.IsInstalled("jq", "2.0.0") {
		t.Error("IsInstalled = true for wrong version, want false")
	}
}

// --- Behavior 3: List installed packages ---

func TestListNonExistentRoot(t *testing.T) {
	s := NewStore("/nonexistent/path/that/does/not/exist")

	pkgs, err := s.List()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pkgs) != 0 {
		t.Errorf("List length = %d, want 0", len(pkgs))
	}
}

func TestListEmptyStore(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	pkgs, err := s.List()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pkgs) != 0 {
		t.Errorf("List length = %d, want 0", len(pkgs))
	}
}

func TestListSinglePackage(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	dir := filepath.Join(root, "jq", "1.7.1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}

	pkgs, err := s.List()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("List length = %d, want 1", len(pkgs))
	}
	if pkgs[0].Name != "jq" {
		t.Errorf("Name = %q, want %q", pkgs[0].Name, "jq")
	}
	if pkgs[0].Version != "1.7.1" {
		t.Errorf("Version = %q, want %q", pkgs[0].Version, "1.7.1")
	}
}

func TestListMultiplePackages(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	dirs := []string{
		filepath.Join(root, "jq", "1.7.1"),
		filepath.Join(root, "ripgrep", "14.0.0"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("failed to create directory: %v", err)
		}
	}

	pkgs, err := s.List()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pkgs) != 2 {
		t.Fatalf("List length = %d, want 2", len(pkgs))
	}

	sort.Slice(pkgs, func(i, j int) bool {
		return pkgs[i].Name < pkgs[j].Name
	})
	if pkgs[0].Name != "jq" {
		t.Errorf("pkgs[0].Name = %q, want %q", pkgs[0].Name, "jq")
	}
	if pkgs[1].Name != "ripgrep" {
		t.Errorf("pkgs[1].Name = %q, want %q",
			pkgs[1].Name, "ripgrep")
	}
}

func TestListMultipleVersions(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	dirs := []string{
		filepath.Join(root, "jq", "1.7.1"),
		filepath.Join(root, "jq", "1.8.0"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("failed to create directory: %v", err)
		}
	}

	pkgs, err := s.List()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pkgs) != 2 {
		t.Fatalf("List length = %d, want 2", len(pkgs))
	}

	sort.Slice(pkgs, func(i, j int) bool {
		return pkgs[i].Version < pkgs[j].Version
	})
	if pkgs[0].Version != "1.7.1" {
		t.Errorf("pkgs[0].Version = %q, want %q",
			pkgs[0].Version, "1.7.1")
	}
	if pkgs[1].Version != "1.8.0" {
		t.Errorf("pkgs[1].Version = %q, want %q",
			pkgs[1].Version, "1.8.0")
	}
	if pkgs[0].Name != "jq" {
		t.Errorf("pkgs[0].Name = %q, want %q",
			pkgs[0].Name, "jq")
	}
	if pkgs[1].Name != "jq" {
		t.Errorf("pkgs[1].Name = %q, want %q",
			pkgs[1].Name, "jq")
	}
}

// --- Behavior 4: Remove package version ---

func TestRemoveDeletesVersionDirectory(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	dir := filepath.Join(root, "jq", "1.7.1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}

	if err := s.Remove("jq", "1.7.1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("expected version directory to be removed")
	}
}

func TestRemoveReturnsErrorWhenNotInstalled(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	err := s.Remove("jq", "1.7.1")
	if err == nil {
		t.Fatal("expected error when removing nonexistent package")
	}
	if !errors.Is(err, ErrNotInstalled) {
		t.Errorf("error = %v, want ErrNotInstalled", err)
	}
}

func TestRemoveCleansUpEmptyParentDirectory(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	dir := filepath.Join(root, "jq", "1.7.1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}

	if err := s.Remove("jq", "1.7.1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	nameDir := filepath.Join(root, "jq")
	if _, err := os.Stat(nameDir); !os.IsNotExist(err) {
		t.Errorf("expected empty parent directory %q to be removed",
			nameDir)
	}
}

func TestRemoveKeepsParentWhenOtherVersionsExist(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	dirs := []string{
		filepath.Join(root, "jq", "1.7.1"),
		filepath.Join(root, "jq", "1.8.0"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("failed to create directory: %v", err)
		}
	}

	if err := s.Remove("jq", "1.7.1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	nameDir := filepath.Join(root, "jq")
	info, err := os.Stat(nameDir)
	if err != nil {
		t.Fatalf("parent directory should still exist: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("expected %q to be a directory", nameDir)
	}

	otherDir := filepath.Join(root, "jq", "1.8.0")
	if _, err := os.Stat(otherDir); err != nil {
		t.Errorf("other version directory should still exist: %v", err)
	}
}

// --- Behavior N: Back-compat fallback for revision-1 suffix ---

// TestIsInstalledExactMatch verifies that a revision-suffixed directory
// is found directly without needing any fallback.
func TestIsInstalledExactMatch(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	dir := filepath.Join(root, "curl", "8.19.0-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "foo"), []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if !s.IsInstalled("curl", "8.19.0-1") {
		t.Error("IsInstalled = false, want true for exact-match revision dir")
	}
}

// TestIsInstalledFallsBackToBareVersionForRevisionOne verifies that when
// a caller passes "<version>-1" but only a bare-version directory exists
// (legacy install), IsInstalled returns true via the fallback path.
func TestIsInstalledFallsBackToBareVersionForRevisionOne(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	// Only the bare (legacy) directory exists.
	dir := filepath.Join(root, "curl", "8.19.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "foo"), []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if !s.IsInstalled("curl", "8.19.0-1") {
		t.Error("IsInstalled = false, want true — should fall back to bare version dir")
	}
}

// TestIsInstalledDoesNotFallBackForRevisionTwo verifies that when the
// caller passes "<version>-2", no fallback to a bare directory occurs;
// only revision 1 triggers the fallback.
func TestIsInstalledDoesNotFallBackForRevisionTwo(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	// Only the bare (legacy) directory exists; no "-2" dir.
	dir := filepath.Join(root, "curl", "8.19.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "foo"), []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if s.IsInstalled("curl", "8.19.0-2") {
		t.Error("IsInstalled = true, want false — revision 2 must not fall back to bare dir")
	}
}

// TestRemoveFindsBareVersionWhenPassedRevisionOne verifies that Remove
// with "<version>-1" succeeds and deletes the bare-version directory
// when no revision-suffixed directory exists (legacy install).
func TestRemoveFindsBareVersionWhenPassedRevisionOne(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	bare := filepath.Join(root, "curl", "8.19.0")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(bare, "foo"), []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := s.Remove("curl", "8.19.0-1"); err != nil {
		t.Fatalf("Remove returned unexpected error: %v", err)
	}

	if _, err := os.Stat(bare); !os.IsNotExist(err) {
		t.Error("expected bare version directory to be removed after Remove with -1 suffix")
	}
}

// TestRemoveReturnsErrNotInstalledForMissingRevisionTwo verifies that
// Remove returns ErrNotInstalled when only a bare directory exists but
// the caller requests revision 2 — which has no fallback.
func TestRemoveReturnsErrNotInstalledForMissingRevisionTwo(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	// Only bare directory; no "-2" dir.
	bare := filepath.Join(root, "curl", "8.19.0")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(bare, "foo"), []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	err := s.Remove("curl", "8.19.0-2")
	if err == nil {
		t.Fatal("expected error when removing revision 2 that does not exist")
	}
	if !errors.Is(err, ErrNotInstalled) {
		t.Errorf("error = %v, want ErrNotInstalled", err)
	}
}

// TestRemovePrefersExactDirWhenBothExist verifies that when both a
// revision-suffixed directory and a bare-version directory exist, Remove
// with the revision-suffixed version removes exactly that directory and
// leaves the bare directory untouched.
func TestRemovePrefersExactDirWhenBothExist(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	for _, sub := range []string{"8.19.0", "8.19.0-1"} {
		dir := filepath.Join(root, "curl", sub)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("setup %s: %v", sub, err)
		}
		if err := os.WriteFile(
			filepath.Join(dir, "foo"), []byte("x"), 0o644); err != nil {
			t.Fatalf("setup %s: %v", sub, err)
		}
	}

	if err := s.Remove("curl", "8.19.0-1"); err != nil {
		t.Fatalf("Remove returned unexpected error: %v", err)
	}

	exactDir := filepath.Join(root, "curl", "8.19.0-1")
	if _, err := os.Stat(exactDir); !os.IsNotExist(err) {
		t.Error("expected exact revision directory 8.19.0-1 to be removed")
	}

	bareDir := filepath.Join(root, "curl", "8.19.0")
	if _, err := os.Stat(bareDir); err != nil {
		t.Errorf("bare version directory 8.19.0 should still exist: %v", err)
	}
}

// --- Behavior 5: Empty directories not considered installed ---

func TestIsInstalledEmptyDirReturnsFalse(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	// Create an empty directory (simulates a failed install).
	dir := filepath.Join(root, "jq", "1.7")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if s.IsInstalled("jq", "1.7") {
		t.Error("IsInstalled returned true for empty directory")
	}
}

// --- Behavior 6: Failed install allows retry ---

func TestCreateThenFailedInstallAllowsRetry(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	// Simulate: Create succeeds, but install fails before
	// writing any content.
	dir, err := s.Create("jq", "1.7")
	if err != nil {
		t.Fatal(err)
	}
	_ = dir

	// The empty directory should not be considered installed.
	if s.IsInstalled("jq", "1.7") {
		t.Error("empty store dir should not be considered installed")
	}

	// A retry should be able to Create again (idempotent).
	dir2, err := s.Create("jq", "1.7")
	if err != nil {
		t.Fatalf("retry Create failed: %v", err)
	}

	// Write content to simulate a successful install.
	binDir := filepath.Join(dir2, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(binDir, "jq"),
		[]byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Now it should be considered installed.
	if !s.IsInstalled("jq", "1.7") {
		t.Error("store dir with content should be installed")
	}
}

// --- Behavior N+1: Bare version resolves to highest revision ---

// TestIsInstalledFindsHigherRevisionForBareVersion verifies that when
// config carries a bare version ("0.26.1") but the store only holds a
// higher-revision dir ("0.26.1-2"), IsInstalled still returns true.
// This matches the documented semantic in gale/CLAUDE.md: a bare
// @version resolves to the highest revision known.
func TestIsInstalledFindsHigherRevisionForBareVersion(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	// Only revision 2 exists — no "-1" canonical dir, no bare dir.
	dir := filepath.Join(root, "bat", "0.26.1-2")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "foo"), []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if !s.IsInstalled("bat", "0.26.1") {
		t.Error("IsInstalled = false, want true — bare version should " +
			"resolve to higher-revision dir when no -1/bare exists")
	}
}

// TestStorePathReturnsHighestRevisionForBareVersion verifies that when
// multiple revisions are on disk and no -1/bare exists, StorePath
// returns the highest-numbered revision dir.
func TestStorePathReturnsHighestRevisionForBareVersion(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	for _, rev := range []string{"1.8.1-2", "1.8.1-3", "1.8.1-10"} {
		dir := filepath.Join(root, "jq", rev)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("setup %s: %v", rev, err)
		}
	}

	got, ok := s.StorePath("jq", "1.8.1")
	if !ok {
		t.Fatalf("StorePath ok = false, want true")
	}
	want := filepath.Join(root, "jq", "1.8.1-10")
	if got != want {
		t.Errorf("StorePath = %q, want %q (highest rev)", got, want)
	}
}

// TestResolveVersionReturnsHighestRevisionWhenCanonicalAlsoExists
// verifies that a bare-version lookup picks the highest revision
// even when the canonical "-1" dir is also present. This is the
// CLAUDE.md semantic: "a bare @version resolves to the highest
// revision known." Without this, a recipe revision bump leaves
// users stuck on the old canonical dir forever — sync keeps
// installing the new revision but the active gen never migrates.
func TestResolveVersionReturnsHighestRevisionWhenCanonicalAlsoExists(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	for _, rev := range []string{"1.8.1-1", "1.8.1-3"} {
		dir := filepath.Join(root, "jq", rev)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("setup %s: %v", rev, err)
		}
	}

	got, ok := s.StorePath("jq", "1.8.1")
	if !ok {
		t.Fatalf("StorePath ok = false, want true")
	}
	want := filepath.Join(root, "jq", "1.8.1-3")
	if got != want {
		t.Errorf("StorePath = %q, want %q (highest wins)", got, want)
	}
}

// TestRemoveBareVersionRemovesBareDirNotHighestRevision pins
// the regression where Remove("go", "1.26.1") would resolve
// the bare version to the highest-revision dir and delete
// that instead of the bare dir. Callers that pass an on-disk
// name (from store.List) must get that exact dir removed,
// even when the lookup-resolve rule says "bare → highest".
func TestRemoveBareVersionRemovesBareDirNotHighestRevision(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	for _, sub := range []string{"1.26.1", "1.26.1-2"} {
		dir := filepath.Join(root, "go", sub)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("setup %s: %v", sub, err)
		}
		if err := os.WriteFile(
			filepath.Join(dir, "marker"),
			[]byte("x"), 0o644); err != nil {
			t.Fatalf("setup %s: %v", sub, err)
		}
	}

	if err := s.Remove("go", "1.26.1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	bare := filepath.Join(root, "go", "1.26.1")
	if _, err := os.Stat(bare); !os.IsNotExist(err) {
		t.Error("bare go/1.26.1 should have been removed")
	}
	rev := filepath.Join(root, "go", "1.26.1-2")
	if _, err := os.Stat(rev); err != nil {
		t.Errorf("go/1.26.1-2 must survive — Remove should "+
			"prefer exact match over lookup fallback: %v", err)
	}
}

// TestStorePathIgnoresBuildTempAndLockSiblings verifies that the
// highest-revision scan skips .build-* staging dirs and *.lock
// sibling files when picking a fallback.
func TestStorePathIgnoresBuildTempAndLockSiblings(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	// Real revision dir.
	if err := os.MkdirAll(
		filepath.Join(root, "foo", "1.0.0-2"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Noise that must be ignored.
	if err := os.MkdirAll(
		filepath.Join(root, "foo", ".build-tmp-1.0.0-9"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "foo", "1.0.0-2.lock"),
		[]byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "foo", "1.0.0-99.lock"),
		[]byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := s.StorePath("foo", "1.0.0")
	if !ok {
		t.Fatalf("StorePath ok = false, want true")
	}
	want := filepath.Join(root, "foo", "1.0.0-2")
	if got != want {
		t.Errorf("StorePath = %q, want %q", got, want)
	}
}
