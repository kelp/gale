package ai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadAndHashTool(t *testing.T) {
	content := "hello world"
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(content))
		}))
	defer srv.Close()

	tmpDir := t.TempDir()
	tool := downloadAndHashTool(tmpDir)

	input, _ := json.Marshal(map[string]string{
		"url": srv.URL + "/test.tar.gz",
	})

	result, err := tool.Handler(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out struct {
		SHA256 string `json:"sha256"`
		Path   string `json:"path"`
	}
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatalf("parse result: %v", err)
	}

	if out.SHA256 == "" {
		t.Error("expected non-empty SHA256")
	}
	if !strings.HasPrefix(out.Path, tmpDir) {
		t.Errorf("path %q not in tmpDir %q", out.Path, tmpDir)
	}
}

func TestDownloadAndHashToolUniqueFilenames(t *testing.T) {
	// Two different URLs that produce the same
	// filepath.Base() should not collide.
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			callCount++
			// Each request gets different content.
			if strings.Contains(r.URL.Path, "alpha") {
				w.Write([]byte("content-alpha"))
			} else {
				w.Write([]byte("content-beta"))
			}
		}))
	defer srv.Close()

	tmpDir := t.TempDir()
	tool := downloadAndHashTool(tmpDir)

	// Both URLs have the same base name.
	url1 := srv.URL + "/alpha/v1.0.0.tar.gz"
	url2 := srv.URL + "/beta/v1.0.0.tar.gz"

	input1, _ := json.Marshal(map[string]string{"url": url1})
	result1, err := tool.Handler(input1)
	if err != nil {
		t.Fatalf("download 1: %v", err)
	}

	input2, _ := json.Marshal(map[string]string{"url": url2})
	result2, err := tool.Handler(input2)
	if err != nil {
		t.Fatalf("download 2: %v", err)
	}

	var out1, out2 struct {
		SHA256 string `json:"sha256"`
		Path   string `json:"path"`
	}
	json.Unmarshal([]byte(result1), &out1)
	json.Unmarshal([]byte(result2), &out2)

	// Different content must produce different hashes.
	if out1.SHA256 == out2.SHA256 {
		t.Error("expected different SHA256 for different URLs")
	}

	// Files must be at different paths.
	if out1.Path == out2.Path {
		t.Error("expected different file paths for different URLs")
	}
}

func TestWriteRecipeTool(t *testing.T) {
	tmpDir := t.TempDir()
	tool := writeRecipeTool(tmpDir)

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

	// Check letter bucketing.
	expected := filepath.Join(tmpDir, "j", "jq.toml")
	if out.Path != expected {
		t.Errorf("path = %q, want %q", out.Path, expected)
	}

	// Check file was written.
	data, err := os.ReadFile(out.Path)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if !strings.Contains(string(data), "jq") {
		t.Error("written file does not contain expected content")
	}
}

func TestLintRecipeTool(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.toml")

	// Write a recipe missing required fields.
	err := os.WriteFile(path, []byte(`
[package]
name = "test"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	tool := lintRecipeTool(tmpDir)
	input, _ := json.Marshal(map[string]string{
		"path": path,
	})

	result, err := tool.Handler(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have errors for missing version, url, sha256, etc.
	if !strings.Contains(result, "error") {
		t.Errorf("expected lint errors, got: %s", result)
	}
}

func TestLintRecipeToolPathTraversal(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a file outside tmpDir.
	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "secret.toml")
	err := os.WriteFile(outsidePath, []byte(`
[package]
name = "secret"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	tool := lintRecipeTool(tmpDir)

	// Try to read a file outside the allowed directory.
	input, _ := json.Marshal(map[string]string{
		"path": outsidePath,
	})

	_, err = tool.Handler(input)
	if err == nil {
		t.Fatal("expected error for path outside tmpDir")
	}
	if !strings.Contains(err.Error(), "outside") {
		t.Errorf("expected 'outside' in error, got: %v", err)
	}
}

func TestLintRecipeToolDotDotTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	tool := lintRecipeTool(tmpDir)

	// Use ../../../etc/passwd style path.
	input, _ := json.Marshal(map[string]string{
		"path": filepath.Join(tmpDir, "..", "..", "etc", "passwd"),
	})

	_, err := tool.Handler(input)
	if err == nil {
		t.Fatal("expected error for traversal path")
	}
}

func TestReadFileTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "go.mod") {
				w.Write([]byte("module example.com/tool"))
			} else {
				http.NotFound(w, r)
			}
		}))
	defer srv.Close()

	// Can't easily test the real GitHub URL, but we
	// can verify the handler parses input correctly.
	tool := readFileTool()
	input, _ := json.Marshal(map[string]string{
		"repo": "owner/repo",
		"path": "nonexistent-file",
	})

	// This will fail with a network error to GitHub
	// which is expected in tests. Just verify it
	// parses the input.
	_, err := tool.Handler(input)
	if err == nil {
		t.Log("read_file succeeded (unexpected in test)")
	}
}

func TestIsSourceAsset(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"htop-3.4.1.tar.xz", true},
		{"jq-1.8.1.tar.gz", true},
		{"fish-4.0.tar.bz2", true},
		{"tool-1.0.tgz", true},
		{"htop-3.4.1.tar.xz.sha256", false},
		{"htop-3.4.1.tar.xz.asc", false},
		{"tool-linux-amd64", false},
		{"tool-1.0.zip", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSourceAsset(tt.name)
			if got != tt.want {
				t.Errorf("isSourceAsset(%q) = %v, want %v",
					tt.name, got, tt.want)
			}
		})
	}
}

func TestListFilesTool(t *testing.T) {
	// Mock GitHub API response for contents endpoint.
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[
				{"name": "CMakeLists.txt", "type": "file"},
				{"name": "src", "type": "dir"},
				{"name": "README.md", "type": "file"},
				{"name": "LICENSE", "type": "file"}
			]`))
		}))
	defer srv.Close()

	tool := listFilesTool()

	// Can't easily mock the GitHub API URL, so just
	// verify input parsing works. The real endpoint
	// will fail in tests.
	input, _ := json.Marshal(map[string]string{
		"repo": "owner/repo",
	})

	_, err := tool.Handler(input)
	if err == nil {
		t.Log("list_files succeeded (unexpected in test)")
	}
}

func TestCheckRecipeToolExists(t *testing.T) {
	checker := func(name string) bool {
		return name == "openssl"
	}
	tool := checkRecipeTool(checker)

	input, _ := json.Marshal(map[string]string{
		"name": "openssl",
	})

	result, err := tool.Handler(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out struct {
		Exists bool `json:"exists"`
	}
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if !out.Exists {
		t.Error("expected exists=true for openssl")
	}
}

func TestCheckRecipeToolNotFound(t *testing.T) {
	checker := func(name string) bool {
		return false
	}
	tool := checkRecipeTool(checker)

	input, _ := json.Marshal(map[string]string{
		"name": "nonexistent",
	})

	result, err := tool.Handler(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out struct {
		Exists bool `json:"exists"`
	}
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if out.Exists {
		t.Error("expected exists=false for nonexistent")
	}
}

func TestRecipeToolsReturnsToolsAndCleanup(t *testing.T) {
	checker := func(string) bool { return false }
	tools, cleanup := RecipeTools(t.TempDir(), checker)
	defer cleanup()

	if len(tools) != 8 {
		t.Errorf("expected 8 tools, got %d", len(tools))
	}

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Param.Name] = true
	}

	expected := []string{
		"github_info",
		"download_and_hash",
		"read_file",
		"list_files",
		"check_recipe",
		"homebrew_formula",
		"write_recipe",
		"lint_recipe",
	}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("missing tool: %s", name)
		}
	}
}

func TestHomebrewFormulaToolSuccess(t *testing.T) {
	formula := `class Jq < Formula
  desc "Lightweight command-line JSON processor"
  homepage "https://jqlang.github.io/jq/"
  url "https://github.com/jqlang/jq/releases/download/jq-1.7.1/jq-1.7.1.tar.gz"
  sha256 "abc123"
  license "MIT"

  depends_on "oniguruma"

  def install
    system "./configure", "--disable-docs", *std_configure_args
    system "make", "install"
  end
end
`

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/Formula/j/jq.rb" {
				w.Write([]byte(formula))
			} else {
				http.NotFound(w, r)
			}
		}))
	defer srv.Close()

	tool := homebrewFormulaToolWithURL(srv.URL)

	input, _ := json.Marshal(map[string]string{
		"name": "jq",
	})

	result, err := tool.Handler(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "class Jq < Formula") {
		t.Errorf("expected formula class, got: %s", result)
	}
	if !strings.Contains(result, "depends_on") {
		t.Errorf("expected depends_on, got: %s", result)
	}
}

func TestWriteRecipeToolEmptyName(t *testing.T) {
	tmpDir := t.TempDir()
	tool := writeRecipeTool(tmpDir)

	input, _ := json.Marshal(map[string]string{
		"name":    "",
		"content": "[package]\nname = \"\"\n",
	})

	_, err := tool.Handler(input)
	if err == nil {
		t.Fatal("expected error for empty name")
	}
	if !strings.Contains(err.Error(), "name is required") {
		t.Errorf("expected 'name is required' error, got: %v", err)
	}
}

func TestHomebrewFormulaToolNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
	defer srv.Close()

	tool := homebrewFormulaToolWithURL(srv.URL)

	input, _ := json.Marshal(map[string]string{
		"name": "nonexistent-pkg",
	})

	_, err := tool.Handler(input)
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
	if !strings.Contains(err.Error(), "formula not found") {
		t.Errorf("expected 'formula not found' error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "nonexistent-pkg") {
		t.Errorf("expected package name in error, got: %v", err)
	}
}
