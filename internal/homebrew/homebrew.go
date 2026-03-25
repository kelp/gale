package homebrew

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Formula holds metadata fetched from Homebrew's API.
type Formula struct {
	Name        string
	Version     string
	Description string
	License     string
	Homepage    string
	SourceURL   string
	RuntimeDeps []string
	BuildDeps   []string
}

// FetchFormula fetches formula metadata from Homebrew's API.
// baseURL overrides the default https://formulae.brew.sh.
func FetchFormula(name, baseURL string) (*Formula, error) {
	url := fmt.Sprintf("%s/api/formula/%s.json", baseURL, name)

	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch formula %s: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch formula %s: HTTP %d", name, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response for %s: %w", name, err)
	}

	var raw struct {
		Name     string `json:"name"`
		Desc     string `json:"desc"`
		License  string `json:"license"`
		Homepage string `json:"homepage"`
		Versions struct {
			Stable string `json:"stable"`
		} `json:"versions"`
		URLs struct {
			Stable struct {
				URL string `json:"url"`
			} `json:"stable"`
		} `json:"urls"`
		Dependencies      []string `json:"dependencies"`
		BuildDependencies []string `json:"build_dependencies"`
	}

	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse formula %s: %w", name, err)
	}

	return &Formula{
		Name:        raw.Name,
		Version:     raw.Versions.Stable,
		Description: raw.Desc,
		License:     raw.License,
		Homepage:    raw.Homepage,
		SourceURL:   raw.URLs.Stable.URL,
		RuntimeDeps: raw.Dependencies,
		BuildDeps:   raw.BuildDependencies,
	}, nil
}

// ToRecipeTOML generates a gale recipe TOML string.
func (f *Formula) ToRecipeTOML() string {
	var b strings.Builder

	fmt.Fprintf(&b, "[package]\n")
	fmt.Fprintf(&b, "name = %q\n", f.Name)
	fmt.Fprintf(&b, "version = %q\n", f.Version)
	fmt.Fprintf(&b, "description = %q\n", f.Description)
	fmt.Fprintf(&b, "license = %q\n", f.License)
	fmt.Fprintf(&b, "homepage = %q\n", f.Homepage)

	fmt.Fprintf(&b, "\n[source]\n")
	fmt.Fprintf(&b, "url = %q\n", f.SourceURL)
	fmt.Fprintf(&b, "sha256 = \"\"\n")

	if len(f.BuildDeps) > 0 || len(f.RuntimeDeps) > 0 {
		fmt.Fprintf(&b, "\n[dependencies]\n")
		if len(f.BuildDeps) > 0 {
			fmt.Fprintf(&b, "build = %s\n", toTOMLArray(f.BuildDeps))
		}
		if len(f.RuntimeDeps) > 0 {
			fmt.Fprintf(&b, "runtime = %s\n", toTOMLArray(f.RuntimeDeps))
		}
	}

	return b.String()
}

func toTOMLArray(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = fmt.Sprintf("%q", s)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
