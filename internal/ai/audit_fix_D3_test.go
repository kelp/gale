package ai

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #64: write_recipe must reject LLM-supplied names
// that escape the temp dir via .. traversal.
func TestWriteRecipeToolDotDotTraversal(t *testing.T) {
	base := t.TempDir()
	tmpDir := filepath.Join(base, "a", "b")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatal(err)
	}

	tool := writeRecipeTool(tmpDir)
	input, _ := json.Marshal(map[string]string{
		"name":    "../../evil",
		"content": "[package]\nname = \"evil\"\n",
	})

	_, err := tool.Handler(input)
	if err == nil {
		t.Fatal("expected error for traversal name")
	}

	// The escaped path must not have been written.
	escaped := filepath.Join(base, "evil.toml")
	if _, statErr := os.Stat(escaped); statErr == nil {
		t.Errorf("file written outside tmpDir: %s", escaped)
	}
}

// Issue #64: names containing path separators are not
// plain filenames and must be rejected.
func TestWriteRecipeToolNameWithSeparator(t *testing.T) {
	tmpDir := t.TempDir()
	tool := writeRecipeTool(tmpDir)

	input, _ := json.Marshal(map[string]string{
		"name":    "sub/pkg",
		"content": "[package]\nname = \"pkg\"\n",
	})

	_, err := tool.Handler(input)
	if err == nil {
		t.Fatal("expected error for name with separator")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("expected 'invalid' in error, got: %v", err)
	}
}

// Issue #64: a leading-dot name makes the letter bucket
// "." and must be rejected.
func TestWriteRecipeToolLeadingDotName(t *testing.T) {
	tmpDir := t.TempDir()
	tool := writeRecipeTool(tmpDir)

	input, _ := json.Marshal(map[string]string{
		"name":    ".hidden",
		"content": "[package]\nname = \"hidden\"\n",
	})

	_, err := tool.Handler(input)
	if err == nil {
		t.Fatal("expected error for leading-dot name")
	}

	if _, statErr := os.Stat(filepath.Join(tmpDir, ".hidden.toml")); statErr == nil {
		t.Error("leading-dot file was written")
	}
}

// Issue #64: an absolute name must be rejected outright.
func TestWriteRecipeToolAbsoluteName(t *testing.T) {
	tmpDir := t.TempDir()
	tool := writeRecipeTool(tmpDir)

	input, _ := json.Marshal(map[string]string{
		"name":    filepath.Join(t.TempDir(), "evil"),
		"content": "[package]\nname = \"evil\"\n",
	})

	_, err := tool.Handler(input)
	if err == nil {
		t.Fatal("expected error for absolute name")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("expected 'invalid' in error, got: %v", err)
	}
}

// Issue #64: a symlinked letter dir inside tmpDir must not
// let write_recipe escape to the symlink target.
func TestWriteRecipeToolSymlinkEscape(t *testing.T) {
	tmpDir := t.TempDir()
	outside := t.TempDir()

	// Plant a symlink where the letter bucket would go.
	if err := os.Symlink(outside, filepath.Join(tmpDir, "e")); err != nil {
		t.Fatal(err)
	}

	tool := writeRecipeTool(tmpDir)
	input, _ := json.Marshal(map[string]string{
		"name":    "evil",
		"content": "[package]\nname = \"evil\"\n",
	})

	_, err := tool.Handler(input)
	if err == nil {
		t.Fatal("expected error for symlinked letter dir")
	}

	if _, statErr := os.Stat(filepath.Join(outside, "evil.toml")); statErr == nil {
		t.Error("file written through symlink outside tmpDir")
	}
}

// Issue #64 regression: when the allowed dir itself is
// reached via a symlink (macOS: /var -> /private/var, so
// every t.TempDir() path is symlinked), containment must
// still pass and the returned path must keep the caller's
// symlinked form, not the resolved one.
func TestWriteRecipeToolSymlinkedAllowedDir(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	tool := writeRecipeTool(link)
	input, _ := json.Marshal(map[string]string{
		"name":    "jq",
		"content": "[package]\nname = \"jq\"\n",
	})

	result, err := tool.Handler(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatalf("parse result: %v", err)
	}

	// The path must use the symlinked form the caller gave,
	// exactly as TestWriteRecipeTool expects on macOS.
	expected := filepath.Join(link, "j", "jq.toml")
	if out.Path != expected {
		t.Errorf("path = %q, want %q", out.Path, expected)
	}
	if _, err := os.Stat(expected); err != nil {
		t.Errorf("written file missing: %v", err)
	}
}

// Issue #64: lint_recipe's containment must also follow
// symlinks — a link inside the allowed dir pointing
// outside must be rejected.
func TestLintRecipeToolSymlinkEscape(t *testing.T) {
	tmpDir := t.TempDir()
	outside := t.TempDir()

	secret := filepath.Join(outside, "secret.toml")
	if err := os.WriteFile(secret, []byte("[package]\nname = \"s\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tmpDir, "link.toml")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}

	tool := lintRecipeTool(tmpDir)
	input, _ := json.Marshal(map[string]string{
		"path": link,
	})

	_, err := tool.Handler(input)
	if err == nil {
		t.Fatal("expected error for symlink escaping allowed dir")
	}
	if !strings.Contains(err.Error(), "outside") {
		t.Errorf("expected 'outside' in error, got: %v", err)
	}
}
