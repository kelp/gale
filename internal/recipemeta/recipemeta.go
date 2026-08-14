// Package recipemeta is the on-disk record of which working-tree
// recipe produced a store directory (.gale-recipe.toml).
//
// It is a sibling of .gale-deps.toml and .gale-provenance.toml
// rather than an extension of either: depsmeta records the runtime
// closure, provenance attests artifact bytes, and this file is the
// cache key for --recipe / --recipes installs (gh#265). Provenance
// is all-or-nothing attestation; stuffing a cache key there would
// collide with "absent record = unprovenanced".
package recipemeta

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// File is the basename written into a store dir.
const File = ".gale-recipe.toml"

// Metadata is the on-disk form of a store directory's recipe
// fingerprint.
type Metadata struct {
	Digest string `toml:"digest"`
}

// Has reports whether <dir>/.gale-recipe.toml exists.
func Has(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, File))
	return err == nil
}

// Write writes the metadata file into dir, overwriting any
// existing file.
func Write(dir string, md Metadata) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(md); err != nil {
		return fmt.Errorf("encode recipe metadata: %w", err)
	}
	path := filepath.Join(dir, File)
	//nolint:gosec // world-readable like every other store file
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write recipe metadata: %w", err)
	}
	return nil
}

// Read reads <dir>/.gale-recipe.toml. Returns an empty Metadata
// (no error) if the file does not exist.
func Read(dir string) (Metadata, error) {
	path := filepath.Join(dir, File)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Metadata{}, nil
		}
		return Metadata{}, fmt.Errorf("read recipe metadata: %w", err)
	}
	var md Metadata
	if _, err := toml.Decode(string(data), &md); err != nil {
		return Metadata{}, fmt.Errorf("parse recipe metadata: %w", err)
	}
	return md, nil
}
