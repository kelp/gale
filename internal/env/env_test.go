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

func TestGenerateHookUnsupportedShellReturnsError(t *testing.T) {
	_, err := GenerateHook("powershell")
	if err == nil {
		t.Fatal("expected error for unsupported shell")
	}
}
