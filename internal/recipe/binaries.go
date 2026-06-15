package recipe

import (
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/kelp/gale/internal/version"
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
	// Digests maps platform key → "sha256:<64hex>" OCI manifest
	// digest recorded by CI when the prebuilt was pushed. Empty
	// when the file predates the field or the digest is absent.
	Digests map[string]string `toml:"-"`
	// History is the append-only [[history]] ledger: one entry
	// per published <version>-<revision>. It is the registry-side
	// source of truth for installable versions, replacing the
	// .versions commit-pin file (gh#121). Nil when the file
	// predates the ledger.
	History []BinaryHistoryEntry `toml:"-"`
}

// BinaryHistoryEntry is one [[history]] table in a .binaries.toml
// ledger: a published <version>-<revision> with its per-platform
// binary coordinates. Unlike the flat head section, history entries
// carry only sha256 and manifest_digest — never deps.
type BinaryHistoryEntry struct {
	Version string
	// Platforms maps platform key → sha256 layer-blob hash.
	Platforms map[string]string
	// Digests maps platform key → "sha256:<64hex>" OCI manifest
	// digest. A platform may appear in Platforms without a digest
	// when the recorded value was malformed or absent.
	Digests map[string]string
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
		Digests:   make(map[string]string),
	}

	// Extract the top-level version string.
	if v, ok := raw["version"]; ok {
		if s, ok := v.(string); ok {
			idx.Version = s
		}
	}

	// The [[history]] ledger is an array of tables, handled
	// separately from the flat platform sections below.
	if h, ok := raw["history"]; ok {
		idx.History = parseBinaryHistory(h)
	}

	// Remaining top-level keys are platform sections.
	for key, val := range raw {
		if key == "version" || key == "history" {
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
		if dig, ok := sub["manifest_digest"]; ok {
			if s, ok := dig.(string); ok && validManifestDigest(s) {
				idx.Digests[key] = s
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

// parseBinaryHistory converts the raw TOML value for the
// `[[history]]` array of tables into typed BinaryHistoryEntry
// values. Each entry has a `version` string plus one inline table
// per platform carrying `sha256` and an optional `manifest_digest`.
// Like the flat section, malformed pieces degrade rather than fail
// the parse: a platform with no sha256 is omitted, and a malformed
// digest is dropped while its sha256 is retained.
func parseBinaryHistory(raw interface{}) []BinaryHistoryEntry {
	tables := asTableSlice(raw)
	if len(tables) == 0 {
		return nil
	}
	out := make([]BinaryHistoryEntry, 0, len(tables))
	for _, t := range tables {
		entry := BinaryHistoryEntry{
			Platforms: make(map[string]string),
			Digests:   make(map[string]string),
		}
		for key, val := range t {
			if key == "version" {
				if s, ok := val.(string); ok {
					entry.Version = s
				}
				continue
			}
			plat, ok := val.(map[string]interface{})
			if !ok {
				continue
			}
			sha, ok := plat["sha256"].(string)
			if !ok || sha == "" {
				continue
			}
			entry.Platforms[key] = sha
			if dig, ok := plat["manifest_digest"].(string); ok && validManifestDigest(dig) {
				entry.Digests[key] = dig
			}
		}
		out = append(out, entry)
	}
	return out
}

// asTableSlice normalizes the two concrete types BurntSushi may
// decode an array-of-tables into ([]map[string]interface{} or
// []interface{}) to a slice of tables.
func asTableSlice(raw interface{}) []map[string]interface{} {
	if arr, ok := raw.([]map[string]interface{}); ok {
		return arr
	}
	iarr, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(iarr))
	for _, v := range iarr {
		if m, ok := v.(map[string]interface{}); ok {
			out = append(out, m)
		}
	}
	return out
}

// validManifestDigest reports whether s is a well-formed OCI
// manifest digest: "sha256:" followed by exactly 64 lowercase
// hex characters. Malformed digests are dropped at parse time —
// the field is informational, so a bad value degrades to absent
// rather than failing the whole parse.
func validManifestDigest(s string) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(s, prefix) {
		return false
	}
	hex := s[len(prefix):]
	if len(hex) != 64 {
		return false
	}
	for _, c := range hex {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// PickHistoryLatest returns the newest entry in the [[history]]
// ledger under gale's total version order (version.KeyNewer), and
// false when the ledger is empty. This is the registry-side
// resolution of "latest installable version" that replaces the
// .versions commit-pin file (gh#121).
func (idx *BinaryIndex) PickHistoryLatest() (BinaryHistoryEntry, bool) {
	keys := make([]string, len(idx.History))
	for i, e := range idx.History {
		keys[i] = e.Version
	}
	latest, ok := version.Latest(keys)
	if !ok {
		return BinaryHistoryEntry{}, false
	}
	for _, e := range idx.History {
		if e.Version == latest {
			return e, true
		}
	}
	return BinaryHistoryEntry{}, false
}

// MergeBinaries populates a recipe's Binary map from a
// BinaryIndex. If the index is nil or its version doesn't
// match the recipe version (stale), this is a no-op.
//
// Accepted match forms for idx.Version:
//   - revision 1 recipes accept either "<version>" or
//     "<version>-1" for compatibility with existing indexes.
//   - revision > 1 recipes require the full
//     "<version>-<revision>" string so a stale bare index
//     cannot install old prebuilts into a new revision.
//
// The GHCR URL is constructed as:
//
//	https://ghcr.io/v2/<ghcrBase>/<name>/blobs/sha256:<hash>
func MergeBinaries(r *Recipe, idx *BinaryIndex, ghcrBase string) {
	if idx == nil {
		return
	}
	mergeBinaryPlatforms(r, idx.Version, idx.Platforms, idx.Digests, ghcrBase)
}

// MergeBinariesFromHistory populates a recipe's Binary map from a
// single [[history]] ledger entry, the same way MergeBinaries does
// from the flat head section. The version gate (binaryIndexMatchesRecipe)
// still applies, so a ledger entry is only merged into a recipe whose
// version it matches. Used by ledger-based resolution (gh#121).
func MergeBinariesFromHistory(r *Recipe, entry BinaryHistoryEntry, ghcrBase string) {
	mergeBinaryPlatforms(r, entry.Version, entry.Platforms, entry.Digests, ghcrBase)
}

// MergeBinariesPreferLedger merges binaries from the index,
// preferring the [[history]] ledger head over the flat head
// section, and reports whether a ledger was used. This is the
// shared "ledger-first, flat fallback" rule for both registry and
// local --recipes resolution (gh#121). A nil index is a no-op.
func MergeBinariesPreferLedger(r *Recipe, idx *BinaryIndex, ghcrBase string) bool {
	if idx == nil {
		return false
	}
	if entry, ok := idx.PickHistoryLatest(); ok {
		MergeBinariesFromHistory(r, entry, ghcrBase)
		return true
	}
	MergeBinaries(r, idx, ghcrBase)
	return false
}

// mergeBinaryPlatforms is the shared body of MergeBinaries and
// MergeBinariesFromHistory: when version matches the recipe, it
// fills r.Binary with one GHCR blob entry per platform.
func mergeBinaryPlatforms(r *Recipe, indexVersion string, platforms, digests map[string]string, ghcrBase string) {
	if !binaryIndexMatchesRecipe(r, indexVersion) {
		return
	}
	r.Binary = make(map[string]Binary, len(platforms))
	for platform, sha := range platforms {
		r.Binary[platform] = Binary{
			URL: fmt.Sprintf(
				"https://ghcr.io/v2/%s/%s/blobs/sha256:%s",
				ghcrBase, r.Package.Name, sha,
			),
			SHA256:         sha,
			Trust:          TrustSigstore,
			ManifestDigest: digests[platform],
		}
	}
}

func binaryIndexMatchesRecipe(r *Recipe, indexVersion string) bool {
	if indexVersion == r.Package.Full() {
		return true
	}
	return r.Package.Revision <= 1 && indexVersion == r.Package.Version
}
