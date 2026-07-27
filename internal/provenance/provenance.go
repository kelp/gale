// Package provenance is the on-disk record of what gale fetched or
// built, and verified, for one store directory.
//
// It is a sibling of .gale-deps.toml rather than an extension of
// it: depsmeta records the runtime closure a package was built
// against, hash-free, and is exchanged between build and installer.
// Provenance records what was verified at commit time, which needs
// hashes, the install method, the platform, and the graph digest.
//
// Provenance is all-or-nothing. The presence of the file means the
// record is complete and trustworthy under its contract, so an
// incomplete record is never written and a record that fails
// validation is treated exactly as an absent one. A tri-state file
// would force every reader to interpret a missing value and would
// collide with the provenanced/unprovenanced distinction that the
// lock writers and the migration path rest on.
package provenance

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/store"
)

// File is the basename written into a store dir, beside
// .gale-deps.toml.
const File = ".gale-provenance.toml"

var (
	// ErrAbsent reports a directory with no provenance file. It is
	// deliberately distinct from ErrInvalid: an absent canonical
	// directory may be populated, while an occupied unprovenanced
	// one may only be replaced.
	ErrAbsent = errors.New("no provenance")

	// ErrInvalid reports a provenance file that does not validate
	// in full. Callers resolving a dependency's digest treat it
	// exactly as ErrAbsent; the two are distinguished for the
	// store-level decisions above, not for degrees of trust.
	ErrInvalid = errors.New("invalid provenance")

	// ErrNotCanonical reports an identity that is not spelled as a
	// canonical version-revision. It is a caller error, raised
	// rather than absorbed: a noncanonical identity would miss its
	// store directory and be indistinguishable from an
	// unprovenanced dependency, silently degrading every record
	// above it.
	ErrNotCanonical = errors.New("identity is not canonical")
)

// Record is what gale verified for one store directory.
type Record struct {
	Name           string   `toml:"name"`
	Version        string   `toml:"version"` // canonical version-revision
	Platform       string   `toml:"platform"`
	SHA256         string   `toml:"sha256"`
	ManifestDigest string   `toml:"manifest_digest,omitempty"`
	Method         string   `toml:"method"`
	RuntimeDeps    []string `toml:"runtime_deps,omitempty"`
	BuildDeps      []string `toml:"build_deps,omitempty"`
	GraphDigest    string   `toml:"graph_digest"`
}

// Key is the record's canonical identifier.
func (r Record) Key() string {
	return lockgraph.Key(r.Name, r.Version)
}

// node rebuilds the lockgraph node this record describes, so a
// stored digest can be recomputed rather than believed. Total on a
// validated record: validate proves the platform splits and every
// dependency entry parses.
func (r Record) node() lockgraph.Node {
	goos, goarch, _ := strings.Cut(r.Platform, "/")
	edges := make([]lockgraph.Edge, 0, len(r.RuntimeDeps)+len(r.BuildDeps))
	for _, k := range r.RuntimeDeps {
		edges = append(edges, edgeFromKey(lockgraph.KindRuntime, k))
	}
	for _, k := range r.BuildDeps {
		edges = append(edges, edgeFromKey(lockgraph.KindBuild, k))
	}
	return lockgraph.Node{
		Name:           r.Name,
		Version:        r.Version,
		GOOS:           goos,
		GOARCH:         goarch,
		Method:         r.Method,
		SHA256:         r.SHA256,
		ManifestDigest: r.ManifestDigest,
		Edges:          edges,
	}
}

// Write writes r into dir. An invalid record is refused rather than
// written: a caller holding a partial record has a bug, and
// persisting it would create the tri-state file this format exists
// to prevent.
func Write(dir string, r Record) error {
	if err := r.validate(); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(r); err != nil {
		return fmt.Errorf("encode provenance: %w", err)
	}
	path := filepath.Join(dir, File)
	//nolint:gosec // world-readable like every other store file
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write provenance: %w", err)
	}
	return nil
}

// ReadUnverified reads dir's provenance and validates its
// structure: required fields, canonical identity, hash formats, and
// dependency syntax.
//
// It deliberately does **not** verify the graph digest against the
// closure it claims, so a well-formed but false digest passes here.
// The awkward name is the point: structural validation is enough to
// tell absent from occupied-unprovenanced, which is the decision the
// lock writers and the migration path need, and is not enough to
// trust the record's digest.
//
// To trust the digest, pick by what you can assume about the store:
// VerifyAgainstStore when the record's serialized closure is known
// to still be installed, which in practice means install time; a
// comparison against the digest recomputed from the lock otherwise,
// because build dependencies are allowed to have been collected
// since. Substituting VerifyAgainstStore for the second case is the
// mistake, not the shortcut.
//
// A missing file is ErrAbsent, never a zero value: absent and
// unprovenanced are different states, and conflating them is what
// would let a lock writer adopt bytes it never verified. Any other
// read failure, permission included, is returned as itself and
// never reported as absence.
func ReadUnverified(dir string) (Record, error) {
	path := filepath.Join(dir, File)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Record{}, fmt.Errorf("%s: %w", dir, ErrAbsent)
		}
		return Record{}, fmt.Errorf("read provenance: %w", err)
	}

	var r Record
	md, err := toml.Decode(string(data), &r)
	if err != nil {
		return Record{}, fmt.Errorf("%s: %w: %w", path, ErrInvalid, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return Record{}, fmt.Errorf(
			"%s: %w: unknown field %s", path, ErrInvalid, undecoded[0].String(),
		)
	}
	if err := r.validate(); err != nil {
		return Record{}, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}

// New builds a complete record for n, resolving each serialized
// edge's digest from that dependency's own provenance under
// storeRoot.
//
// It returns lockgraph.ErrMissingDep when any serialized edge lacks
// usable provenance. That is not a failure of the install: the
// caller commits the artifact without provenance and continues, so
// nothing untrustworthy ever supplies a digest to an edge. Which
// edges are serialized is lockgraph's rule, deliberately not
// restated here — build edges count only for a source artifact, so
// a prebuilt binary is never blocked by an unprovenanced build
// tool.
//
// A dependency's stored digest is recomputed from its own record
// before it is used, recursively. Believing the stored value would
// make a single hand-written provenance file able to certify an
// arbitrary closure above it, which is the one thing this record
// exists to prevent.
//
// Identities must be canonical; noncanonical ones are rejected
// rather than looked up, because a missed lookup is
// indistinguishable from an unprovenanced dependency.
func New(storeRoot string, n lockgraph.Node) (Record, error) {
	if err := CheckIdentity(n.Name, n.Version); err != nil {
		return Record{}, err
	}
	for _, e := range n.Edges {
		if err := CheckIdentity(e.Name, e.Version); err != nil {
			return Record{}, err
		}
	}

	rs := newResolver(storeRoot, n.GOOS+"/"+n.GOARCH)
	digest, err := lockgraph.Digest(n, rs.edgeDigests(n.Edges))
	if err != nil {
		return Record{}, err
	}
	r := recordFrom(n, digest)
	if err := r.validate(); err != nil {
		return Record{}, err
	}
	return r, nil
}

// recordFrom is the single place a node becomes a record. Both the
// writer and the lock-relative comparator go through it, so a
// comparator can never drift from the writer by normalizing
// differently: any change here changes both sides at once.
func recordFrom(n lockgraph.Node, digest string) Record {
	return Record{
		Name:           n.Name,
		Version:        n.Version,
		Platform:       n.GOOS + "/" + n.GOARCH,
		SHA256:         n.SHA256,
		ManifestDigest: n.ManifestDigest,
		Method:         n.Method,
		RuntimeDeps:    edgeKeys(n, lockgraph.KindRuntime),
		BuildDeps:      buildDepsFor(n),
		GraphDigest:    digest,
	}
}

// buildDepsFor records build dependencies only for a source
// artifact. A locked binary node legitimately carries recipe-derived
// build edges, but they describe what would build the package rather
// than what produced the bytes fetched here, so provenance omits
// them. Dropping them is not a silent narrowing: the record's
// contract is that a binary attests only what was verified, and
// validate enforces it independently.
func buildDepsFor(n lockgraph.Node) []string {
	if n.Method != lockgraph.MethodSource {
		return nil
	}
	return edgeKeys(n, lockgraph.KindBuild)
}

// VerifyAgainstLock checks a stored record against the node the lock
// says should be there, using a graph digest the caller recomputed
// from the lock rather than from the store.
//
// This is the third trust level, and it exists here so there is only
// one of it. The activation gate and the locked cache-hit path both
// need it, and both need the same normalization: the same fields
// compared, and recipe-derived build edges ignored for a binary
// node. Two independent implementations of that comparison would
// diverge, and the direction they diverge in is "accepts something
// it should have refused".
//
// Unlike VerifyAgainstStore this reads nothing but the record, so a
// source artifact whose build dependencies were collected long ago
// still verifies: the digest binds the closure it was produced from
// without that closure needing to exist.
func VerifyAgainstLock(dir string, want lockgraph.Node, wantDigest string) (Record, error) {
	got, err := ReadUnverified(dir)
	if err != nil {
		// Under a lock, absent provenance is a conflict rather than
		// a lesser state: the lock names bytes that nothing on disk
		// attests. Classifying it here keeps the policy in the one
		// verifier both the gate and the cache-hit path call,
		// instead of leaving each to reinterpret the error. ErrAbsent
		// stays in the chain for callers that distinguish it.
		return Record{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if mismatch := firstMismatch(got, recordFrom(want, wantDigest)); mismatch != "" {
		return Record{}, fmt.Errorf("%s: %w: %s", dir, ErrInvalid, mismatch)
	}
	return got, nil
}

// firstMismatch names the first field on which a stored record and
// the expected one disagree, so the error says what is wrong rather
// than that something is.
//
// graph_digest is checked last, deliberately. It changes whenever
// anything below it changes, so checking it first would report it
// for every drift and name the symptom instead of the cause. It is
// the right answer only when every directly comparable field
// already agrees, which is exactly the case where the closure
// beneath this node moved.
func firstMismatch(got, want Record) string {
	scalars := []struct {
		field     string
		got, want string
	}{
		{"name", got.Name, want.Name},
		{"version", got.Version, want.Version},
		{"platform", got.Platform, want.Platform},
		{"sha256", got.SHA256, want.SHA256},
		{"manifest_digest", got.ManifestDigest, want.ManifestDigest},
		{"method", got.Method, want.Method},
	}
	for _, s := range scalars {
		if s.got != s.want {
			return fmt.Sprintf("%s is %q, lock says %q", s.field, s.got, s.want)
		}
	}
	lists := []struct {
		field     string
		got, want []string
	}{
		{"runtime_deps", got.RuntimeDeps, want.RuntimeDeps},
		{"build_deps", got.BuildDeps, want.BuildDeps},
	}
	for _, l := range lists {
		// slices.Equal, not reflect.DeepEqual: a nil list and an
		// empty one are the same record. TOML omits an empty array
		// on write and yields nil on read, so the distinction cannot
		// survive a round trip and must not be a conflict.
		if !slices.Equal(l.got, l.want) {
			return fmt.Sprintf("%s is %v, lock says %v", l.field, l.got, l.want)
		}
	}
	if got.GraphDigest != want.GraphDigest {
		return fmt.Sprintf(
			"graph_digest is %q, lock says %q; every directly compared "+
				"field agrees, so the closure beneath this package changed",
			got.GraphDigest, want.GraphDigest,
		)
	}
	return ""
}

// VerifyAgainstStore reads a store directory's provenance and
// verifies it against the closure **currently installed**:
// structure, that the record describes the identity it was found
// under, that it is for the expected platform, and that its graph
// digest survives recomputation from the dependencies presently on
// disk, recursively.
//
// The name is long because the limitation is real and easy to walk
// into. Verification here needs every serialized dependency to
// still be installed, so a source record whose build dependency has
// since been collected fails, despite being perfectly valid. That
// is correct at install time, where the closure was just committed
// in topological order, and wrong everywhere else.
//
// Lock-aware callers must not use it. The activation gate and the
// locked cache-hit path compare a stored record against the graph
// digest recomputed from the *lock*, which is what binds the build
// closure a source artifact was produced from without requiring
// that closure to still exist. Those callers use ReadUnverified
// plus that comparison.
func VerifyAgainstStore(storeRoot, name, version, platform string) (Record, error) {
	if err := CheckIdentity(name, version); err != nil {
		return Record{}, err
	}
	return newResolver(storeRoot, platform).verify(name, version)
}

// resolver verifies dependency digests bottom-up, memoizing both
// outcomes. Verification is recursive, so a diamond would otherwise
// re-walk a shared subtree once per path.
type resolver struct {
	store    *store.Store
	platform string
	// cache holds both outcomes, so neither a verified digest nor a
	// proven-unusable dependency is recomputed.
	cache map[string]outcome
	// open marks the current recursion path. A cycle means a
	// corrupt store, so every node on it is unusable rather than
	// collapsed.
	open map[string]bool
}

// outcome is a memoized verification result. Failures are cached
// too: on a legacy machine most dependencies fail, and re-walking
// them once per parent is the common case, not the rare one.
type outcome struct {
	digest string
	err    error
}

func newResolver(storeRoot, platform string) *resolver {
	return &resolver{
		store:    store.NewStore(storeRoot),
		platform: platform,
		cache:    make(map[string]outcome),
		open:     make(map[string]bool),
	}
}

// edgeDigests collects the digest of every edge that verifies.
// Unusable dependencies are simply omitted: lockgraph.Digest then
// reports ErrMissingDep for the edges that actually matter, so the
// serialization rule stays in one place.
func (rs *resolver) edgeDigests(edges []lockgraph.Edge) map[string]string {
	digests := make(map[string]string, len(edges))
	for _, e := range edges {
		if d, err := rs.digest(e.Name, e.Version); err == nil {
			digests[lockgraph.Key(e.Name, e.Version)] = d
		}
	}
	return digests
}

// digest returns a dependency's verified digest, memoizing both
// outcomes.
func (rs *resolver) digest(name, version string) (string, error) {
	key := lockgraph.Key(name, version)
	if o, cached := rs.cache[key]; cached {
		return o.digest, o.err
	}
	r, err := rs.verify(name, version)
	rs.cache[key] = outcome{digest: r.GraphDigest, err: err}
	return r.GraphDigest, err
}

// verify reads a record and recomputes its digest from its own
// closure. store.ResolveDir does the lookup rather than a raw path
// join: a canonical "<v>-1" identity legitimately lives in a bare
// "<v>" directory for installs that predate revisions, and a join
// would report every one of those as unprovenanced. Resolution stays
// deterministic because the identity is already canonical, so the
// resolver's bare-version rules cannot fire.
func (rs *resolver) verify(name, version string) (Record, error) {
	key := lockgraph.Key(name, version)
	if rs.open[key] {
		return Record{}, fmt.Errorf(
			"%w: dependency cycle through %s", ErrInvalid, key,
		)
	}
	rs.open[key] = true
	defer delete(rs.open, key)

	dir := rs.store.ResolveDir(name, version)
	r, err := ReadUnverified(dir)
	if err != nil {
		return Record{}, err
	}
	// The record must describe the identity it was found under. A
	// mismatch means a moved, copied, or hand-edited store dir, and
	// adopting its digest would attribute one package's closure to
	// another.
	if r.Key() != key {
		return Record{}, fmt.Errorf(
			"%s: %w: record names %s", dir, ErrInvalid, r.Key(),
		)
	}
	if r.Platform != rs.platform {
		return Record{}, fmt.Errorf(
			"%s: %w: record is for %s, want %s",
			dir, ErrInvalid, r.Platform, rs.platform,
		)
	}
	n := r.node()
	recomputed, err := lockgraph.Digest(n, rs.edgeDigests(n.Edges))
	if err != nil {
		return Record{}, fmt.Errorf("%s: %w: %w", dir, ErrInvalid, err)
	}
	if recomputed != r.GraphDigest {
		return Record{}, fmt.Errorf(
			"%s: %w: graph_digest does not match its own closure",
			dir, ErrInvalid,
		)
	}
	return r, nil
}

// edgeKeys lists the node's edges of one kind as canonical
// identifiers, sorted.
//
// Sorting is required, not cosmetic. lockgraph.Digest sorts its
// edge lines, so permuting a node's edges leaves the digest
// identical; without sorting here the same graph would produce
// records that differ only in array order, and a comparator using
// slice equality would call that an integrity conflict. Callers
// also build edge lists from maps, whose order is unstable by
// design.
//
// Duplicates are preserved rather than collapsed: lockgraph
// serializes a repeated edge twice, so removing one here would make
// the record describe a different graph from the one the digest
// covers.
func edgeKeys(n lockgraph.Node, kind lockgraph.Kind) []string {
	var keys []string
	for _, e := range n.Edges {
		if e.Kind == kind {
			keys = append(keys, lockgraph.Key(e.Name, e.Version))
		}
	}
	slices.Sort(keys)
	return keys
}

// edgeFromKey splits a canonical identifier back into an edge.
// Total on a validated record: validate proves every entry has the
// name@version shape.
func edgeFromKey(kind lockgraph.Kind, key string) lockgraph.Edge {
	name, version, _ := strings.Cut(key, "@")
	return lockgraph.Edge{Kind: kind, Name: name, Version: version}
}

// CheckIdentity rejects an identity that is not spelled
// name@<version>-<revision>, and that does not address exactly one
// store directory. store.HasNumericRevisionSuffix is the repo's
// canonical classifier for the revision spelling; this package does
// not keep its own copy.
//
// It is exported so a caller assembling a node can screen dependency
// identities it read from disk before New sees them. New treats a
// noncanonical identity as a caller error, which is right for the node
// itself and wrong for a dependency: an edge gale cannot even name is
// unusable, and under the all-or-nothing rule that means no record
// rather than a failed install.
//
// The path-component rule is a boundary, not a style check.
// Identities reach here from provenance files on disk, which are
// untrusted input, and resolution joins them onto the store root.
// A dependency named "../../outside" would otherwise resolve and be
// read from anywhere the process can reach.
func CheckIdentity(name, version string) error {
	if strings.Contains(name, "@") {
		return fmt.Errorf("%w: name %q contains @", ErrNotCanonical, name)
	}
	if !safeComponent(name) {
		return fmt.Errorf(
			"%w: name %q is not a single path component",
			ErrNotCanonical, name,
		)
	}
	if !safeComponent(version) {
		return fmt.Errorf(
			"%w: version %q is not a single path component",
			ErrNotCanonical, version,
		)
	}
	if !store.HasNumericRevisionSuffix(version) {
		return fmt.Errorf(
			"%w: %s has no revision suffix", ErrNotCanonical,
			lockgraph.Key(name, version),
		)
	}
	return nil
}

// safeComponent reports whether s addresses exactly one directory
// entry: non-empty, no separator, and not a traversal element.
func safeComponent(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	if strings.ContainsAny(s, `/\`) {
		return false
	}
	// Belt and braces against anything Clean would rewrite, so the
	// string that is validated is the string that is joined.
	return filepath.Clean(s) == s
}
