package lockfile

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- Behavior 1: Read lock file ---

const validLockTOML = `[packages.jq]
version = "1.7.1"
sha256 = "abc123"

[packages.ripgrep]
version = "14.1.0"
sha256 = "def456"
`

func TestReadParsesPackages(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.lock")
	if err := os.WriteFile(path,
		[]byte(validLockTOML), 0o644); err != nil {
		t.Fatal(err)
	}

	lf, err := Read(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lf.Packages) != 2 {
		t.Fatalf("got %d packages, want 2",
			len(lf.Packages))
	}
	if lf.Packages["jq"].Version != "1.7.1" {
		t.Errorf("jq version = %q, want 1.7.1",
			lf.Packages["jq"].Version)
	}
	if lf.Packages["jq"].SHA256 != "abc123" {
		t.Errorf("jq sha256 = %q, want abc123",
			lf.Packages["jq"].SHA256)
	}
	if lf.Packages["ripgrep"].Version != "14.1.0" {
		t.Errorf("ripgrep version = %q, want 14.1.0",
			lf.Packages["ripgrep"].Version)
	}
}

func TestReadMissingFileReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gale.lock")

	lf, err := Read(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lf.Packages) != 0 {
		t.Errorf("got %d packages, want 0",
			len(lf.Packages))
	}
}

func TestReadMalformedTOMLErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.lock")
	if err := os.WriteFile(path,
		[]byte("not [valid toml"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Read(path)
	if err == nil {
		t.Fatal("expected error for malformed TOML")
	}
}

// --- Behavior 2: Write lock file ---

func TestWriteRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.lock")

	original := &LockFile{
		Packages: map[string]LockedPackage{
			"jq":      {Version: "1.7.1", SHA256: "abc"},
			"ripgrep": {Version: "14.1.0", SHA256: "def"},
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
		t.Fatalf("got %d packages, want 2",
			len(parsed.Packages))
	}
	if parsed.Packages["jq"].Version != "1.7.1" {
		t.Errorf("jq version = %q, want 1.7.1",
			parsed.Packages["jq"].Version)
	}
	if parsed.Packages["jq"].SHA256 != "abc" {
		t.Errorf("jq sha256 = %q, want abc",
			parsed.Packages["jq"].SHA256)
	}
}

func TestWriteOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.lock")

	first := &LockFile{
		Packages: map[string]LockedPackage{
			"jq": {Version: "1.7.1"},
		},
	}
	if err := Write(path, first); err != nil {
		t.Fatal(err)
	}

	second := &LockFile{
		Packages: map[string]LockedPackage{
			"ripgrep": {Version: "14.1.0"},
		},
	}
	if err := Write(path, second); err != nil {
		t.Fatal(err)
	}

	parsed, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed.Packages["jq"]; ok {
		t.Error("jq should be gone after overwrite")
	}
	if parsed.Packages["ripgrep"].Version != "14.1.0" {
		t.Errorf("ripgrep = %q, want 14.1.0",
			parsed.Packages["ripgrep"].Version)
	}
}

func TestWriteEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.lock")

	lf := &LockFile{
		Packages: map[string]LockedPackage{},
	}
	if err := Write(path, lf); err != nil {
		t.Fatal(err)
	}

	parsed, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Packages) != 0 {
		t.Errorf("got %d, want 0", len(parsed.Packages))
	}
}

// --- Behavior 3: Detect stale lock ---

func writeLock(t *testing.T, path string, pkgs map[string]LockedPackage) {
	t.Helper()
	lf := &LockFile{Packages: pkgs}
	if err := Write(path, lf); err != nil {
		t.Fatal(err)
	}
}

func TestIsStaleInSync(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "gale.toml")
	lockPath := filepath.Join(dir, "gale.lock")

	pkgs := map[string]string{"jq": "1.7.1"}

	if err := os.WriteFile(tomlPath,
		[]byte("[packages]\njq = \"1.7.1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeLock(t, lockPath, map[string]LockedPackage{
		"jq": {Version: "1.7.1"},
	})

	// Lock must be newer than toml.
	past := time.Now().Add(-10 * time.Second)
	if err := os.Chtimes(tomlPath, past, past); err != nil {
		t.Fatal(err)
	}

	stale, err := IsStale(tomlPath, lockPath, pkgs)
	if err != nil {
		t.Fatal(err)
	}
	if stale {
		t.Error("expected not stale when in sync")
	}
}

func TestIsStaleTOMLNewer(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "gale.toml")
	lockPath := filepath.Join(dir, "gale.lock")

	pkgs := map[string]string{"jq": "1.7.1"}
	writeLock(t, lockPath, map[string]LockedPackage{
		"jq": {Version: "1.7.1"},
	})

	past := time.Now().Add(-10 * time.Second)
	if err := os.Chtimes(lockPath, past, past); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(tomlPath,
		[]byte("[packages]\njq = \"1.7.1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stale, err := IsStale(tomlPath, lockPath, pkgs)
	if err != nil {
		t.Fatal(err)
	}
	if !stale {
		t.Error("expected stale when toml is newer")
	}
}

func TestIsStaleExtraPackage(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "gale.toml")
	lockPath := filepath.Join(dir, "gale.lock")

	tomlPkgs := map[string]string{
		"jq": "1.7.1", "ripgrep": "14.1.0",
	}

	if err := os.WriteFile(tomlPath,
		[]byte("[packages]\njq = \"1.7.1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeLock(t, lockPath, map[string]LockedPackage{
		"jq": {Version: "1.7.1"},
	})

	past := time.Now().Add(-10 * time.Second)
	if err := os.Chtimes(tomlPath, past, past); err != nil {
		t.Fatal(err)
	}

	stale, err := IsStale(tomlPath, lockPath, tomlPkgs)
	if err != nil {
		t.Fatal(err)
	}
	if !stale {
		t.Error("expected stale when toml has extra pkg")
	}
}

func TestIsStaleMissingLock(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "gale.toml")
	lockPath := filepath.Join(dir, "gale.lock")

	if err := os.WriteFile(tomlPath,
		[]byte("[packages]\njq = \"1.7.1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stale, err := IsStale(tomlPath, lockPath,
		map[string]string{"jq": "1.7.1"})
	if err != nil {
		t.Fatal(err)
	}
	if !stale {
		t.Error("expected stale when lock missing")
	}
}

func TestIsStaleVersionDiffers(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "gale.toml")
	lockPath := filepath.Join(dir, "gale.lock")

	if err := os.WriteFile(tomlPath,
		[]byte("[packages]\njq = \"1.8.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeLock(t, lockPath, map[string]LockedPackage{
		"jq": {Version: "1.7.1"},
	})

	past := time.Now().Add(-10 * time.Second)
	if err := os.Chtimes(tomlPath, past, past); err != nil {
		t.Fatal(err)
	}

	stale, err := IsStale(tomlPath, lockPath,
		map[string]string{"jq": "1.8.0"})
	if err != nil {
		t.Fatal(err)
	}
	if !stale {
		t.Error("expected stale when versions differ")
	}
}
