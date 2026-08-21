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

const (
	// SchemaV1 is the enforced source-install lock schema.
	SchemaV1 = 1
	// SchemaV2 is the fetch lock schema. WriteV2 writes it.
	// Load classifies it as KindV2. WriteV1 still models v1.
	SchemaV2 = 2
	// SchemaVersion is the schema Load and WriteV1 model. It
	// stays SchemaV1 even though WriteV2 exists. Bumping it
	// would make WriteV1 emit version = 2 with a v1 body and
	// would make Load accept a v2 file as v1.
	SchemaVersion = SchemaV1
)

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

	// ErrUnknownField reports a lockfile carrying a field this
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

	// ErrDowngradeGuard reports a lockfile whose reserved guard
	// entry is absent or malformed. Such a file still claims a
	// version this build understands but is destructible by an
	// already-shipped gale, which is the exact hole the guard
	// closes, so it is refused rather than repaired.
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

const (
	// guardKey is the reserved [packages] entry that stops an
	// already-shipped gale from rewriting a v1 lockfile in the
	// flat schema. It cannot collide with a real v1 node, which
	// is always name@version-revision. Its value is an integer
	// where the legacy LockedPackage.Version is a string, so the
	// legacy decoder fails on type and old gale stops instead of
	// discarding an enforced lock.
	guardKey = "!gale-lock-v1"
	// guardKeyV2 is the v2 counterpart. A real v2 node is
	// name@version, which still cannot collide with this key.
	guardKeyV2 = "!gale-lock-v2"
)

// wireNode is the on-disk shape of a [packages.*] table when
// reading. Version is decoded permissively: a string there is
// exactly what the legacy decoder accepts, so rejecting it must be
// ours to do and must name the guard rather than surfacing a TOML
// type mismatch. It is never encoded; writeGuarded is the writer's
// shape.
type wireNode[A any] struct {
	Version   any          `toml:"version"`
	Artifacts map[string]A `toml:"artifacts"`
}

// wireV1 is the on-disk document, guard included. The guard is a
// wire-level concern only: ReadV1 strips it and WriteV1 injects it,
// so plan construction never sees it as a node and never needs to
// remember to skip one.
type wireV1 struct {
	Version  int                           `toml:"version"`
	Targets  Targets                       `toml:"targets"`
	Packages map[string]wireNode[Artifact] `toml:"packages"`
}

// stripGuardNodes validates the downgrade guard and returns the
// remaining package artifacts without it. A file claiming a version
// this build understands without a well-formed guard is still
// destructible by an old gale, so it is refused rather than repaired.
func stripGuardNodes[A any](packages map[string]wireNode[A], key string, want int) (map[string]map[string]A, error) {
	guard, ok := packages[key]
	if !ok {
		return nil, fmt.Errorf(
			"%w: reserved entry [packages.%q] is missing",
			ErrDowngradeGuard, key,
		)
	}
	if v, isInt := guard.Version.(int64); !isInt || int(v) != want {
		return nil, fmt.Errorf(
			"%w: [packages.%q] version must be the integer %d, found %v",
			ErrDowngradeGuard, key, want, guard.Version,
		)
	}
	if len(guard.Artifacts) > 0 {
		return nil, fmt.Errorf(
			"%w: [packages.%q] must carry no artifacts",
			ErrDowngradeGuard, key,
		)
	}

	pkgs := make(map[string]map[string]A, len(packages)-1)
	for name, p := range packages {
		if name == key {
			continue
		}
		if p.Version != nil {
			return nil, fmt.Errorf(
				"%w: package %q carries the reserved guard field",
				ErrDowngradeGuard, name,
			)
		}
		pkgs[name] = p.Artifacts
	}
	return pkgs, nil
}

// stripGuard validates the v1 downgrade guard and returns the
// package nodes without it.
func stripGuard(w *wireV1) (map[string]Package, error) {
	arts, err := stripGuardNodes(w.Packages, guardKey, SchemaV1)
	if err != nil {
		return nil, err
	}
	pkgs := make(map[string]Package, len(arts))
	for name, a := range arts {
		pkgs[name] = Package{Artifacts: a}
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
	arts := make(map[string]map[string]Artifact, len(lf.Packages))
	for name, p := range lf.Packages {
		arts[name] = p.Artifacts
	}
	return writeGuarded(path, lf.Version, lf.Targets, arts, guardKey)
}

// writeGuarded encodes a lock document with the integer downgrade
// guard injected. WriteV1 and WriteV2 share it so the on-disk
// guard rules stay one function.
func writeGuarded[A any](
	path string, version int, targets Targets,
	packages map[string]map[string]A, key string,
) error {
	type outP struct {
		Version   *int         `toml:"version,omitempty"`
		Artifacts map[string]A `toml:"artifacts,omitempty"`
	}
	type outDoc struct {
		Version  int             `toml:"version"`
		Targets  Targets         `toml:"targets"`
		Packages map[string]outP `toml:"packages"`
	}
	out := outDoc{
		Version:  version,
		Targets:  targets,
		Packages: make(map[string]outP, len(packages)+1),
	}
	guardVersion := version
	out.Packages[key] = outP{Version: &guardVersion}
	for name, arts := range packages {
		if name == key {
			return fmt.Errorf(
				"%w: package %q collides with the reserved entry",
				ErrDowngradeGuard, name,
			)
		}
		out.Packages[name] = outP{Artifacts: arts}
	}

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(out); err != nil {
		return fmt.Errorf("encoding lock file: %w", err)
	}
	return atomicfile.Write(path, buf.Bytes())
}
