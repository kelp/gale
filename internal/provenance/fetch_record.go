package provenance

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/store"
)

// MethodFetch is the unused fetch-path sidecar method. ReadUnverified
// still rejects it: Record.validate only allows binary and source.
// Later verify / admission slices teach Record; do not extend it here.
const MethodFetch = "fetch"

// FetchRecord is the unused fetch-path sidecar. It is scheduled
// debt: tree_digest belongs on Record once lockgraph accepts fetch.
type FetchRecord struct {
	Name       string `toml:"name"`
	Version    string `toml:"version"`
	SHA256     string `toml:"sha256"`
	TreeDigest string `toml:"tree_digest"`
	Method     string `toml:"method"`
}

// WriteFetch writes r as File in dir. An incomplete record is
// refused. The write is os.WriteFile, not atomicfile.Write: a
// dest/.gale-tmp-* leftover is visible to DigestTree and would
// make a retry occupied + different digest.
func WriteFetch(dir string, r FetchRecord) error {
	if err := r.validate(); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(r); err != nil {
		return fmt.Errorf("encode fetch provenance: %w", err)
	}
	path := filepath.Join(dir, File)
	//nolint:gosec // world-readable like every other store file
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write fetch provenance: %w", err)
	}
	return nil
}

func (r FetchRecord) validate() error {
	if r.Name == "" {
		return fmt.Errorf("%w: missing name", ErrInvalid)
	}
	if r.Version == "" {
		return fmt.Errorf("%w: missing version", ErrInvalid)
	}
	if !store.SafeComponent(r.Name) {
		return fmt.Errorf("%w: name is not a single path component", ErrInvalid)
	}
	if !store.SafeComponent(r.Version) {
		return fmt.Errorf("%w: version is not a single path component", ErrInvalid)
	}
	if !lockgraph.IsHexSHA256(r.SHA256) {
		return fmt.Errorf("%w: sha256 is not 64 hex digits", ErrInvalid)
	}
	if !lockgraph.IsDigest(r.TreeDigest) {
		return fmt.Errorf("%w: tree_digest is not a sha256 digest", ErrInvalid)
	}
	if r.Method != MethodFetch {
		return fmt.Errorf("%w: method must be %s", ErrInvalid, MethodFetch)
	}
	return nil
}
