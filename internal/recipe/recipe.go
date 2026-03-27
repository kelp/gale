package recipe

import (
	"fmt"

	"github.com/BurntSushi/toml"
)

// Recipe represents a parsed recipe TOML file.
type Recipe struct {
	Package      Package
	Source       Source
	Build        Build
	Binary       map[string]Binary `toml:"binary"`
	Dependencies Dependencies
}

// Binary holds a prebuilt archive location for a platform.
type Binary struct {
	URL    string `toml:"url"`
	SHA256 string `toml:"sha256"`
}

// BinaryForPlatform returns the binary for the given OS and
// architecture, or nil if none exists. Keys are "GOOS-GOARCH".
func (r *Recipe) BinaryForPlatform(goos, goarch string) *Binary {
	if r.Binary == nil {
		return nil
	}
	key := goos + "-" + goarch
	b, ok := r.Binary[key]
	if !ok {
		return nil
	}
	return &b
}

// Package holds the package metadata.
type Package struct {
	Name        string
	Version     string
	Description string
	License     string
	Homepage    string
}

// Source holds the source archive location and checksum.
// Repo and ReleasedAt are optional — used by the
// auto-update agent to track upstream releases.
type Source struct {
	URL        string `toml:"url"`
	SHA256     string `toml:"sha256"`
	Repo       string `toml:"repo"`
	ReleasedAt string `toml:"released_at"`
}

// Build holds the build system and steps.
type Build struct {
	System string
	Steps  []string
}

// Dependencies holds build-time and runtime dependency lists.
type Dependencies struct {
	Build   []string
	Runtime []string
}

// Parse parses a TOML recipe string and validates it.
func Parse(data string) (*Recipe, error) {
	var r Recipe
	if err := toml.Unmarshal([]byte(data), &r); err != nil {
		return nil, fmt.Errorf("invalid TOML: %w", err)
	}
	if r.Package.Name == "" {
		return nil, fmt.Errorf("missing required field: package.name")
	}
	if r.Package.Version == "" {
		return nil, fmt.Errorf("missing required field: package.version")
	}
	if r.Source.URL == "" {
		return nil, fmt.Errorf("missing required field: source.url")
	}
	if r.Source.SHA256 == "" {
		return nil, fmt.Errorf("missing required field: source.sha256")
	}
	return &r, nil
}
