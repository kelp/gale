package registry

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/kelp/gale/internal/recipe"
)

const DefaultURL = "https://raw.githubusercontent.com/" +
	"kelp/gale-recipes/main"

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

	return rec, nil
}
