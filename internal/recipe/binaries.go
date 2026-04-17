package recipe

import (
	"fmt"

	"github.com/BurntSushi/toml"
)

// BinaryIndex represents a .binaries.toml file that maps
// platform keys to SHA256 hashes for prebuilt binaries.
type BinaryIndex struct {
	Version   string            `toml:"version"`
	Platforms map[string]string `toml:"-"`
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
	}

	return idx, nil
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
		}
	}
}
