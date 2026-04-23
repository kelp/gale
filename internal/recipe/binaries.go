package recipe

import (
	"fmt"

	"github.com/BurntSushi/toml"
)

// BinaryDep records one entry from a `.binaries.toml` per-platform
// `deps` array. It's the same shape as depsmeta.ResolvedDep but
// lives here to avoid a dependency cycle (depsmeta is the
// on-disk format for the archive-internal `.gale-deps.toml`; this
// type is the registry-level view of the same closure).
//
// Informational only at install time — the archive's own
// `.gale-deps.toml` remains authoritative. See docs/revisions.md.
type BinaryDep struct {
	Name     string `toml:"name"`
	Version  string `toml:"version"`
	Revision int    `toml:"revision"`
}

// BinaryIndex represents a .binaries.toml file that maps
// platform keys to SHA256 hashes (and optionally the linked
// dep closure) for prebuilt binaries.
type BinaryIndex struct {
	Version   string            `toml:"version"`
	Platforms map[string]string `toml:"-"`
	// Deps maps platform key → list of resolved (name, version,
	// revision) entries recorded by CI when the prebuilt was
	// built. Empty when the file was written before C4 landed,
	// or when the build had no declared deps.
	Deps map[string][]BinaryDep `toml:"-"`
}

// ParseBinaryIndex parses a .binaries.toml string into a
// BinaryIndex. Platform sections like [darwin-arm64] are
// decoded as map keys with sha256 sub-fields.
func ParseBinaryIndex(data string) (*BinaryIndex, error) {
	var raw map[string]interface{}
	if err := toml.Unmarshal([]byte(data), &raw); err != nil {
		return nil, fmt.Errorf("invalid binaries TOML: %w", err)
	}

	idx := &BinaryIndex{
		Platforms: make(map[string]string),
		Deps:      make(map[string][]BinaryDep),
	}

	// Extract the top-level version string.
	if v, ok := raw["version"]; ok {
		if s, ok := v.(string); ok {
			idx.Version = s
		}
	}

	// Remaining top-level keys are platform sections.
	for key, val := range raw {
		if key == "version" {
			continue
		}
		sub, ok := val.(map[string]interface{})
		if !ok {
			continue
		}
		if sha, ok := sub["sha256"]; ok {
			if s, ok := sha.(string); ok {
				idx.Platforms[key] = s
			}
		}
		if depsRaw, ok := sub["deps"]; ok {
			if deps := parseBinaryDeps(depsRaw); len(deps) > 0 {
				idx.Deps[key] = deps
			}
		}
	}

	return idx, nil
}

// parseBinaryDeps converts the raw TOML value for a platform's
// `deps = [...]` into typed BinaryDep entries. Invalid entries
// (non-table, missing fields) are skipped — the field is
// informational, so a malformed entry degrades to empty rather
// than failing the whole parse.
func parseBinaryDeps(raw interface{}) []BinaryDep {
	arr, ok := raw.([]map[string]interface{})
	if !ok {
		// BurntSushi decodes inline tables and arrays of tables
		// into different concrete types. Handle both.
		iarr, ok2 := raw.([]interface{})
		if !ok2 {
			return nil
		}
		for _, v := range iarr {
			m, ok := v.(map[string]interface{})
			if ok {
				arr = append(arr, m)
			}
		}
	}
	var out []BinaryDep
	for _, m := range arr {
		var dep BinaryDep
		if s, ok := m["name"].(string); ok {
			dep.Name = s
		}
		if s, ok := m["version"].(string); ok {
			dep.Version = s
		}
		switch n := m["revision"].(type) {
		case int64:
			dep.Revision = int(n)
		case int:
			dep.Revision = n
		}
		if dep.Name == "" || dep.Version == "" {
			continue
		}
		if dep.Revision <= 0 {
			dep.Revision = 1
		}
		out = append(out, dep)
	}
	return out
}

// MergeBinaries populates a recipe's Binary map from a
// BinaryIndex. If the index is nil or its version doesn't
// match the recipe version (stale), this is a no-op.
//
// Accepted match forms for idx.Version:
//   - the full "<version>-<revision>" string (new canonical)
//   - the bare "<version>" (legacy .binaries.toml files
//     written before the revision system)
//
// The GHCR URL is constructed as:
//
//	https://ghcr.io/v2/<ghcrBase>/<name>/blobs/sha256:<hash>
func MergeBinaries(r *Recipe, idx *BinaryIndex, ghcrBase string) {
	if idx == nil {
		return
	}
	if idx.Version != r.Package.Full() && idx.Version != r.Package.Version {
		return
	}

	r.Binary = make(map[string]Binary, len(idx.Platforms))
	for platform, sha := range idx.Platforms {
		r.Binary[platform] = Binary{
			URL: fmt.Sprintf(
				"https://ghcr.io/v2/%s/%s/blobs/sha256:%s",
				ghcrBase, r.Package.Name, sha),
			SHA256: sha,
			Trust:  TrustSigstore,
		}
	}
}
