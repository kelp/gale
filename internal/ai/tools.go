package ai

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/lint"
)

// RecipeTools returns the tools available for recipe
// creation. The caller must call Cleanup after the
// agent finishes to remove temp files.
func RecipeTools() ([]Tool, func()) {
	tmpDir, err := os.MkdirTemp("", "gale-recipe-*")
	if err != nil {
		tmpDir = os.TempDir()
	}
	cleanup := func() { os.RemoveAll(tmpDir) }

	return []Tool{
		githubInfoTool(),
		downloadAndHashTool(tmpDir),
		readFileTool(),
		writeRecipeTool(tmpDir),
		lintRecipeTool(),
	}, cleanup
}

// RecipeTmpDir returns the temp directory used for
// recipe files, extracted from the tools list.
// The write_recipe tool stores files here.
func RecipeTmpDir(tools []Tool) string {
	// Convention: write_recipe tool stores the dir.
	// Caller should track this separately.
	return ""
}

func githubInfoTool() Tool {
	return Tool{
		Param: anthropic.ToolParam{
			Name:        "github_info",
			Description: anthropic.String("Fetch GitHub repository metadata: description, license, homepage, latest release tag and tarball URL."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"repo": map[string]any{
						"type":        "string",
						"description": "GitHub repo in owner/repo format",
					},
				},
				Required: []string{"repo"},
			},
		},
		Handler: func(input json.RawMessage) (string, error) {
			var args struct {
				Repo string `json:"repo"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("parse input: %w", err)
			}
			return fetchGitHubInfo(args.Repo)
		},
	}
}

func downloadAndHashTool(tmpDir string) Tool {
	return Tool{
		Param: anthropic.ToolParam{
			Name:        "download_and_hash",
			Description: anthropic.String("Download a file from a URL and compute its SHA256 hash. Returns the hash and local file path."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"url": map[string]any{
						"type":        "string",
						"description": "URL to download",
					},
				},
				Required: []string{"url"},
			},
		},
		Handler: func(input json.RawMessage) (string, error) {
			var args struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("parse input: %w", err)
			}

			// Extract filename from URL.
			name := filepath.Base(args.URL)
			destPath := filepath.Join(tmpDir, name)

			if err := download.Fetch(args.URL, destPath); err != nil {
				return "", fmt.Errorf("download: %w", err)
			}

			hash, err := download.HashFile(destPath)
			if err != nil {
				return "", fmt.Errorf("hash: %w", err)
			}

			result, _ := json.Marshal(map[string]string{
				"sha256": hash,
				"path":   destPath,
			})
			return string(result), nil
		},
	}
}

func readFileTool() Tool {
	return Tool{
		Param: anthropic.ToolParam{
			Name:        "read_file",
			Description: anthropic.String("Read a file from a GitHub repository. Returns file contents (truncated to 10KB). Use to detect build system by reading configure.ac, CMakeLists.txt, Cargo.toml, go.mod, Makefile, etc."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"repo": map[string]any{
						"type":        "string",
						"description": "GitHub repo in owner/repo format",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "File path within the repo",
					},
				},
				Required: []string{"repo", "path"},
			},
		},
		Handler: func(input json.RawMessage) (string, error) {
			var args struct {
				Repo string `json:"repo"`
				Path string `json:"path"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("parse input: %w", err)
			}

			url := fmt.Sprintf(
				"https://raw.githubusercontent.com/%s/HEAD/%s",
				args.Repo, args.Path)

			client := &http.Client{Timeout: 15 * time.Second}
			resp, err := client.Get(url) //nolint:gosec
			if err != nil {
				return "", fmt.Errorf("fetch: %w", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusNotFound {
				return "", fmt.Errorf("file not found: %s", args.Path)
			}
			if resp.StatusCode != http.StatusOK {
				return "", fmt.Errorf("HTTP %d", resp.StatusCode)
			}

			// Truncate to 10KB.
			data, err := io.ReadAll(io.LimitReader(resp.Body, 10240))
			if err != nil {
				return "", fmt.Errorf("read: %w", err)
			}

			return string(data), nil
		},
	}
}

func writeRecipeTool(tmpDir string) Tool {
	return Tool{
		Param: anthropic.ToolParam{
			Name:        "write_recipe",
			Description: anthropic.String("Write a recipe TOML file. Returns the file path. Use letter-bucketed naming: the file is saved as <tmpdir>/<first-letter>/<name>.toml."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "Package name (e.g., jq)",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "Full recipe TOML content",
					},
				},
				Required: []string{"name", "content"},
			},
		},
		Handler: func(input json.RawMessage) (string, error) {
			var args struct {
				Name    string `json:"name"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("parse input: %w", err)
			}

			letter := string(args.Name[0])
			dir := filepath.Join(tmpDir, letter)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", fmt.Errorf("mkdir: %w", err)
			}

			path := filepath.Join(dir, args.Name+".toml")
			if err := os.WriteFile(path, []byte(args.Content), 0o644); err != nil { //nolint:gosec
				return "", fmt.Errorf("write: %w", err)
			}

			result, _ := json.Marshal(map[string]string{
				"path": path,
			})
			return string(result), nil
		},
	}
}

func lintRecipeTool() Tool {
	return Tool{
		Param: anthropic.ToolParam{
			Name:        "lint_recipe",
			Description: anthropic.String("Validate a recipe TOML file. Returns a list of issues (errors and warnings)."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path to the recipe TOML file",
					},
				},
				Required: []string{"path"},
			},
		},
		Handler: func(input json.RawMessage) (string, error) {
			var args struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("parse input: %w", err)
			}

			data, err := os.ReadFile(args.Path)
			if err != nil {
				return "", fmt.Errorf("read recipe: %w", err)
			}

			issues := lint.Lint(string(data), args.Path)

			result, _ := json.Marshal(issues)
			return string(result), nil
		},
	}
}

// fetchGitHubInfo queries the GitHub API for repo
// metadata and latest release.
func fetchGitHubInfo(repo string) (string, error) {
	client := &http.Client{Timeout: 15 * time.Second}

	// Fetch repo info.
	repoURL := fmt.Sprintf(
		"https://api.github.com/repos/%s", repo)
	repoResp, err := client.Get(repoURL)
	if err != nil {
		return "", fmt.Errorf("fetch repo: %w", err)
	}
	defer repoResp.Body.Close()

	if repoResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API: HTTP %d", repoResp.StatusCode)
	}

	var repoData struct {
		Description string `json:"description"`
		License     struct {
			SPDXID string `json:"spdx_id"`
		} `json:"license"`
		Homepage string `json:"homepage"`
		HTMLURL  string `json:"html_url"`
	}
	if err := json.NewDecoder(repoResp.Body).Decode(&repoData); err != nil {
		return "", fmt.Errorf("parse repo: %w", err)
	}

	// Fetch latest release.
	releaseURL := fmt.Sprintf(
		"https://api.github.com/repos/%s/releases/latest", repo)
	releaseResp, err := client.Get(releaseURL)
	if err != nil {
		return "", fmt.Errorf("fetch release: %w", err)
	}
	defer releaseResp.Body.Close()

	info := map[string]string{
		"description": repoData.Description,
		"license":     repoData.License.SPDXID,
		"homepage":    repoData.Homepage,
		"html_url":    repoData.HTMLURL,
		"repo":        repo,
	}

	if releaseResp.StatusCode == http.StatusOK {
		var releaseData struct {
			TagName    string `json:"tag_name"`
			TarballURL string `json:"tarball_url"`
		}
		if err := json.NewDecoder(releaseResp.Body).Decode(&releaseData); err == nil {
			info["latest_tag"] = releaseData.TagName
			info["tarball_url"] = releaseData.TarballURL
		}
	}

	result, _ := json.Marshal(info)
	return string(result), nil
}
