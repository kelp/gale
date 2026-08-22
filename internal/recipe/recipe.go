package recipe

import (
	"fmt"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// Recipe represents a parsed recipe TOML file.
type Recipe struct {
	Package      Package
	Source       Source
	Build        Build `toml:"-"`
	Dependencies Dependencies

	// Digest is SHA-256 of the TOML bytes this recipe was
	// parsed from. Empty when the recipe was not loaded from
	// a file (registry cache, in-memory tests).
	Digest string `toml:"-"`
	// FromWorkingTree is true when the recipe was loaded from
	// a local file (--recipe / --recipes), not the registry.
	FromWorkingTree bool `toml:"-"`
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

// SupportsPlatform reports whether the package may be installed on
// the given "<goos>-<goarch>" key. An empty Platforms list means no
// restriction. The platform is a parameter rather than the host's own
// because the lock writers reason about platforms they are not
// running on.
func (p Package) SupportsPlatform(key string) bool {
	if len(p.Platforms) == 0 {
		return true
	}
	return slices.Contains(p.Platforms, key)
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
	Build       []string
	Runtime     []string
	Constraints map[string]string // name → version constraint
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
		// Catch typos in [package] and [source] — those are
		// strict-schema tables. Leftover [binary] TOML is
		// ignored. Everything else (build/dependencies
		// sub-tables decoded into interface{} maps; recipe-repo
		// extensions like [smoke]) legitimately ends up
		// undecoded and must not fail parsing.
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
				strings.Join(bad, ", "),
			)
		}
	}

	deps, err := parseDependencies(raw.Dependencies)
	if err != nil {
		return nil, err
	}

	r := &Recipe{
		Package:      raw.Package,
		Source:       raw.Source,
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
// present, otherwise the default lists are used. Per-platform
// constraints are merged into the returned Constraints map,
// overriding any global constraint for the same dep name.
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
			if len(pd.Constraints) > 0 {
				merged := make(map[string]string, len(deps.Constraints)+len(pd.Constraints))
				for k, v := range deps.Constraints {
					merged[k] = v
				}
				for k, v := range pd.Constraints {
					merged[k] = v
				}
				deps.Constraints = merged
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

// parseDepList parses a raw dep array into a name slice and a
// constraints map. constraints is the caller's existing map and is
// returned (possibly newly allocated) with any new entries appended.
func parseDepList(
	raw interface{},
	constraints map[string]string,
) (names []string, out map[string]string, err error) {
	arr, ok := raw.([]interface{})
	if !ok {
		return nil, constraints, nil
	}
	for _, v := range arr {
		name, constraint, err := parseDep(v)
		if err != nil {
			return nil, nil, err
		}
		names = append(names, name)
		if constraint != "" {
			if constraints == nil {
				constraints = make(map[string]string)
			}
			constraints[name] = constraint
		}
	}
	return names, constraints, nil
}

func parseDependencies(raw map[string]interface{}) (Dependencies, error) {
	if raw == nil {
		return Dependencies{}, nil
	}
	var deps Dependencies
	var err error
	if deps.Build, deps.Constraints, err = parseDepList(raw["build"], nil); err != nil {
		return Dependencies{}, err
	}
	if deps.Runtime, deps.Constraints, err = parseDepList(raw["runtime"], deps.Constraints); err != nil {
		return Dependencies{}, err
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
		var pd PlatformDependencies
		if pd.Build, pd.Constraints, err = parseDepList(sub["build"], nil); err != nil {
			return Dependencies{}, err
		}
		if pd.Runtime, pd.Constraints, err = parseDepList(sub["runtime"], pd.Constraints); err != nil {
			return Dependencies{}, err
		}
		if deps.Platform == nil {
			deps.Platform = make(map[string]PlatformDependencies)
		}
		deps.Platform[key] = pd
	}
	return deps, nil
}

// stringVal returns the string value for key in m, or "" if the
// key is absent or the value is not a string.
func stringVal(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// toStringSlice converts a raw []interface{} to []string.
// Non-string elements are silently dropped (intentional: wrong-typed
// build steps are a recipe authoring error that lint catches; silent
// drops keep parse tolerant for forward-compat unknown array types).
func toStringSlice(raw interface{}) []string {
	arr, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	var out []string
	for _, v := range arr {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// toStringStringMap converts a raw map[string]interface{} to
// map[string]string. Non-string values are silently dropped
// (same rationale as toStringSlice).
func toStringStringMap(raw interface{}) map[string]string {
	m, ok := raw.(map[string]interface{})
	if !ok {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// parseBuild extracts Build and per-platform overrides
// from the raw TOML map.
func parseBuild(raw map[string]interface{}) (Build, error) {
	if raw == nil {
		return Build{}, nil
	}
	b := Build{
		Steps: toStringSlice(raw["steps"]),
		Env:   toStringStringMap(raw["env"]),
	}
	b.System = stringVal(raw, "system")
	b.Toolchain = stringVal(raw, "toolchain")
	if dbg, ok := raw["debug"]; ok {
		if d, ok := dbg.(bool); ok {
			b.Debug = d
		}
	}
	// Extract per-platform overrides (sub-tables). Skip every known
	// scalar key explicitly so a string-typed key (e.g. toolchain)
	// is skipped by name rather than by failing the map assertion.
	for key, val := range raw {
		if key == "steps" || key == "env" || key == "system" ||
			key == "debug" || key == "toolchain" {
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
		pb := PlatformBuild{
			Steps: toStringSlice(sub["steps"]),
			Env:   toStringStringMap(sub["env"]),
		}
		pb.Toolchain = stringVal(sub, "toolchain")
		if b.Platform == nil {
			b.Platform = make(map[string]PlatformBuild)
		}
		b.Platform[key] = pb
	}
	return b, nil
}
