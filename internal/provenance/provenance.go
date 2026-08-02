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
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

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
//
// Absence is established by an O_NOFOLLOW open, and anything that is
// not a regular file is ErrInvalid. Neither os.ReadFile nor a
// preceding Lstat is enough: ReadFile follows a symlink, and a
// DANGLING one fails with ENOENT, so a path that plainly exists
// would be reported as no provenance at all — and §13's replacement
// path acts on exactly that answer. Checking with Lstat and then
// reading by path leaves the same hole one scheduling window wide,
// since the file can become a link in between. One descriptor
// answers both questions about the same object.
//
// A resolvable link is refused too, for the reason one is never
// written: the record must describe the directory it sits in, and a
// link's target is chosen by whoever planted it.
func ReadUnverified(dir string) (Record, error) {
	path := filepath.Join(dir, File)
	// O_NONBLOCK because the type check comes AFTER the open, and an
	// O_RDONLY open of a FIFO waits for a writer: a planted pipe
	// would hang the caller forever instead of being rejected as a
	// non-regular file. It is ignored for regular files.
	f, err := os.OpenFile(
		path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0,
	)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Record{}, fmt.Errorf("%s: %w", dir, ErrAbsent)
		}
		// ELOOP is O_NOFOLLOW refusing a symlink, which is a planted
		// record rather than an unreadable one.
		if errors.Is(err, syscall.ELOOP) {
			return Record{}, fmt.Errorf(
				"%s: %w: a symlink, not a record", path, ErrInvalid,
			)
		}
		return Record{}, fmt.Errorf("open provenance: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Record{}, fmt.Errorf("stat provenance: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return Record{}, fmt.Errorf(
			"%s: %w: not a regular file (%s)", path, ErrInvalid, fi.Mode().Type(),
		)
	}
	data, err := io.ReadAll(f)
	if err != nil {
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
	if err := store.CheckIdentity(n.Name, n.Version); err != nil {
		return Record{}, err
	}
	for _, e := range n.Edges {
		if err := store.CheckIdentity(e.Name, e.Version); err != nil {
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
	if err := store.CheckIdentity(name, version); err != nil {
		return Record{}, err
	}
	return newResolver(storeRoot, platform).verify(name, version)
}

// Closure verifies every record reachable from roots and returns them
// keyed by canonical identity. Roots map a package name to its
// canonical version-revision, which is the shape a caller already
// holds after resolving the roots it means to lock.
//
// The lock writers need the whole closure, not one node per root: an
// artifact is emitted per node, and the roots alone do not name the
// transitive dependencies the graph digest binds. One resolver serves
// every root, so a diamond is walked once and every node in one
// closure is judged against the same memoized outcomes.
//
// It carries VerifyAgainstStore's precondition, and for the same
// reason: each record's digest is recomputed from the closure
// currently installed. That holds at write time, when the closure was
// just installed. It stops holding once build dependencies are
// collected, so a caller relocking an old closure must install it
// rather than reach for this.
func Closure(storeRoot, platform string, roots map[string]string) (map[string]Record, error) {
	rs := newResolver(storeRoot, platform)
	out := make(map[string]Record, len(roots))
	// Sorted so a closure with two unusable roots names the same one
	// on every run.
	for _, name := range slices.Sorted(maps.Keys(roots)) {
		version := roots[name]
		if err := store.CheckIdentity(name, version); err != nil {
			return nil, err
		}
		if err := rs.collect(name, version, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// collect adds one identity's record and every record below it.
//
// Recursing over the record's own edges needs no serialization rule:
// validate proves a binary record carries no build dependencies, so a
// validated record's edges are already exactly the set lockgraph
// serializes. Re-deriving that rule here would be a second copy of it,
// free to drift.
func (rs *resolver) collect(name, version string, out map[string]Record) error {
	key := lockgraph.Key(name, version)
	if _, done := out[key]; done {
		return nil
	}
	r, err := rs.record(name, version)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	out[key] = r
	for _, e := range r.node().Edges {
		if err := rs.collect(e.Name, e.Version, out); err != nil {
			return err
		}
	}
	return nil
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
//
// The record is memoized beside the digest so a caller that needs the
// nodes themselves, not just their digests, cannot walk the closure a
// second time and disagree with this one about what is in it.
type outcome struct {
	record Record
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
	r, err := rs.record(name, version)
	return r.GraphDigest, err
}

// record returns a verified record, memoizing both outcomes. Every
// memoized read goes through here so the digest path and the closure
// path share one cache and one verification.
func (rs *resolver) record(name, version string) (Record, error) {
	key := lockgraph.Key(name, version)
	if o, cached := rs.cache[key]; cached {
		return o.record, o.err
	}
	r, err := rs.verify(name, version)
	rs.cache[key] = outcome{record: r, digest: r.GraphDigest, err: err}
	return r, err
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
