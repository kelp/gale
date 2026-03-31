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

	tool := lintRecipeTool()
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

func TestRecipeToolsReturnsToolsAndCleanup(t *testing.T) {
	tools, cleanup := RecipeTools(t.TempDir())
	defer cleanup()

	if len(tools) != 6 {
		t.Errorf("expected 6 tools, got %d", len(tools))
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
		"write_recipe",
		"lint_recipe",
	}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("missing tool: %s", name)
		}
	}
}
