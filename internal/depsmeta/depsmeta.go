// Package depsmeta is the on-disk format for a package's
// built-against dep closure (.gale-deps.toml). It lives in a
// dedicated package so build can write the file before the
// archive is sealed and installer can read it back without a
// dependency cycle.
package depsmeta

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
)

// File is the basename of the metadata file written into a
// store dir (and into a built tarball's prefix root).
const File = ".gale-deps.toml"

// ResolvedDep records one dep's full identity at build/install
// time.
type ResolvedDep struct {
	Name     string `toml:"name"`
	Version  string `toml:"version"`
	Revision int    `toml:"revision"`
}

// Metadata is the on-disk form of a package's built-against
// dep closure.
type Metadata struct {
	Deps []ResolvedDep `toml:"deps"`
}

// Has reports whether <dir>/.gale-deps.toml exists.
func Has(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, File))
	return err == nil
}

// Write writes the metadata file into dir, overwriting any
// existing file.
func Write(dir string, md Metadata) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(md); err != nil {
		return fmt.Errorf("encode deps metadata: %w", err)
	}
	path := filepath.Join(dir, File)
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write deps metadata: %w", err)
	}
	return nil
}

// Read reads <dir>/.gale-deps.toml. Returns an empty Metadata
// (no error) if the file does not exist.
func Read(dir string) (Metadata, error) {
	path := filepath.Join(dir, File)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Metadata{}, nil
		}
		return Metadata{}, fmt.Errorf("read deps metadata: %w", err)
	}
	var md Metadata
	if _, err := toml.Decode(string(data), &md); err != nil {
		return Metadata{}, fmt.Errorf("parse deps metadata: %w", err)
	}
	return md, nil
}

// FromNamedDirs converts a name → store-dir map into the flat
// ResolvedDep list persisted in .gale-deps.toml. The store
// dir's basename is split into (version, revision): a
// "1.2.3-2" suffix becomes revision 2; a bare "1.2.3" defaults
// to revision 1.
//
// Entries with empty name or path are skipped.
func FromNamedDirs(namedDirs map[string]string) []ResolvedDep {
	if len(namedDirs) == 0 {
		return nil
	}
	result := make([]ResolvedDep, 0, len(namedDirs))
	for name, dir := range namedDirs {
		if name == "" || dir == "" {
			continue
		}
		version, revision := parseVersionRevision(filepath.Base(dir))
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

// FromNamedDirsFiltered is FromNamedDirs restricted to the dep
// names in keep. Entries whose name is absent from keep are
// dropped; names in keep with no matching dir are skipped. The
// builder uses it to record only the runtime dep closure in
// .gale-deps.toml, excluding build-only tools (cmake, rust, go
// and their transitive deps) that the shipped binary cannot
// link. Transitivity is preserved because each recorded dep's
// own metadata lists its runtime deps, so consumers that walk
// the metadata chain (the dylib farm, gc) still reach the full
// runtime closure. See gh#157.
func FromNamedDirsFiltered(namedDirs map[string]string, keep []string) []ResolvedDep {
	if len(namedDirs) == 0 || len(keep) == 0 {
		return nil
	}
	keepSet := make(map[string]bool, len(keep))
	for _, name := range keep {
		keepSet[name] = true
	}
	filtered := make(map[string]string, len(keep))
	for name, dir := range namedDirs {
		if keepSet[name] {
			filtered[name] = dir
		}
	}
	return FromNamedDirs(filtered)
}

func parseVersionRevision(base string) (string, int) {
	idx := strings.LastIndex(base, "-")
	if idx >= 0 {
		suffix := base[idx+1:]
		if isAllDigits(suffix) {
			if rev, err := strconv.Atoi(suffix); err == nil {
				return base[:idx], rev
			}
		}
	}
	return base, 1
}

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
