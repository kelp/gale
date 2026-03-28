package lockfile

import (
	"bytes"
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

// --- Read: unreadable file returns error ---

func TestReadUnreadableFileErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.lock")
	if err := os.WriteFile(path,
		[]byte("[packages.jq]\nversion = \"1.7.1\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}

	_, err := Read(path)
	if err == nil {
		t.Fatal("expected error for unreadable file")
	}
}

// --- Read: TOML with no packages section ---

func TestReadNoPackagesSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.lock")
	if err := os.WriteFile(path,
		[]byte("# empty lock file\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	lf, err := Read(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lf.Packages == nil {
		t.Fatal("Packages map should be initialized")
	}
	if len(lf.Packages) != 0 {
		t.Errorf("got %d packages, want 0",
			len(lf.Packages))
	}
}

// --- Write: nonexistent parent directory ---

func TestWriteNonexistentDirErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(),
		"no-such-dir", "gale.lock")
	lf := &LockFile{
		Packages: map[string]LockedPackage{
			"jq": {Version: "1.7.1"},
		},
	}

	err := Write(path, lf)
	if err == nil {
		t.Fatal("expected error for nonexistent dir")
	}
}

// --- Write: preserves original on rename failure ---

func TestWritePreservesOriginalOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.lock")

	original := &LockFile{
		Packages: map[string]LockedPackage{
			"jq": {Version: "1.7.1"},
		},
	}
	if err := Write(path, original); err != nil {
		t.Fatal(err)
	}

	// Write original to a directory, then make it
	// read-only so the next Write fails.
	readOnlyDir := filepath.Join(t.TempDir(), "readonly")
	if err := os.Mkdir(readOnlyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	roPath := filepath.Join(readOnlyDir, "gale.lock")

	if err := Write(roPath, original); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(readOnlyDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Chmod(readOnlyDir, 0o755)
	})

	err := Write(roPath, &LockFile{
		Packages: map[string]LockedPackage{
			"ripgrep": {Version: "14.1.0"},
		},
	})
	if err == nil {
		t.Fatal("expected error writing to read-only dir")
	}

	// Original should still be readable.
	os.Chmod(readOnlyDir, 0o755)
	parsed, err := Read(roPath)
	if err != nil {
		t.Fatalf("original destroyed: %v", err)
	}
	if parsed.Packages["jq"].Version != "1.7.1" {
		t.Error("original content was corrupted")
	}
}

// --- Write + Read: SHA256 round-trip ---

func TestWriteSHA256RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.lock")

	original := &LockFile{
		Packages: map[string]LockedPackage{
			"jq": {
				Version: "1.7.1",
				SHA256:  "e4287154fc6a0e9e24273b61b6e3b68ebc76e249b52093a457e65b9e40bdb278",
			},
		},
	}

	if err := Write(path, original); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	parsed, err := Read(path)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}

	got := parsed.Packages["jq"].SHA256
	want := "e4287154fc6a0e9e24273b61b6e3b68ebc76e249b52093a457e65b9e40bdb278"
	if got != want {
		t.Errorf("SHA256 = %q, want %q", got, want)
	}
}

// --- Write: omits SHA256 when empty ---

func TestWriteOmitsEmptySHA256(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.lock")

	lf := &LockFile{
		Packages: map[string]LockedPackage{
			"jq": {Version: "1.7.1"},
		},
	}
	if err := Write(path, lf); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if bytes.Contains(data, []byte("sha256")) {
		t.Errorf("expected no sha256 key, got:\n%s",
			content)
	}
}

// --- IsStale: lock file stat error (not ENOENT) ---

func TestIsStaleStatLockError(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "gale.toml")
	lockPath := filepath.Join(dir, "subdir", "gale.lock")

	if err := os.WriteFile(tomlPath,
		[]byte("[packages]\njq = \"1.7.1\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	// Create lock inside a directory, then make directory
	// unreadable so stat fails with permission error.
	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath,
		[]byte("[packages.jq]\nversion = \"1.7.1\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(subdir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Chmod(subdir, 0o755)
	})

	_, err := IsStale(tomlPath, lockPath,
		map[string]string{"jq": "1.7.1"})
	if err == nil {
		t.Fatal("expected error for unreadable lock dir")
	}
}

// --- IsStale: missing gale.toml ---

func TestIsStaleStatTOMLError(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "gale.toml")
	lockPath := filepath.Join(dir, "gale.lock")

	writeLock(t, lockPath, map[string]LockedPackage{
		"jq": {Version: "1.7.1"},
	})

	// tomlPath does not exist.
	_, err := IsStale(tomlPath, lockPath,
		map[string]string{"jq": "1.7.1"})
	if err == nil {
		t.Fatal("expected error when gale.toml missing")
	}
}

// --- IsStale: malformed lock file ---

func TestIsStaleMalformedLockErrors(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "gale.toml")
	lockPath := filepath.Join(dir, "gale.lock")

	if err := os.WriteFile(tomlPath,
		[]byte("[packages]\njq = \"1.7.1\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath,
		[]byte("not [valid toml"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Lock must be newer than toml.
	past := time.Now().Add(-10 * time.Second)
	if err := os.Chtimes(tomlPath, past, past); err != nil {
		t.Fatal(err)
	}

	_, err := IsStale(tomlPath, lockPath,
		map[string]string{"jq": "1.7.1"})
	if err == nil {
		t.Fatal("expected error for malformed lock")
	}
}

// --- IsStale: lock has extra package not in toml ---

func TestIsStaleLockHasExtraPackage(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "gale.toml")
	lockPath := filepath.Join(dir, "gale.lock")

	if err := os.WriteFile(tomlPath,
		[]byte("[packages]\njq = \"1.7.1\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	writeLock(t, lockPath, map[string]LockedPackage{
		"jq":      {Version: "1.7.1"},
		"ripgrep": {Version: "14.1.0"},
	})

	past := time.Now().Add(-10 * time.Second)
	if err := os.Chtimes(tomlPath, past, past); err != nil {
		t.Fatal(err)
	}

	stale, err := IsStale(tomlPath, lockPath,
		map[string]string{"jq": "1.7.1"})
	if err != nil {
		t.Fatal(err)
	}
	if !stale {
		t.Error("expected stale when lock has extra pkg")
	}
}

// --- IsStale: package in toml not in lock ---

func TestIsStalePackageMissingFromLock(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "gale.toml")
	lockPath := filepath.Join(dir, "gale.lock")

	if err := os.WriteFile(tomlPath,
		[]byte("[packages]\njq = \"1.7.1\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	writeLock(t, lockPath, map[string]LockedPackage{
		"ripgrep": {Version: "14.1.0"},
	})

	past := time.Now().Add(-10 * time.Second)
	if err := os.Chtimes(tomlPath, past, past); err != nil {
		t.Fatal(err)
	}

	stale, err := IsStale(tomlPath, lockPath,
		map[string]string{"jq": "1.7.1"})
	if err != nil {
		t.Fatal(err)
	}
	if !stale {
		t.Error("expected stale when pkg missing from lock")
	}
}

// --- Read: extra fields in TOML are ignored ---

func TestReadIgnoresUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.lock")
	content := `[packages.jq]
version = "1.7.1"
sha256 = "abc123"
unknown_field = "should be ignored"
`
	if err := os.WriteFile(path,
		[]byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	lf, err := Read(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lf.Packages["jq"].Version != "1.7.1" {
		t.Errorf("version = %q, want 1.7.1",
			lf.Packages["jq"].Version)
	}
	if lf.Packages["jq"].SHA256 != "abc123" {
		t.Errorf("sha256 = %q, want abc123",
			lf.Packages["jq"].SHA256)
	}
}

// --- Write + Read: multiple packages survive ---

func TestWriteMultiplePackagesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.lock")

	original := &LockFile{
		Packages: map[string]LockedPackage{
			"jq":       {Version: "1.7.1", SHA256: "aaa"},
			"ripgrep":  {Version: "14.1.0", SHA256: "bbb"},
			"fd":       {Version: "9.0.0", SHA256: "ccc"},
			"bat":      {Version: "0.24.0"},
			"starship": {Version: "1.17.1", SHA256: "ddd"},
		},
	}

	if err := Write(path, original); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	parsed, err := Read(path)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}

	if len(parsed.Packages) != len(original.Packages) {
		t.Fatalf("got %d packages, want %d",
			len(parsed.Packages),
			len(original.Packages))
	}

	for name, orig := range original.Packages {
		got, ok := parsed.Packages[name]
		if !ok {
			t.Errorf("package %q missing after round-trip",
				name)
			continue
		}
		if got.Version != orig.Version {
			t.Errorf("%s version = %q, want %q",
				name, got.Version, orig.Version)
		}
		if got.SHA256 != orig.SHA256 {
			t.Errorf("%s sha256 = %q, want %q",
				name, got.SHA256, orig.SHA256)
		}
	}
}
