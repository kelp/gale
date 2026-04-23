package env

import (
	"strings"
	"testing"
)

func TestGenerateHookDirenvNoError(t *testing.T) {
	hook, err := GenerateHook("direnv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hook == "" {
		t.Error("expected non-empty direnv hook output")
	}
}

func TestGenerateHookDirenvContainsUseGale(t *testing.T) {
	hook, err := GenerateHook("direnv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(hook, "use_gale") {
		t.Errorf("direnv hook missing 'use_gale': %q", hook)
	}
}

func TestGenerateHookDirenvContainsPATHAdd(t *testing.T) {
	hook, err := GenerateHook("direnv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(hook, "PATH_add") {
		t.Errorf("direnv hook missing 'PATH_add': %q", hook)
	}
}

func TestGenerateHookDirenvWatchesManifest(t *testing.T) {
	hook, err := GenerateHook("direnv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(hook, "watch_file") {
		t.Errorf("direnv hook missing 'watch_file': %q", hook)
	}
	if !strings.Contains(hook, "gale.toml") {
		t.Errorf("direnv hook missing 'gale.toml': %q", hook)
	}
}

func TestGenerateHookDirenvSkipsSyncWhenFresh(t *testing.T) {
	hook, err := GenerateHook("direnv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The freshness check guards `gale sync` behind a mtime
	// comparison so activation is a no-op when nothing changed.
	if !strings.Contains(hook, "-nt") {
		t.Errorf("direnv hook missing '-nt' freshness check: %q", hook)
	}
}

func TestGenerateHookUnsupportedShellReturnsError(t *testing.T) {
	_, err := GenerateHook("powershell")
	if err == nil {
		t.Fatal("expected error for unsupported shell")
	}
}
