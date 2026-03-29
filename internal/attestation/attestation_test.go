package attestation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAvailableWhenGHOnPath(t *testing.T) {
	// Point at a real binary that exists.
	orig := lookPath
	lookPath = func(name string) (string, error) {
		return "/usr/bin/gh", nil
	}
	defer func() { lookPath = orig }()
	resetAvailable()

	if !Available() {
		t.Error("expected Available to return true")
	}
}

func TestAvailableWhenGHMissing(t *testing.T) {
	orig := lookPath
	lookPath = func(name string) (string, error) {
		return "", &os.PathError{Op: "lookpath", Path: "gh"}
	}
	defer func() { lookPath = orig }()
	resetAvailable()

	if Available() {
		t.Error("expected Available to return false")
	}
}

func TestVerifyFileSuccess(t *testing.T) {
	mock := writeMockGH(t, 0)
	orig := lookPath
	lookPath = func(name string) (string, error) {
		return mock, nil
	}
	defer func() { lookPath = orig }()
	resetAvailable()

	if err := VerifyFile("test.tar.zst", "owner/repo"); err != nil {
		t.Errorf("expected success, got %v", err)
	}
}

func TestVerifyFileFailure(t *testing.T) {
	mock := writeMockGH(t, 1)
	orig := lookPath
	lookPath = func(name string) (string, error) {
		return mock, nil
	}
	defer func() { lookPath = orig }()
	resetAvailable()

	err := VerifyFile("test.tar.zst", "owner/repo")
	if err == nil {
		t.Error("expected error, got nil")
	}
}

func TestVerifyFileGHNotAvailable(t *testing.T) {
	orig := lookPath
	lookPath = func(name string) (string, error) {
		return "", &os.PathError{Op: "lookpath", Path: "gh"}
	}
	defer func() { lookPath = orig }()
	resetAvailable()

	err := VerifyFile("test.tar.zst", "owner/repo")
	if err == nil {
		t.Error("expected error when gh not available")
	}
}

func TestDisableOverridesAvailable(t *testing.T) {
	orig := lookPath
	lookPath = func(name string) (string, error) {
		return "/usr/bin/gh", nil
	}
	defer func() { lookPath = orig }()
	resetAvailable()

	Disable()
	if Available() {
		t.Error("expected Available to return false when disabled")
	}
	Enable()
	if !Available() {
		t.Error("expected Available to return true after Enable")
	}
}

// writeMockGH creates a shell script that exits with
// the given code. Returns the path to the script.
func writeMockGH(t *testing.T, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	script := "#!/bin/sh\n"
	if exitCode != 0 {
		script += "echo 'verification failed' >&2\n"
	}
	script += "exit " + itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	return "1"
}
