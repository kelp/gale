package lockfile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/kelp/gale/internal/atomicfile"
)

// SchemaVersion is the lockfile schema this build models fully.
const SchemaVersion = 1

var (
	// ErrLegacySchema reports a lockfile with no version key: one
	// written by a gale that recorded checksums without enforcing
	// them. Its entries cannot be trusted as an integrity lock,
	// and it carries no platform dimension, so which platform
	// wrote it is unknowable.
	ErrLegacySchema = errors.New("legacy lockfile schema")

	// ErrUnknownVersion reports a schema this build does not model.
	// Reading one is a hard failure rather than a best-effort
	// parse, because toml.Decode silently drops fields the struct
	// does not know, so a write-back would destroy them.
	ErrUnknownVersion = errors.New("unknown lockfile schema version")

	// ErrUnknownField reports a v1 lockfile carrying a field this
	// build does not model. The version check alone does not catch
	// it: the file claims a version we understand, so the field
	// would decode to nothing and the next write would drop it.
	// Refusing is the same bargain as ErrUnknownVersion, one level
	// down.
	ErrUnknownField = errors.New("unknown lockfile field")
)

// Artifact is one package built or fetched for one platform. The
// dependency lists are canonical name@version-revision identifiers;
// GraphDigest binds them and the artifact's own identity into one
// value (see internal/lockgraph).
type Artifact struct {
	SHA256         string   `toml:"sha256"`
	ManifestDigest string   `toml:"manifest_digest,omitempty"`
	Method         string   `toml:"method"`
	RuntimeDeps    []string `toml:"runtime_deps,omitempty"`
	BuildDeps      []string `toml:"build_deps,omitempty"`
	GraphDigest    string   `toml:"graph_digest"`
}

// Package is one node of the locked graph: a package at one exact
// version-revision, with one artifact per platform. Nodes are keyed
// name@version-revision, so several versions of a package coexist
// across targets.
type Package struct {
	Artifacts map[string]Artifact `toml:"artifacts"`
}

// Target is the set of declared roots for one selector.
type Target struct {
	Roots []string `toml:"roots"`
}

// Targets holds the shared root graph and any host overlays. Host
// keys are gale.toml's selector strings verbatim, wildcards and
// comma-separated lists included. They are post-alias: `--host
// current` resolves to the concrete hostname before the write, so
// a concrete hostname appears here only when that alias was used
// or the selector was itself an exact host name.
//
// Default is a pointer so a project declaring only host overlays
// writes no default target at all. An empty table would invent a
// target no writer was asked to touch, and erase the difference
// between "no shared roots" and "shared roots, currently none".
type Targets struct {
	Default *Target           `toml:"default,omitempty"`
	Host    map[string]Target `toml:"host,omitempty"`
}

// V1 is the enforced lockfile schema.
type V1 struct {
	Version  int                `toml:"version"`
	Targets  Targets            `toml:"targets"`
	Packages map[string]Package `toml:"packages"`
}

// schemaProbe reads only the version key, so the schema can be
// classified before the body is decoded against a struct that may
// not match it.
type schemaProbe struct {
	Version *int `toml:"version"`
}

// ReadV1 reads a v1 lockfile. A missing file wraps fs.ErrNotExist
// so callers can distinguish "no lock" (unlocked mode) from a lock
// that is present but unusable.
func ReadV1(path string) (*V1, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading lock file: %w", err)
	}

	var probe schemaProbe
	if _, err := toml.Decode(string(data), &probe); err != nil {
		return nil, fmt.Errorf("parsing lock file: %w", err)
	}
	if probe.Version == nil {
		return nil, fmt.Errorf("%s: %w", path, ErrLegacySchema)
	}
	if *probe.Version != SchemaVersion {
		return nil, fmt.Errorf(
			"%s: %w: found %d, this gale models %d",
			path, ErrUnknownVersion, *probe.Version, SchemaVersion,
		)
	}

	var lf V1
	md, err := toml.Decode(string(data), &lf)
	if err != nil {
		return nil, fmt.Errorf("parsing lock file: %w", err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf(
			"%s: %w: %s", path, ErrUnknownField, joinKeys(undecoded),
		)
	}
	if lf.Packages == nil {
		lf.Packages = make(map[string]Package)
	}
	return &lf, nil
}

// joinKeys renders undecoded keys in their dotted TOML form so the
// error names the exact line to fix.
func joinKeys(keys []toml.Key) string {
	names := make([]string, 0, len(keys))
	for _, k := range keys {
		names = append(names, k.String())
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// WriteV1 writes a v1 lockfile atomically. It refuses any version
// this build does not model, so a partially understood lockfile is
// never rewritten from an incomplete struct.
func WriteV1(path string, lf *V1) error {
	if lf.Version != SchemaVersion {
		return fmt.Errorf(
			"%w: refusing to write version %d, this gale models %d",
			ErrUnknownVersion, lf.Version, SchemaVersion,
		)
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(lf); err != nil {
		return fmt.Errorf("encoding lock file: %w", err)
	}
	return atomicfile.Write(path, buf.Bytes())
}
