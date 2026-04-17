package installer

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"

	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/recipe"
)

const depsMetadataFile = ".gale-deps.toml"

// ResolvedDep records one dep's full identity at install
// time. Persisted in <storeDir>/.gale-deps.toml.
type ResolvedDep struct {
	Name     string `toml:"name"`
	Version  string `toml:"version"`
	Revision int    `toml:"revision"`
}

// DepsMetadata is the on-disk form of a package's
// built-against dep closure.
type DepsMetadata struct {
	Deps []ResolvedDep `toml:"deps"`
}

// WriteDepsMetadata writes the metadata file into storeDir.
// Overwrites any existing file.
func WriteDepsMetadata(storeDir string, md DepsMetadata) error {
	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	if err := enc.Encode(md); err != nil {
		return fmt.Errorf("encoding deps metadata: %w", err)
	}
	path := filepath.Join(storeDir, depsMetadataFile)
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("writing deps metadata: %w", err)
	}
	return nil
}

// ReadDepsMetadata reads <storeDir>/.gale-deps.toml.
// Returns an empty DepsMetadata (no error) if the file
// does not exist. Returns an error if the file exists
// but fails to parse.
func ReadDepsMetadata(storeDir string) (DepsMetadata, error) {
	path := filepath.Join(storeDir, depsMetadataFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DepsMetadata{}, nil
		}
		return DepsMetadata{}, fmt.Errorf("reading deps metadata: %w", err)
	}
	var md DepsMetadata
	if _, err := toml.Decode(string(data), &md); err != nil {
		return DepsMetadata{}, fmt.Errorf("parsing deps metadata: %w", err)
	}
	return md, nil
}

// IsStale reports whether an installed package is stale
// relative to the current recipes of its declared
// dependencies. Stale means the package was built against
// a dep version-revision that differs from what the
// current recipe for that dep produces.
//
// A missing .gale-deps.toml causes IsStale to return
// true (stale) so a soft migration reinstalls old
// installs that predate this metadata.
//
// Only Runtime and Build deps declared on `r` are
// considered — external system libraries are ignored.
func IsStale(storeDir string, r *recipe.Recipe, resolver RecipeResolver) (bool, error) {
	// Check whether the metadata file is present before reading it.
	// A missing file means the package predates this metadata (soft
	// migration → stale). A present file with zero deps is a valid
	// zero-dep install (not stale).
	metaPath := filepath.Join(storeDir, depsMetadataFile)
	if _, statErr := os.Stat(metaPath); os.IsNotExist(statErr) {
		return true, nil
	}

	md, err := ReadDepsMetadata(storeDir)
	if err != nil {
		return false, fmt.Errorf("read deps metadata: %w", err)
	}

	// Build a map from name to (version, revision) from metadata.
	type depKey struct {
		Version  string
		Revision int
	}
	metaMap := make(map[string]depKey, len(md.Deps))
	for _, dep := range md.Deps {
		metaMap[dep.Name] = depKey{Version: dep.Version, Revision: dep.Revision}
	}

	// Collect declared deps from Build and Runtime.
	declared := make([]string, 0, len(r.Dependencies.Build)+len(r.Dependencies.Runtime))
	seen := make(map[string]bool)
	for _, dep := range r.Dependencies.Build {
		if !seen[dep] {
			seen[dep] = true
			declared = append(declared, dep)
		}
	}
	for _, dep := range r.Dependencies.Runtime {
		if !seen[dep] {
			seen[dep] = true
			declared = append(declared, dep)
		}
	}

	// For each declared dep, resolve and compare.
	for _, name := range declared {
		resolved, err := resolver(name)
		if err != nil {
			return false, fmt.Errorf("resolve %s: %w", name, err)
		}

		current := depKey{
			Version:  resolved.Package.Version,
			Revision: resolved.Package.Revision,
		}

		recorded, ok := metaMap[name]
		if !ok {
			// Declared dep with no record in metadata —
			// stale regardless of constraint.
			return true, nil
		}

		// If the recipe declared an explicit version
		// constraint for this dep, use it to decide
		// staleness instead of exact match. This lets
		// recipe authors opt out of automatic
		// propagation for revisions that don't actually
		// affect them.
		if expr, has := r.Dependencies.Constraints[name]; has && expr != "" {
			c, cerr := recipe.ParseConstraint(expr)
			if cerr != nil {
				return false, fmt.Errorf(
					"parse constraint for %s: %w", name, cerr)
			}
			if !c.Satisfies(recorded.Version, recorded.Revision) {
				return true, nil
			}
			// Still also check that the current recipe's
			// dep also satisfies the constraint — if not,
			// we can't resolve this install anyway; treat
			// as stale so a reinstall can fail loudly.
			if !c.Satisfies(current.Version, current.Revision) {
				return true, nil
			}
			continue
		}

		if recorded != current {
			return true, nil
		}
	}

	return false, nil
}

// BuildDepsToResolved converts a BuildDeps into the
// flat (name, version, revision) list we persist in
// .gale-deps.toml. The version-revision is extracted
// from each store dir's basename: "curl/8.19.0-2" →
// (curl, 8.19.0, 2); a bare basename with no revision
// suffix is treated as revision 1 for back-compat
// with old installs.
//
// Deps with malformed store dirs (empty path, no parent)
// are skipped silently.
// BuildDepsToResolved converts a BuildDeps into the
// flat (name, version, revision) list we persist in
// .gale-deps.toml. The version-revision is extracted
// from each store dir's basename: "curl/8.19.0-2" →
// (curl, 8.19.0, 2); a bare basename with no revision
// suffix is treated as revision 1 for back-compat
// with old installs.
//
// Deps with malformed store dirs (empty path, no parent)
// are skipped silently.
func BuildDepsToResolved(deps *build.BuildDeps) []ResolvedDep {
	if deps == nil {
		return nil
	}
	if len(deps.NamedDirs) == 0 {
		return nil
	}

	result := make([]ResolvedDep, 0, len(deps.NamedDirs))
	for name, dir := range deps.NamedDirs {
		if name == "" || dir == "" {
			continue
		}
		base := filepath.Base(dir)
		version, revision := parseVersionRevision(base)
		result = append(result, ResolvedDep{
			Name:     name,
			Version:  version,
			Revision: revision,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// parseVersionRevision splits a basename into (version, revision).
// If the basename ends with "-<digits>", the digits become the
// revision and the prefix is the version. Otherwise the whole
// basename is the version and revision defaults to 1.
func parseVersionRevision(base string) (string, int) {
	idx := strings.LastIndex(base, "-")
	if idx >= 0 {
		suffix := base[idx+1:]
		if isAllDigits(suffix) {
			rev, err := strconv.Atoi(suffix)
			if err == nil {
				return base[:idx], rev
			}
		}
	}
	return base, 1
}

// isAllDigits reports whether s is non-empty and contains only ASCII digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
