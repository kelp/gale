package recipe

import (
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
)

// Recipe represents a parsed recipe TOML file.
type Recipe struct {
	Package      Package
	Source       Source
	Build        Build             `toml:"-"`
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
	Platforms   []string `toml:"platforms,omitempty"`
	Verify      string   `toml:"verify,omitempty"`
}

// Source holds the source archive location and checksum.
// Repo and ReleasedAt are optional — used by the
// auto-update agent to track upstream releases.
// Branch is optional — used for git clone builds
// (defaults to the repo's default branch).
type Source struct {
	URL        string `toml:"url"`
	SHA256     string `toml:"sha256"`
	Repo       string `toml:"repo"`
	ReleasedAt string `toml:"released_at"`
	Branch     string `toml:"branch"`
}

// Build holds the build system and steps.
type Build struct {
	System   string
	Steps    []string
	Debug    bool                     `toml:"debug,omitempty"`
	Platform map[string]PlatformBuild `toml:"-"`
}

// PlatformBuild holds per-platform build overrides.
type PlatformBuild struct {
	Steps []string
}

// BuildForPlatform returns the build config for the given
// platform. If a per-platform override exists, it is
// returned. Otherwise the default Build is returned.
func (r *Recipe) BuildForPlatform(goos, goarch string) Build {
	key := goos + "-" + goarch
	if r.Build.Platform != nil {
		if pb, ok := r.Build.Platform[key]; ok {
			return Build{
				System: r.Build.System,
				Steps:  pb.Steps,
			}
		}
	}
	return r.Build
}

// Dependencies holds build-time and runtime dependency lists.
type Dependencies struct {
	Build   []string
	Runtime []string
}

// rawRecipe is used for initial TOML decoding before
// extracting per-platform build overrides.
type rawRecipe struct {
	Package      Package
	Source       Source
	Build        map[string]interface{} `toml:"build"`
	Binary       map[string]Binary      `toml:"binary"`
	Dependencies Dependencies
}

// Parse parses a TOML recipe string and validates all
// required fields including source.url and source.sha256.
func Parse(data string) (*Recipe, error) { return parse(data, true) }

// ParseLocal parses a TOML recipe for local-source builds.
// Skips source.url and source.sha256 validation since the
// source is provided locally.
func ParseLocal(data string) (*Recipe, error) { return parse(data, false) }

func parse(data string, requireSource bool) (*Recipe, error) {
	var raw rawRecipe
	if err := toml.Unmarshal([]byte(data), &raw); err != nil {
		return nil, fmt.Errorf("invalid TOML: %w", err)
	}

	r := &Recipe{
		Package:      raw.Package,
		Source:       raw.Source,
		Binary:       raw.Binary,
		Dependencies: raw.Dependencies,
	}

	b, err := parseBuild(raw.Build)
	if err != nil {
		return nil, err
	}
	r.Build = b

	if r.Package.Name == "" {
		return nil, fmt.Errorf("missing required field: package.name")
	}
	if r.Package.Version == "" {
		return nil, fmt.Errorf("missing required field: package.version")
	}
	if requireSource {
		if r.Source.URL == "" {
			return nil, fmt.Errorf("missing required field: source.url")
		}
		if r.Source.SHA256 == "" {
			return nil, fmt.Errorf("missing required field: source.sha256")
		}
	}
	return r, nil
}

// validPlatformKey checks whether a key looks like a
// valid GOOS-GOARCH platform string (two hyphen-separated
// lowercase-alphanumeric parts).
func validPlatformKey(key string) bool {
	parts := strings.SplitN(key, "-", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	for _, p := range parts {
		for _, c := range p {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
				return false
			}
		}
	}
	return true
}

// parseBuild extracts Build and per-platform overrides
// from the raw TOML map.
func parseBuild(raw map[string]interface{}) (Build, error) {
	b := Build{}
	if raw == nil {
		return b, nil
	}

	// Extract top-level steps.
	if steps, ok := raw["steps"]; ok {
		if arr, ok := steps.([]interface{}); ok {
			for _, v := range arr {
				if s, ok := v.(string); ok {
					b.Steps = append(b.Steps, s)
				}
			}
		}
	}

	// Extract system.
	if sys, ok := raw["system"]; ok {
		if s, ok := sys.(string); ok {
			b.System = s
		}
	}

	// Extract debug.
	if dbg, ok := raw["debug"]; ok {
		if d, ok := dbg.(bool); ok {
			b.Debug = d
		}
	}

	// Extract per-platform overrides (sub-tables).
	for key, val := range raw {
		if key == "steps" || key == "system" || key == "debug" {
			continue
		}
		sub, ok := val.(map[string]interface{})
		if !ok {
			continue
		}
		if !validPlatformKey(key) {
			return Build{}, fmt.Errorf(
				"unrecognized build key %q: expected platform in os-arch format",
				key,
			)
		}
		pb := PlatformBuild{}
		if steps, ok := sub["steps"]; ok {
			if arr, ok := steps.([]interface{}); ok {
				for _, v := range arr {
					if s, ok := v.(string); ok {
						pb.Steps = append(pb.Steps, s)
					}
				}
			}
		}
		if b.Platform == nil {
			b.Platform = make(map[string]PlatformBuild)
		}
		b.Platform[key] = pb
	}

	return b, nil
}
