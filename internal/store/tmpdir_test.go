package store

import (
	"os"
	"path/filepath"
	"testing"
)

func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func breakGaleDir(t *testing.T) {
	t.Helper()
	home := isolateHome(t)
	blocker := filepath.Join(home, ".gale")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatalf("plant %s as a regular file: %v", blocker, err)
	}
}

func breakSystemTemp(t *testing.T) {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatalf("plant %s as a regular file: %v", blocker, err)
	}
	t.Setenv("TMPDIR", filepath.Join(blocker, "tmp"))
}

func TestTmpDirReturnsHomeScopedPath(t *testing.T) {
	home := isolateHome(t)
	dir, err := TmpDir()
	if err != nil {
		t.Fatalf("TmpDir: %v", err)
	}
	want := filepath.Join(home, ".gale", "tmp")
	if dir != want {
		t.Errorf("TmpDir = %q, want %q", dir, want)
	}
}

func TestTmpDirFallsBackToSystemTempWhenGaleDirUnusable(t *testing.T) {
	breakGaleDir(t)
	dir, err := TmpDir()
	if err != nil {
		t.Fatalf("TmpDir returned %v; an unusable ~/.gale/tmp must fall back", err)
	}
	if dir != os.TempDir() {
		t.Errorf("TmpDir = %q, want %q", dir, os.TempDir())
	}
	probe, err := os.MkdirTemp(dir, "gale-fallback-probe-*")
	if err != nil {
		t.Fatalf("TmpDir returned an unusable path %q: %v", dir, err)
	}
	_ = os.RemoveAll(probe)
}

func TestTmpDirErrorsWhenNeitherLocationUsable(t *testing.T) {
	breakGaleDir(t)
	breakSystemTemp(t)

	dir, err := TmpDir()
	if err == nil {
		t.Fatalf("TmpDir() = %q, <nil>; want an error when neither location is usable", dir)
	}
	if dir != "" {
		t.Errorf("TmpDir() returned path %q alongside error %v", dir, err)
	}
}
