package index

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// File is one package index document.
type File struct {
	Package  Package            `toml:"package"`
	Versions map[string]Version `toml:"versions"`
}

// Package is the index header. Latest must name a version key.
type Package struct {
	Name        string `toml:"name"`
	Description string `toml:"description"`
	License     string `toml:"license"`
	Homepage    string `toml:"homepage"`
	Repo        string `toml:"repo"`
	Latest      string `toml:"latest"`
}

// Version is one immutable version block.
type Version struct {
	Artifacts map[string]Artifact `toml:"artifacts"`
}

// Artifact is one platform's fetch description. Attestation is
// presence-only: nil means absent, a pointer to true is required
// when the field is set.
type Artifact struct {
	URL         string      `toml:"url"`
	Format      string      `toml:"format"`
	SHA256      string      `toml:"sha256"`
	TreeDigest  string      `toml:"tree_digest"`
	HashSource  string      `toml:"hash_source"`
	Strip       int         `toml:"strip"`
	Attestation *bool       `toml:"attestation"`
	Files       []FileEntry `toml:"files"`
}

// FileEntry maps one archive path onto the store tree.
type FileEntry struct {
	Src  string `toml:"src"`
	Dest string `toml:"dest"`
	Mode int    `toml:"mode"`
}

// Parse decodes an index document. It rejects unknown fields
// and does not lint.
func Parse(data []byte) (*File, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("parsing index: empty document")
	}
	var f File
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return nil, fmt.Errorf("parsing index: %w", err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf(
			"parsing index: unknown field %s", joinKeys(undecoded),
		)
	}
	if len(f.Versions) == 0 {
		return nil, fmt.Errorf("parsing index: missing versions")
	}
	return &f, nil
}

func joinKeys(keys []toml.Key) string {
	names := make([]string, 0, len(keys))
	for _, k := range keys {
		names = append(names, k.String())
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
