package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/lockfile"
)

func TestLockfilePathWithTomlSuffix(t *testing.T) {
	got := lockfilePath("/home/user/.gale/gale.toml")
	want := "/home/user/.gale/gale.lock"
	if got != want {
		t.Errorf("lockfilePath() = %q, want %q", got, want)
	}
}

func TestLockfilePathWithoutTomlSuffixPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for path without .toml suffix")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("expected string panic, got %T: %v", r, r)
		}
		if !strings.Contains(msg, ".toml") {
			t.Errorf("panic message %q should mention .toml", msg)
		}
	}()

	lockfilePath("/home/user/.gale/gale.conf")
}

func TestLockfilePathShortPathPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for short path")
		}
	}()

	lockfilePath("abc")
}

func TestWriteConfigAndLockUpdatesLockfileOnCachedInstall(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  mypkg = \"1.0.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	// Write an initial lockfile with v1.0.0 and a hash.
	lockPath := filepath.Join(tmp, "gale.lock")
	lf := &lockfile.LockFile{
		Packages: map[string]lockfile.LockedPackage{
			"mypkg": {Version: "1.0.0", SHA256: "oldhash123"},
		},
	}
	if err := lockfile.Write(lockPath, lf); err != nil {
		t.Fatal(err)
	}

	// Simulate a cached install to v2.0.0 (sha256 is empty).
	if err := writeConfigAndLock(
		configPath, "mypkg", "2.0.0", ""); err != nil {
		t.Fatalf("writeConfigAndLock: %v", err)
	}

	// Read the lockfile back. The version should be updated
	// even though sha256 was empty. The old hash must not
	// remain associated with the new version.
	got, err := lockfile.Read(lockPath)
	if err != nil {
		t.Fatalf("reading lockfile: %v", err)
	}

	pkg, ok := got.Packages["mypkg"]
	if !ok {
		t.Fatal("mypkg not found in lockfile")
	}
	if pkg.Version != "2.0.0" {
		t.Errorf("lockfile version = %q, want %q",
			pkg.Version, "2.0.0")
	}
	if pkg.SHA256 == "oldhash123" {
		t.Error("lockfile still has old hash from v1.0.0")
	}
}

func TestWriteConfigAndLockPreservesHashOnSameVersionCache(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  mypkg = \"1.0.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	// Write a lockfile with v1.0.0 and a valid hash.
	lockPath := filepath.Join(tmp, "gale.lock")
	lf := &lockfile.LockFile{
		Packages: map[string]lockfile.LockedPackage{
			"mypkg": {Version: "1.0.0", SHA256: "validhash"},
		},
	}
	if err := lockfile.Write(lockPath, lf); err != nil {
		t.Fatal(err)
	}

	// Cached install of the same version (sha256 empty).
	if err := writeConfigAndLock(
		configPath, "mypkg", "1.0.0", ""); err != nil {
		t.Fatalf("writeConfigAndLock: %v", err)
	}

	// The existing hash should be preserved.
	got, err := lockfile.Read(lockPath)
	if err != nil {
		t.Fatalf("reading lockfile: %v", err)
	}
	pkg := got.Packages["mypkg"]
	if pkg.SHA256 != "validhash" {
		t.Errorf("lockfile hash = %q, want %q",
			pkg.SHA256, "validhash")
	}
}
