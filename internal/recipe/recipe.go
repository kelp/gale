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
	Dependencies Dependencies
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
type Source struct {
	URL    string `toml:"url"`
	SHA256 string `toml:"sha256"`
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
