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
	Revision    int      `toml:"revision,omitempty"`
}

// Full returns the full version string including revision,
// formatted as "<version>-<revision>". Revision defaults to 1.
func (p Package) Full() string {
	rev := p.Revision
	if rev <= 0 {
		rev = 1
	}
	return fmt.Sprintf("%s-%d", p.Version, rev)
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
	System    string
	Steps     []string
	Debug     bool `toml:"debug,omitempty"`
	Env       map[string]string
	Toolchain string                   `toml:"toolchain,omitempty"`
	Platform  map[string]PlatformBuild `toml:"-"`
}

// PlatformBuild holds per-platform build overrides.
type PlatformBuild struct {
	Steps     []string
	Env       map[string]string
	Toolchain string `toml:"toolchain,omitempty"`
}

// BuildForPlatform returns the build config for the given
// platform. If a per-platform override exists, it is
// returned. Otherwise the default Build is returned.
// Per-platform Env overrides the default Env when present.
func (r *Recipe) BuildForPlatform(goos, goarch string) Build {
	key := goos + "-" + goarch
	if r.Build.Platform != nil {
		if pb, ok := r.Build.Platform[key]; ok {
			env := r.Build.Env
			if pb.Env != nil {
				env = pb.Env
			}
			steps := r.Build.Steps
			if pb.Steps != nil {
				steps = pb.Steps
			}
			toolchain := r.Build.Toolchain
			if pb.Toolchain != "" {
				toolchain = pb.Toolchain
			}
			return Build{
				System:    r.Build.System,
				Steps:     steps,
				Env:       env,
				Toolchain: toolchain,
			}
		}
	}
	return r.Build
}

// PlatformDependencies holds per-platform dependency overrides.
type PlatformDependencies struct {
	Build   []string
	Runtime []string
}

// Dependencies holds build-time and runtime dependency lists.
type Dependencies struct {
	Build       []string
	Runtime     []string
	Constraints map[string]string               `toml:"-"` // name → raw constraint
	Platform    map[string]PlatformDependencies `toml:"-"`
}

// rawRecipe is used for initial TOML decoding before
// extracting per-platform build overrides.
type rawRecipe struct {
	Package      Package
	Source       Source
	Build        map[string]interface{} `toml:"build"`
	Binary       map[string]Binary      `toml:"binary"`
	Dependencies map[string]interface{} `toml:"dependencies"`
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
	md, err := toml.Decode(data, &raw)
	if err != nil {
		return nil, fmt.Errorf("invalid TOML: %w", err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		// M1: catch typos in [package] and [source] —
		// those are strict-schema tables. Everything else
		// (build/dependencies sub-tables decoded into
		// interface{} maps; recipe-repo extensions like
		// [smoke]) legitimately ends up undecoded and must
		// not fail parsing.
		var bad []string
		for _, key := range undecoded {
			if len(key) < 2 {
				continue
			}
			head := key[0]
			if head == "package" || head == "source" {
				bad = append(bad, key.String())
			}
		}
		if len(bad) > 0 {
			return nil, fmt.Errorf(
				"unknown recipe field(s): %s",
				strings.Join(bad, ", "))
		}
	}

	deps, err := parseDependencies(raw.Dependencies)
	if err != nil {
		return nil, err
	}

	r := &Recipe{
		Package:      raw.Package,
		Source:       raw.Source,
		Binary:       raw.Binary,
		Dependencies: deps,
	}

	b, err := parseBuild(raw.Build)
	if err != nil {
		return nil, err
	}
	r.Build = b

	if r.Package.Revision < 0 {
		return nil, fmt.Errorf("invalid revision %d: revision must be >= 0", r.Package.Revision)
	}
	if r.Package.Revision == 0 {
		r.Package.Revision = 1
	}
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

// DependenciesForPlatform returns the dependencies for the given
// platform. Per-platform lists override the default lists when
// present, otherwise the default lists are used.
func (r *Recipe) DependenciesForPlatform(goos, goarch string) Dependencies {
	deps := r.Dependencies
	key := goos + "-" + goarch
	if deps.Platform != nil {
		if pd, ok := deps.Platform[key]; ok {
			if pd.Build != nil {
				deps.Build = pd.Build
			}
			if pd.Runtime != nil {
				deps.Runtime = pd.Runtime
			}
		}
	}
	return deps
}

// parseDep extracts a dep name and optional version constraint from a
// single element in a build/runtime list. The element may be either a
// bare string or an inline table with at least a "name" key.
// It returns the name, an optional constraint (empty string means none),
// and an error if the element is malformed.
func parseDep(v interface{}) (name, constraint string, err error) {
	switch val := v.(type) {
	case string:
		return val, "", nil
	case map[string]interface{}:
		nameRaw, ok := val["name"]
		if !ok {
			return "", "", fmt.Errorf("dependency table missing \"name\" field")
		}
		nameStr, ok := nameRaw.(string)
		if !ok {
			return "", "", fmt.Errorf("dependency table \"name\" must be a string")
		}
		if nameStr == "" {
			return "", "", fmt.Errorf("dependency table \"name\" must not be empty")
		}
		var versionStr string
		if versionRaw, hasVersion := val["version"]; hasVersion {
			versionStr, ok = versionRaw.(string)
			if !ok {
				return "", "", fmt.Errorf("dependency table \"version\" must be a string")
			}
		}
		return nameStr, versionStr, nil
	default:
		return "", "", fmt.Errorf("dependency entry must be a string or inline table")
	}
}

func parseDependencies(raw map[string]interface{}) (Dependencies, error) {
	deps := Dependencies{}
	if raw == nil {
		return deps, nil
	}
	if buildRaw, ok := raw["build"]; ok {
		if arr, ok := buildRaw.([]interface{}); ok {
			for _, v := range arr {
				name, constraint, err := parseDep(v)
				if err != nil {
					return Dependencies{}, err
				}
				deps.Build = append(deps.Build, name)
				if constraint != "" {
					if deps.Constraints == nil {
						deps.Constraints = make(map[string]string)
					}
					deps.Constraints[name] = constraint
				}
			}
		}
	}
	if runtimeRaw, ok := raw["runtime"]; ok {
		if arr, ok := runtimeRaw.([]interface{}); ok {
			for _, v := range arr {
				name, constraint, err := parseDep(v)
				if err != nil {
					return Dependencies{}, err
				}
				deps.Runtime = append(deps.Runtime, name)
				if constraint != "" {
					if deps.Constraints == nil {
						deps.Constraints = make(map[string]string)
					}
					deps.Constraints[name] = constraint
				}
			}
		}
	}
	for key, val := range raw {
		if key == "build" || key == "runtime" {
			continue
		}
		sub, ok := val.(map[string]interface{})
		if !ok {
			continue
		}
		if !validPlatformKey(key) {
			return Dependencies{}, fmt.Errorf(
				"unrecognized dependencies key %q: expected platform in os-arch format",
				key,
			)
		}
		pd := PlatformDependencies{}
		if buildRaw, ok := sub["build"]; ok {
			if arr, ok := buildRaw.([]interface{}); ok {
				for _, v := range arr {
					name, constraint, err := parseDep(v)
					if err != nil {
						return Dependencies{}, err
					}
					pd.Build = append(pd.Build, name)
					if constraint != "" {
						if deps.Constraints == nil {
							deps.Constraints = make(map[string]string)
						}
						deps.Constraints[name] = constraint
					}
				}
			}
		}
		if runtimeRaw, ok := sub["runtime"]; ok {
			if arr, ok := runtimeRaw.([]interface{}); ok {
				for _, v := range arr {
					name, constraint, err := parseDep(v)
					if err != nil {
						return Dependencies{}, err
					}
					pd.Runtime = append(pd.Runtime, name)
					if constraint != "" {
						if deps.Constraints == nil {
							deps.Constraints = make(map[string]string)
						}
						deps.Constraints[name] = constraint
					}
				}
			}
		}
		if deps.Platform == nil {
			deps.Platform = make(map[string]PlatformDependencies)
		}
		deps.Platform[key] = pd
	}
	return deps, nil
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

	// Extract top-level env.
	if envRaw, ok := raw["env"]; ok {
		if m, ok := envRaw.(map[string]interface{}); ok {
			b.Env = make(map[string]string, len(m))
			for k, v := range m {
				if s, ok := v.(string); ok {
					b.Env[k] = s
				}
			}
		}
	}

	// Extract top-level toolchain.
	if toolchain, ok := raw["toolchain"]; ok {
		if s, ok := toolchain.(string); ok {
			b.Toolchain = s
		}
	}

	// Extract per-platform overrides (sub-tables).
	for key, val := range raw {
		if key == "steps" || key == "system" || key == "debug" || key == "env" {
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
		if envRaw, ok := sub["env"]; ok {
			if m, ok := envRaw.(map[string]interface{}); ok {
				pb.Env = make(map[string]string, len(m))
				for k, v := range m {
					if s, ok := v.(string); ok {
						pb.Env[k] = s
					}
				}
			}
		}
		if toolchain, ok := sub["toolchain"]; ok {
			if s, ok := toolchain.(string); ok {
				pb.Toolchain = s
			}
		}
		if b.Platform == nil {
			b.Platform = make(map[string]PlatformBuild)
		}
		b.Platform[key] = pb
	}

	return b, nil
}
