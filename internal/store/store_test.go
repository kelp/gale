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
