package lockfile

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
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

	// ErrMalformed reports a lockfile that is present but cannot be
	// parsed at all. It shares a class with the schema errors above
	// rather than being an ordinary failure: in every case the lock
	// exists and cannot be modeled, and the remedy is to regenerate
	// it. Without the sentinel a syntax error or a mistyped value
	// would be indistinguishable from a build or network failure.
	ErrMalformed = errors.New("malformed lockfile")

	// ErrDowngradeGuard reports a v1 lockfile whose reserved guard
	// entry is absent or malformed. Such a file still claims version
	// 1 but is destructible by an already-shipped gale, which is the
	// exact hole the guard closes, so it is refused rather than
	// repaired.
	ErrDowngradeGuard = errors.New("malformed lockfile downgrade guard")
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

// guardKey is the reserved [packages] entry that stops an
// already-shipped gale from rewriting a v1 lockfile in the flat
// schema. It cannot collide with a real node, which is always
// name@version-revision. Its value is an integer where the legacy
// LockedPackage.Version is a string, so the legacy decoder fails on
// type and old gale stops instead of discarding an enforced lock.
const guardKey = "!gale-lock-v1"

// wirePackage is the on-disk shape of a [packages.*] table when
// reading. Version is decoded permissively: a string there is
// exactly what the legacy decoder accepts, so rejecting it must be
// ours to do and must name the guard rather than surfacing a TOML
// type mismatch. It is never encoded; outPackage is the writer's
// shape.
type wirePackage struct {
	Version   any                 `toml:"version"`
	Artifacts map[string]Artifact `toml:"artifacts"`
}

// wireV1 is the on-disk document, guard included. The guard is a
// wire-level concern only: ReadV1 strips it and WriteV1 injects it,
// so plan construction never sees it as a node and never needs to
// remember to skip one.
type wireV1 struct {
	Version  int                    `toml:"version"`
	Targets  Targets                `toml:"targets"`
	Packages map[string]wirePackage `toml:"packages"`
}

// outPackage is the writer's shape. Version is a pointer so it is
// emitted for the guard alone.
type outPackage struct {
	Version   *int                `toml:"version,omitempty"`
	Artifacts map[string]Artifact `toml:"artifacts,omitempty"`
}

// outV1 mirrors wireV1 for encoding.
type outV1 struct {
	Version  int                   `toml:"version"`
	Targets  Targets               `toml:"targets"`
	Packages map[string]outPackage `toml:"packages"`
}

// stripGuard validates the downgrade guard and returns the package
// nodes without it. A file claiming version 1 without a well-formed
// guard is still destructible by an old gale, so it is refused
// rather than repaired.
func stripGuard(w *wireV1) (map[string]Package, error) {
	guard, ok := w.Packages[guardKey]
	if !ok {
		return nil, fmt.Errorf(
			"%w: reserved entry [packages.%q] is missing",
			ErrDowngradeGuard, guardKey,
		)
	}
	if v, isInt := guard.Version.(int64); !isInt || v != SchemaVersion {
		return nil, fmt.Errorf(
			"%w: [packages.%q] version must be the integer %d, found %v",
			ErrDowngradeGuard, guardKey, SchemaVersion, guard.Version,
		)
	}
	if len(guard.Artifacts) > 0 {
		return nil, fmt.Errorf(
			"%w: [packages.%q] must carry no artifacts",
			ErrDowngradeGuard, guardKey,
		)
	}

	pkgs := make(map[string]Package, len(w.Packages)-1)
	for name, p := range w.Packages {
		if name == guardKey {
			continue
		}
		if p.Version != nil {
			return nil, fmt.Errorf(
				"%w: package %q carries the reserved guard field",
				ErrDowngradeGuard, name,
			)
		}
		pkgs[name] = Package{Artifacts: p.Artifacts}
	}
	return pkgs, nil
}

// ReadV1 reads a v1 lockfile. A missing file wraps fs.ErrNotExist
// so callers can distinguish "no lock" (unlocked mode) from a lock
// that is present but unusable.
func ReadV1(path string) (*V1, error) {
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
	if err := checkSchemaVersion(path, *version); err != nil {
		return nil, err
	}
	return decodeV1(path, data)
}

// probeVersion decodes only the top-level version key, so a file can
// be classified before its body is decoded against a struct that may
// not match it. A nil result means the key is absent, which is the
// legacy schema.
func probeVersion(path string, data []byte) (*int, error) {
	var probe schemaProbe
	if _, err := toml.Decode(string(data), &probe); err != nil {
		return nil, fmt.Errorf("%s: %w: %w", path, ErrMalformed, err)
	}
	return probe.Version, nil
}

// checkSchemaVersion rejects a schema this build does not model.
func checkSchemaVersion(path string, version int) error {
	if version != SchemaVersion {
		return fmt.Errorf(
			"%s: %w: found %d, this gale models %d",
			path, ErrUnknownVersion, version, SchemaVersion,
		)
	}
	return nil
}

// decodeV1 decodes a document already known to claim version 1.
func decodeV1(path string, data []byte) (*V1, error) {
	var w wireV1
	md, err := toml.Decode(string(data), &w)
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %w", path, ErrMalformed, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf(
			"%s: %w: %s", path, ErrUnknownField, joinKeys(undecoded),
		)
	}

	pkgs, err := stripGuard(&w)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &V1{Version: w.Version, Targets: w.Targets, Packages: pkgs}, nil
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
	out := outV1{
		Version:  lf.Version,
		Targets:  lf.Targets,
		Packages: make(map[string]outPackage, len(lf.Packages)+1),
	}
	guardVersion := SchemaVersion
	out.Packages[guardKey] = outPackage{Version: &guardVersion}
	for name, p := range lf.Packages {
		if name == guardKey {
			return fmt.Errorf(
				"%w: package %q collides with the reserved entry",
				ErrDowngradeGuard, name,
			)
		}
		out.Packages[name] = outPackage{Artifacts: p.Artifacts}
	}

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(out); err != nil {
		return fmt.Errorf("encoding lock file: %w", err)
	}
	return atomicfile.Write(path, buf.Bytes())
}
