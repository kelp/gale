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
	"io"
	"os"
	"path/filepath"
	"syscall"

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
//
// An archive can plant this name as an absolute symlink. The
// extractor allows that on the stated grounds that later writes
// use O_NOFOLLOW; os.WriteFile does not. os.Remove unlinks the
// entry itself, then the open refuses a replanted link.
func Write(dir string, md Metadata) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(md); err != nil {
		return fmt.Errorf("encode recipe metadata: %w", err)
	}
	path := filepath.Join(dir, File)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove staged recipe metadata: %w", err)
	}
	//nolint:gosec // world-readable like every other store file
	f, err := os.OpenFile(
		path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0o644,
	)
	if err != nil {
		return fmt.Errorf("write recipe metadata: %w", err)
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		os.Remove(path)
		return fmt.Errorf("write recipe metadata: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return fmt.Errorf("write recipe metadata: %w", err)
	}
	return nil
}

// Read reads <dir>/.gale-recipe.toml. Returns an empty Metadata
// (no error) if the file does not exist.
//
// Absence is established by an O_NOFOLLOW open. os.ReadFile
// follows a symlink, and a dangling one fails with ENOENT, so a
// planted link would look like a missing sidecar (a cache miss
// that then Write must not follow). A resolvable link is refused
// too: the digest must describe this directory, not a target
// chosen by whoever planted the name.
func Read(dir string) (Metadata, error) {
	path := filepath.Join(dir, File)
	// O_NONBLOCK because the type check comes AFTER the open, and
	// an O_RDONLY open of a FIFO waits for a writer.
	f, err := os.OpenFile(
		path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0,
	)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Metadata{}, nil
		}
		if errors.Is(err, syscall.ELOOP) {
			return Metadata{}, fmt.Errorf(
				"read recipe metadata: %w: a symlink, not a record", err,
			)
		}
		return Metadata{}, fmt.Errorf("read recipe metadata: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Metadata{}, fmt.Errorf("stat recipe metadata: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return Metadata{}, fmt.Errorf(
			"read recipe metadata: not a regular file (%s)", fi.Mode().Type(),
		)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return Metadata{}, fmt.Errorf("read recipe metadata: %w", err)
	}
	var md Metadata
	if _, err := toml.Decode(string(data), &md); err != nil {
		return Metadata{}, fmt.Errorf("parse recipe metadata: %w", err)
	}
	return md, nil
}
