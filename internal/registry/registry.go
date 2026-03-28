package registry

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kelp/gale/internal/recipe"
)

const DefaultURL = "https://raw.githubusercontent.com/" +
	"kelp/gale-recipes/main"

const defaultGHCRBase = "kelp/gale-recipes"

// Registry fetches recipe TOML files from a remote HTTP
// registry using letter-bucketed paths.
type Registry struct {
	BaseURL string
}

// New returns a Registry configured with DefaultURL.
func New() *Registry {
	return &Registry{BaseURL: DefaultURL}
}

// NewWithURL returns a Registry with the given base URL.
// If url is empty, DefaultURL is used.
func NewWithURL(url string) *Registry {
	if url == "" {
		return New()
	}
	return &Registry{BaseURL: url}
}

// FetchRecipe downloads and parses the recipe for the named
// package from the registry.
func (r *Registry) FetchRecipe(name string) (*recipe.Recipe, error) {
	if name == "" {
		return nil, fmt.Errorf("fetch recipe: name must not be empty")
	}

	bucket := string(name[0])
	url := fmt.Sprintf("%s/recipes/%s/%s.toml",
		r.BaseURL, bucket, name)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch recipe %s: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch recipe %s: HTTP %d",
			name, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("fetch recipe %s: %w", name, err)
	}

	rec, err := recipe.Parse(string(body))
	if err != nil {
		return nil, fmt.Errorf("fetch recipe %s: %w", name, err)
	}

	// If the recipe has no inline binary entries, try to
	// fetch a separate .binaries.toml file.
	if len(rec.Binary) == 0 {
		idx, _ := r.fetchBinaries(name)
		if idx != nil {
			base := ghcrBaseFromURL(r.BaseURL)
			recipe.MergeBinaries(rec, idx, base)
		}
	}

	return rec, nil
}

// FetchRecipeVersion fetches a recipe at a specific version
// by looking up the commit hash in the .versions index, then
// fetching the recipe at that commit.
func (r *Registry) FetchRecipeVersion(name, version string) (*recipe.Recipe, error) {
	if name == "" {
		return nil, fmt.Errorf("name must not be empty")
	}

	// Fetch the versions index.
	bucket := string(name[0])
	indexURL := fmt.Sprintf("%s/recipes/%s/%s.versions",
		r.BaseURL, bucket, name)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(indexURL)
	if err != nil {
		return nil, fmt.Errorf(
			"fetch version index for %s: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"version index for %s: HTTP %d", name, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf(
			"read version index for %s: %w", name, err)
	}

	idx, err := parseVersionIndex(string(body))
	if err != nil {
		return nil, fmt.Errorf(
			"parse version index for %s: %w", name, err)
	}

	commit, ok := idx[version]
	if !ok {
		return nil, fmt.Errorf(
			"%s@%s: version not found in registry", name, version)
	}

	// Fetch recipe at the specific commit.
	recipeURL := fmt.Sprintf("%s/%s/recipes/%s/%s.toml",
		r.BaseURL, commit, bucket, name)

	resp2, err := client.Get(recipeURL)
	if err != nil {
		return nil, fmt.Errorf(
			"fetch %s@%s recipe: %w", name, version, err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"fetch %s@%s recipe: HTTP %d",
			name, version, resp2.StatusCode)
	}

	recipeBody, err := io.ReadAll(resp2.Body)
	if err != nil {
		return nil, fmt.Errorf(
			"read %s@%s recipe: %w", name, version, err)
	}

	rec, err := recipe.Parse(string(recipeBody))
	if err != nil {
		return nil, fmt.Errorf(
			"parse %s@%s recipe: %w", name, version, err)
	}

	return rec, nil
}

// fetchBinaries fetches the .binaries.toml file for a
// package. Returns nil (not error) if the file is not found.
func (r *Registry) fetchBinaries(name string) (*recipe.BinaryIndex, error) {
	bucket := string(name[0])
	url := fmt.Sprintf("%s/recipes/%s/%s.binaries.toml",
		r.BaseURL, bucket, name)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, nil //nolint:nilerr // network error is not fatal
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"fetch binaries %s: HTTP %d", name, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf(
			"read binaries %s: %w", name, err)
	}

	return recipe.ParseBinaryIndex(string(body))
}

// ghcrBaseFromURL extracts the "owner/repo" from a
// raw.githubusercontent.com URL. Falls back to the default
// GHCR base if the URL doesn't match the expected pattern.
func ghcrBaseFromURL(rawURL string) string {
	const prefix = "https://raw.githubusercontent.com/"
	if !strings.HasPrefix(rawURL, prefix) {
		return defaultGHCRBase
	}
	path := strings.TrimPrefix(rawURL, prefix)
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 2 {
		return defaultGHCRBase
	}
	return parts[0] + "/" + parts[1]
}

// parseVersionIndex parses a .versions file into a
// version→commit map. Each line is "version commit-hash".
func parseVersionIndex(data string) (map[string]string, error) {
	idx := make(map[string]string)
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			return nil, fmt.Errorf(
				"malformed version line: %q", line)
		}
		idx[parts[0]] = parts[1]
	}
	return idx, nil
}
