package lockfile

import (
	"fmt"
	"io/fs"

	"github.com/BurntSushi/toml"
)

// V2File is one source path placed into the store tree.
type V2File struct {
	Src  string `toml:"src"`
	Dest string `toml:"dest"`
	Mode int    `toml:"mode"`
}

// V2Attestation is the locked identity a fetch must verify.
// A nil pointer means the artifact has no attestation
// requirement; a non-nil value, even empty, means one was
// recorded and dropping it is a later refusal.
type V2Attestation struct {
	Issuer string `toml:"issuer"`
	SAN    string `toml:"san"`
	Repo   string `toml:"repo"`
}

// V2Artifact is one fetched package for one platform.
type V2Artifact struct {
	URL         string         `toml:"url"`
	Format      string         `toml:"format"`
	SHA256      string         `toml:"sha256"`
	TreeDigest  string         `toml:"tree_digest"`
	Method      string         `toml:"method"`
	Strip       int            `toml:"strip"`
	HashSource  string         `toml:"hash_source"`
	IndexCommit string         `toml:"index_commit"`
	Attestation *V2Attestation `toml:"attestation,omitempty"`
	Files       []V2File       `toml:"files"`
}

// V2Package is one locked fetch node: a package at one exact
// version, with one artifact per platform. Nodes are keyed
// name@version, so several versions of a package coexist
// across targets.
type V2Package struct {
	Artifacts map[string]V2Artifact `toml:"artifacts"`
}

// V2 is the fetch lockfile schema. This build can read it;
// nothing writes it yet. Package keys and target roots are
// name@version with no revision and must not be passed to
// ParseIdentity.
type V2 struct {
	Version  int                  `toml:"version"`
	Targets  Targets              `toml:"targets"`
	Packages map[string]V2Package `toml:"packages"`
}

// wireV2 is the on-disk document, guard included. The guard is
// a wire-level concern only: ReadV2 strips it, so callers never
// see it as a node.
type wireV2 struct {
	Version  int                             `toml:"version"`
	Targets  Targets                         `toml:"targets"`
	Packages map[string]wireNode[V2Artifact] `toml:"packages"`
}

// ReadV2 reads a v2 lockfile. A missing file wraps fs.ErrNotExist
// so callers can distinguish "no lock" from a lock that is
// present but unusable. Load still rejects version 2 as
// ErrUnknownVersion; this reader is unused until a writer exists.
func ReadV2(path string) (*V2, error) {
	data, absent, err := readLockFile(path)
	if err != nil {
		return nil, err
	}
	if absent {
		return nil, fmt.Errorf("reading lock file %s: %w", path, fs.ErrNotExist)
	}
	version, err := probeVersion(path, data)
	if err != nil {
		return nil, err
	}
	if version == nil {
		return nil, fmt.Errorf("%s: %w", path, ErrLegacySchema)
	}
	if *version != SchemaV2 {
		return nil, fmt.Errorf(
			"%s: %w: found %d, this reader models %d",
			path, ErrUnknownVersion, *version, SchemaV2,
		)
	}
	return decodeV2(path, data)
}

// decodeV2 decodes a document already known to claim version 2.
func decodeV2(path string, data []byte) (*V2, error) {
	var w wireV2
	md, err := toml.Decode(string(data), &w)
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %w", path, ErrMalformed, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf(
			"%s: %w: %s", path, ErrUnknownField, joinKeys(undecoded),
		)
	}

	arts, err := stripGuardNodes(w.Packages, guardKeyV2, SchemaV2)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	pkgs := make(map[string]V2Package, len(arts))
	for name, a := range arts {
		pkgs[name] = V2Package{Artifacts: a}
	}
	return &V2{Version: w.Version, Targets: w.Targets, Packages: pkgs}, nil
}
