package lockfile

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- Behavior 1: Read lock file ---

const validLockTOML = `[packages]
jq = "1.7.1"
ripgrep = "14.1.0"
`

func TestReadReturnsNonNilLockFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.lock")
	if err := os.WriteFile(path, []byte(validLockTOML), 0o644); err != nil {
		t.Fatalf("failed to write lock file: %v", err)
	}

	lf, err := Read(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lf == nil {
		t.Fatal("expected non-nil LockFile")
	}
}

func TestReadParsesPackageCount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.lock")
	if err := os.WriteFile(path, []byte(validLockTOML), 0o644); err != nil {
		t.Fatalf("failed to write lock file: %v", err)
	}

	lf, err := Read(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lf.Packages) != 2 {
		t.Errorf("Packages length = %d, want 2", len(lf.Packages))
	}
}

func TestReadParsesJqVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.lock")
	if err := os.WriteFile(path, []byte(validLockTOML), 0o644); err != nil {
		t.Fatalf("failed to write lock file: %v", err)
	}

	lf, err := Read(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lf.Packages["jq"] != "1.7.1" {
		t.Errorf("Packages[jq] = %q, want %q",
			lf.Packages["jq"], "1.7.1")
	}
}

func TestReadParsesRipgrepVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.lock")
	if err := os.WriteFile(path, []byte(validLockTOML), 0o644); err != nil {
		t.Fatalf("failed to write lock file: %v", err)
	}

	lf, err := Read(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lf.Packages["ripgrep"] != "14.1.0" {
		t.Errorf("Packages[ripgrep] = %q, want %q",
			lf.Packages["ripgrep"], "14.1.0")
	}
}

func TestReadMissingFileReturnsEmptyLockFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.lock")

	lf, err := Read(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lf == nil {
		t.Fatal("expected non-nil LockFile for missing file")
	}
	if len(lf.Packages) != 0 {
		t.Errorf("Packages length = %d, want 0", len(lf.Packages))
	}
}

func TestReadMissingFileReturnsNoError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.lock")

	_, err := Read(path)
	if err != nil {
		t.Errorf("expected no error for missing file, got: %v", err)
	}
}

func TestReadMalformedTOMLReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.lock")
	if err := os.WriteFile(path, []byte("this is not [valid toml"), 0o644); err != nil {
		t.Fatalf("failed to write lock file: %v", err)
	}

	_, err := Read(path)
	if err == nil {
		t.Fatal("expected error for malformed TOML")
	}
}

// --- Behavior 2: Write lock file ---

func TestWriteCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.lock")

	lf := &LockFile{
		Packages: map[string]string{
			"jq":      "1.7.1",
			"ripgrep": "14.1.0",
		},
	}

	if err := Write(path, lf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file to exist: %v", err)
	}
}

func TestWriteProducesNonEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.lock")

	lf := &LockFile{
		Packages: map[string]string{"jq": "1.7.1"},
	}

	if err := Write(path, lf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("written file is empty")
	}
}

func TestWriteRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.lock")

	original := &LockFile{
		Packages: map[string]string{
			"jq":      "1.7.1",
			"ripgrep": "14.1.0",
		},
	}

	if err := Write(path, original); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	parsed, err := Read(path)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if parsed.Packages["jq"] != "1.7.1" {
		t.Errorf("round-trip Packages[jq] = %q, want %q",
			parsed.Packages["jq"], "1.7.1")
	}
	if parsed.Packages["ripgrep"] != "14.1.0" {
		t.Errorf("round-trip Packages[ripgrep] = %q, want %q",
			parsed.Packages["ripgrep"], "14.1.0")
	}
}

func TestWriteRoundTripsPackageCount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.lock")

	original := &LockFile{
		Packages: map[string]string{
			"jq":      "1.7.1",
			"ripgrep": "14.1.0",
		},
	}

	if err := Write(path, original); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	parsed, err := Read(path)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if len(parsed.Packages) != 2 {
		t.Errorf("round-trip Packages length = %d, want 2",
			len(parsed.Packages))
	}
}

func TestWriteEmptyPackages(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.lock")

	lf := &LockFile{
		Packages: map[string]string{},
	}

	if err := Write(path, lf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parsed, err := Read(path)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if len(parsed.Packages) != 0 {
		t.Errorf("Packages length = %d, want 0",
			len(parsed.Packages))
	}
}

func TestWriteOverwritesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.lock")

	first := &LockFile{
		Packages: map[string]string{"jq": "1.7.1"},
	}
	if err := Write(path, first); err != nil {
		t.Fatalf("first Write error: %v", err)
	}

	second := &LockFile{
		Packages: map[string]string{"ripgrep": "14.1.0"},
	}
	if err := Write(path, second); err != nil {
		t.Fatalf("second Write error: %v", err)
	}

	parsed, err := Read(path)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if _, exists := parsed.Packages["jq"]; exists {
		t.Error("expected jq to be gone after overwrite")
	}
	if parsed.Packages["ripgrep"] != "14.1.0" {
		t.Errorf("Packages[ripgrep] = %q, want %q",
			parsed.Packages["ripgrep"], "14.1.0")
	}
}

// --- Behavior 3: Detect stale lock ---

func TestIsStaleReturnsFalseWhenInSync(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "gale.toml")
	lockPath := filepath.Join(dir, "gale.lock")

	pkgs := map[string]string{
		"jq":      "1.7.1",
		"ripgrep": "14.1.0",
	}

	if err := os.WriteFile(tomlPath, []byte("[packages]\njq = \"1.7.1\"\nripgrep = \"14.1.0\"\n"), 0o644); err != nil {
		t.Fatalf("failed to write gale.toml: %v", err)
	}

	lf := &LockFile{Packages: pkgs}
	if err := Write(lockPath, lf); err != nil {
		t.Fatalf("failed to write lock file: %v", err)
	}

	// Ensure lock file is newer than gale.toml.
	past := time.Now().Add(-10 * time.Second)
	if err := os.Chtimes(tomlPath, past, past); err != nil {
		t.Fatalf("failed to set gale.toml mtime: %v", err)
	}

	stale, err := IsStale(tomlPath, lockPath, pkgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stale {
		t.Error("IsStale = true, want false when in sync")
	}
}

func TestIsStaleReturnsTrueWhenTOMLIsNewer(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "gale.toml")
	lockPath := filepath.Join(dir, "gale.lock")

	pkgs := map[string]string{"jq": "1.7.1"}

	lf := &LockFile{Packages: pkgs}
	if err := Write(lockPath, lf); err != nil {
		t.Fatalf("failed to write lock file: %v", err)
	}

	// Set lock file to the past.
	past := time.Now().Add(-10 * time.Second)
	if err := os.Chtimes(lockPath, past, past); err != nil {
		t.Fatalf("failed to set lock mtime: %v", err)
	}

	// Write gale.toml after the lock so it's newer.
	if err := os.WriteFile(tomlPath, []byte("[packages]\njq = \"1.7.1\"\n"), 0o644); err != nil {
		t.Fatalf("failed to write gale.toml: %v", err)
	}

	stale, err := IsStale(tomlPath, lockPath, pkgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !stale {
		t.Error("IsStale = false, want true when gale.toml is newer")
	}
}

func TestIsStaleReturnsTrueWhenTOMLHasExtraPackage(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "gale.toml")
	lockPath := filepath.Join(dir, "gale.lock")

	tomlPkgs := map[string]string{
		"jq":      "1.7.1",
		"ripgrep": "14.1.0",
	}

	lockPkgs := map[string]string{
		"jq": "1.7.1",
	}

	if err := os.WriteFile(tomlPath, []byte("[packages]\njq = \"1.7.1\"\nripgrep = \"14.1.0\"\n"), 0o644); err != nil {
		t.Fatalf("failed to write gale.toml: %v", err)
	}

	lf := &LockFile{Packages: lockPkgs}
	if err := Write(lockPath, lf); err != nil {
		t.Fatalf("failed to write lock file: %v", err)
	}

	// Set gale.toml to the past so mtime is not the trigger.
	past := time.Now().Add(-10 * time.Second)
	if err := os.Chtimes(tomlPath, past, past); err != nil {
		t.Fatalf("failed to set gale.toml mtime: %v", err)
	}

	stale, err := IsStale(tomlPath, lockPath, tomlPkgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !stale {
		t.Error("IsStale = false, want true when toml has extra package")
	}
}

func TestIsStaleReturnsTrueWhenLockHasExtraPackage(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "gale.toml")
	lockPath := filepath.Join(dir, "gale.lock")

	tomlPkgs := map[string]string{
		"jq": "1.7.1",
	}

	lockPkgs := map[string]string{
		"jq":      "1.7.1",
		"ripgrep": "14.1.0",
	}

	if err := os.WriteFile(tomlPath, []byte("[packages]\njq = \"1.7.1\"\n"), 0o644); err != nil {
		t.Fatalf("failed to write gale.toml: %v", err)
	}

	lf := &LockFile{Packages: lockPkgs}
	if err := Write(lockPath, lf); err != nil {
		t.Fatalf("failed to write lock file: %v", err)
	}

	// Set gale.toml to the past so mtime is not the trigger.
	past := time.Now().Add(-10 * time.Second)
	if err := os.Chtimes(tomlPath, past, past); err != nil {
		t.Fatalf("failed to set gale.toml mtime: %v", err)
	}

	stale, err := IsStale(tomlPath, lockPath, tomlPkgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !stale {
		t.Error("IsStale = false, want true when lock has extra package")
	}
}

func TestIsStaleReturnsTrueWhenLockFileMissing(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "gale.toml")
	lockPath := filepath.Join(dir, "gale.lock")

	pkgs := map[string]string{"jq": "1.7.1"}

	if err := os.WriteFile(tomlPath, []byte("[packages]\njq = \"1.7.1\"\n"), 0o644); err != nil {
		t.Fatalf("failed to write gale.toml: %v", err)
	}

	stale, err := IsStale(tomlPath, lockPath, pkgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !stale {
		t.Error("IsStale = false, want true when lock file is missing")
	}
}

func TestIsStaleReturnsNoErrorWhenLockFileMissing(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "gale.toml")
	lockPath := filepath.Join(dir, "gale.lock")

	pkgs := map[string]string{"jq": "1.7.1"}

	if err := os.WriteFile(tomlPath, []byte("[packages]\njq = \"1.7.1\"\n"), 0o644); err != nil {
		t.Fatalf("failed to write gale.toml: %v", err)
	}

	_, err := IsStale(tomlPath, lockPath, pkgs)
	if err != nil {
		t.Errorf("expected no error for missing lock file, got: %v",
			err)
	}
}

func TestIsStaleReturnsTrueWhenVersionsDiffer(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "gale.toml")
	lockPath := filepath.Join(dir, "gale.lock")

	tomlPkgs := map[string]string{"jq": "1.8.0"}
	lockPkgs := map[string]string{"jq": "1.7.1"}

	if err := os.WriteFile(tomlPath, []byte("[packages]\njq = \"1.8.0\"\n"), 0o644); err != nil {
		t.Fatalf("failed to write gale.toml: %v", err)
	}

	lf := &LockFile{Packages: lockPkgs}
	if err := Write(lockPath, lf); err != nil {
		t.Fatalf("failed to write lock file: %v", err)
	}

	// Set gale.toml to the past so mtime is not the trigger.
	past := time.Now().Add(-10 * time.Second)
	if err := os.Chtimes(tomlPath, past, past); err != nil {
		t.Fatalf("failed to set gale.toml mtime: %v", err)
	}

	stale, err := IsStale(tomlPath, lockPath, tomlPkgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !stale {
		t.Error("IsStale = false, want true when versions differ")
	}
}
