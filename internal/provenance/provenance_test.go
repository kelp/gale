package provenance

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/kelp/gale/internal/lockgraph"
)

// Hashes are the real shape, 64 hex digits, because validation
// rejects anything else and a placeholder would only pass by
// weakening the check it is meant to exercise.
const (
	shaJQ   = "1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a"
	shaOnig = "2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b"
	digestM = "sha256:3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c"
)

func onigNode() lockgraph.Node {
	return lockgraph.Node{
		Name:    "oniguruma",
		Version: "6.9.10-1",
		GOOS:    "darwin",
		GOARCH:  "arm64",
		Method:  lockgraph.MethodBinary,
		SHA256:  shaOnig,
	}
}

func jqNode() lockgraph.Node {
	return lockgraph.Node{
		Name:           "jq",
		Version:        "1.8.1-2",
		GOOS:           "darwin",
		GOARCH:         "arm64",
		Method:         lockgraph.MethodBinary,
		SHA256:         shaJQ,
		ManifestDigest: digestM,
		Edges: []lockgraph.Edge{
			{Kind: lockgraph.KindRuntime, Name: "oniguruma", Version: "6.9.10-1"},
		},
	}
}

// recordFor builds the record a node would produce, computing the
// digest directly through lockgraph rather than through New, so
// fixtures never depend on the function under test.
func recordFor(t *testing.T, n lockgraph.Node, deps map[string]string) Record {
	t.Helper()
	digest, err := lockgraph.Digest(n, deps)
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	return Record{
		Name:           n.Name,
		Version:        n.Version,
		Platform:       n.GOOS + "/" + n.GOARCH,
		SHA256:         n.SHA256,
		ManifestDigest: n.ManifestDigest,
		Method:         n.Method,
		RuntimeDeps:    edgeKeys(n, lockgraph.KindRuntime),
		BuildDeps:      edgeKeys(n, lockgraph.KindBuild),
		GraphDigest:    digest,
	}
}

// installAt writes a record into an explicit store directory name,
// so tests can exercise a canonical "-1" identity living in a bare
// directory.
func installAt(t *testing.T, storeRoot, name, dir string, r Record) {
	t.Helper()
	path := filepath.Join(storeRoot, name, dir)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir store dir: %v", err)
	}
	if err := Write(path, r); err != nil {
		t.Fatalf("Write: %v", err)
	}
}

// encodeForTest serializes a record without validating it, so the
// invalid-fixture table can put records on disk that Write itself
// would refuse.
func encodeForTest(t *testing.T, r Record) string {
	t.Helper()
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(r); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	return buf.String()
}

func writeRaw(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, File), []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := recordFor(t, onigNode(), nil)
	if err := Write(dir, want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := ReadUnverified(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip changed the record:\n got %#v\nwant %#v", got, want)
	}
}

// TestReadAbsentIsDistinct: absent and unusable are different
// states. gale lock may populate an absent canonical directory and
// may not adopt an occupied unprovenanced one, so a reader that
// returned a zero value for a missing file (as depsmeta.Read does)
// would erase the distinction the whole migration path rests on.
func TestReadAbsentIsDistinct(t *testing.T) {
	_, err := ReadUnverified(t.TempDir())
	if !errors.Is(err, ErrAbsent) {
		t.Fatalf("err = %v, want ErrAbsent", err)
	}
	if errors.Is(err, ErrInvalid) {
		t.Error("absent must not also report ErrInvalid")
	}
}

// TestReadUnreadableIsNotAbsence: only a missing file is absence.
// An I/O or permission failure reported as "no provenance" would
// make a directory look replaceable to the migration path when
// gale simply could not read it.
func TestReadUnreadableIsNotAbsence(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits this test relies on")
	}
	dir := t.TempDir()
	if err := Write(dir, recordFor(t, onigNode(), nil)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := os.Chmod(filepath.Join(dir, File), 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	_, err := ReadUnverified(dir)
	if err == nil {
		t.Fatal("Read succeeded on an unreadable file")
	}
	if errors.Is(err, ErrAbsent) {
		t.Errorf("unreadable reported as absent: %v", err)
	}
}

// TestReadInvalid: a record is usable only if it validates in full.
// The cases are examples of that rule, not the rule itself: a
// closed list would go stale the first time a field is added, which
// is why validation checks every required field rather than these
// specific ones.
func TestReadInvalid(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Record)
		content string // used instead of mutate when non-empty
	}{
		{name: "unparseable", content: "name = \n"},
		{name: "unknown field", content: "name = \"jq\"\nstray = true\n"},
		{name: "no name", mutate: func(r *Record) { r.Name = "" }},
		{name: "no version", mutate: func(r *Record) { r.Version = "" }},
		{name: "no platform", mutate: func(r *Record) { r.Platform = "" }},
		{name: "no sha256", mutate: func(r *Record) { r.SHA256 = "" }},
		{name: "no method", mutate: func(r *Record) { r.Method = "" }},
		{
			name:   "no graph digest",
			mutate: func(r *Record) { r.GraphDigest = "" },
		},
		{
			name:   "unknown method",
			mutate: func(r *Record) { r.Method = "magic" },
		},
		{
			name:   "malformed platform",
			mutate: func(r *Record) { r.Platform = "darwin" },
		},
		{
			name:   "noncanonical version",
			mutate: func(r *Record) { r.Version = "6.9.10" },
		},
		{
			name:   "truncated sha256",
			mutate: func(r *Record) { r.SHA256 = "abcd" },
		},
		{
			name:   "uppercase sha256",
			mutate: func(r *Record) { r.SHA256 = strings.ToUpper(shaOnig) },
		},
		{
			name:   "unprefixed graph digest",
			mutate: func(r *Record) { r.GraphDigest = shaOnig },
		},
		{
			name:   "malformed manifest digest",
			mutate: func(r *Record) { r.ManifestDigest = "sha256:nope" },
		},
		{
			name:   "dependency is not name@version",
			mutate: func(r *Record) { r.RuntimeDeps = []string{"zlib"} },
		},
		{
			name:   "noncanonical dependency",
			mutate: func(r *Record) { r.RuntimeDeps = []string{"zlib@1.3"} },
		},
		{
			// A prebuilt was not produced from build deps here, so
			// recording any would attest something never verified.
			name:   "binary carries build deps",
			mutate: func(r *Record) { r.BuildDeps = []string{"autoconf@2.72-1"} },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			content := tt.content
			if content == "" {
				r := recordFor(t, onigNode(), nil)
				tt.mutate(&r)
				content = encodeForTest(t, r)
			}
			writeRaw(t, dir, content)
			_, err := ReadUnverified(dir)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			if errors.Is(err, ErrAbsent) {
				t.Error("invalid must not also report ErrAbsent")
			}
		})
	}
}

// TestWriteRejectsInvalid: an incomplete record must never reach
// disk. Provenance is all-or-nothing, so a caller that assembled a
// partial record has a bug, and writing it would create exactly the
// tri-state file the format exists to prevent.
func TestWriteRejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	r := recordFor(t, onigNode(), nil)
	r.GraphDigest = ""
	if err := Write(dir, r); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if _, err := os.Stat(filepath.Join(dir, File)); err == nil {
		t.Error("Write created a file despite rejecting the record")
	}
}

// TestNewRejectsNoncanonical: a bare version would miss its store
// directory and produce a silent "unprovenanced" result
// indistinguishable from a genuinely legacy dependency. Rejecting
// is the difference between a caller bug that is reported and one
// that quietly degrades every record above it.
func TestNewRejectsNoncanonical(t *testing.T) {
	tests := []struct {
		name string
		node func() lockgraph.Node
	}{
		{
			name: "node version",
			node: func() lockgraph.Node {
				n := onigNode()
				n.Version = "6.9.10"
				return n
			},
		},
		{
			name: "edge version",
			node: func() lockgraph.Node {
				n := jqNode()
				n.Edges[0].Version = "6.9.10"
				return n
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(t.TempDir(), tt.node())
			if !errors.Is(err, ErrNotCanonical) {
				t.Fatalf("err = %v, want ErrNotCanonical", err)
			}
		})
	}
}

// TestNewLeaf: a node with no serialized edges is always
// computable. That is what lets provenance spread from the leaves
// up on a machine whose store predates enforcement.
func TestNewLeaf(t *testing.T) {
	got, err := New(t.TempDir(), onigNode())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	want := recordFor(t, onigNode(), nil)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("leaf record = %#v, want %#v", got, want)
	}
}

// TestNewBinaryDropsBuildEdges: a locked binary node legitimately
// carries recipe-derived build edges, but they describe what would
// build the package rather than what produced the bytes fetched
// here. Recording them would attest something never verified, and
// failing on them would make every package with a build-tool recipe
// unprovenanceable.
func TestNewBinaryDropsBuildEdges(t *testing.T) {
	n := onigNode()
	n.Edges = []lockgraph.Edge{
		{Kind: lockgraph.KindBuild, Name: "autoconf", Version: "2.72-1"},
	}
	got, err := New(t.TempDir(), n)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got.BuildDeps != nil {
		t.Errorf("BuildDeps = %v, want none", got.BuildDeps)
	}
	if got.GraphDigest == "" {
		t.Error("no graph digest despite no serialized edges")
	}
}

// TestNewSourceKeepsBuildEdges is the other half: a source artifact
// was produced from its build deps, so it records them and its
// digest serializes them.
func TestNewSourceKeepsBuildEdges(t *testing.T) {
	storeRoot := t.TempDir()
	tool := onigNode()
	tool.Name, tool.Version = "autoconf", "2.72-1"
	installAt(t, storeRoot, "autoconf", "2.72-1", recordFor(t, tool, nil))

	n := onigNode()
	n.Name, n.Version = "jq", "1.8.1-2"
	n.Method = lockgraph.MethodSource
	n.Edges = []lockgraph.Edge{
		{Kind: lockgraph.KindBuild, Name: "autoconf", Version: "2.72-1"},
	}
	got, err := New(storeRoot, n)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !reflect.DeepEqual(got.BuildDeps, []string{"autoconf@2.72-1"}) {
		t.Errorf("BuildDeps = %v, want [autoconf@2.72-1]", got.BuildDeps)
	}
}

// TestVerifyAgainstStoreRejectsWhatReadAccepts pins the split
// between the two readers. ReadUnverified is structural, so a
// well-formed but false digest passes it; VerifyAgainstStore
// recomputes from the store and rejects it. A future caller that
// reaches for the structural reader when it meant the verifying one
// is the failure this distinction exists to make visible.
func TestVerifyAgainstStoreRejectsWhatReadAccepts(t *testing.T) {
	storeRoot := t.TempDir()
	r := recordFor(t, onigNode(), nil)
	r.GraphDigest = digestM // well-formed, and not the real digest
	dir := filepath.Join(storeRoot, "oniguruma", "6.9.10-1")
	installAt(t, storeRoot, "oniguruma", "6.9.10-1", r)

	if _, err := ReadUnverified(dir); err != nil {
		t.Fatalf("ReadUnverified rejected a structurally sound record: %v", err)
	}
	_, err := VerifyAgainstStore(storeRoot, "oniguruma", "6.9.10-1", "darwin/arm64")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("VerifyAgainstStore err = %v, want ErrInvalid", err)
	}
}

// TestVerifyAgainstStoreAcceptsSoundRecord is the positive half, so the test
// above cannot pass by VerifyAgainstStore rejecting everything.
func TestVerifyAgainstStoreAcceptsSoundRecord(t *testing.T) {
	storeRoot := t.TempDir()
	want := recordFor(t, onigNode(), nil)
	installAt(t, storeRoot, "oniguruma", "6.9.10-1", want)

	got, err := VerifyAgainstStore(storeRoot, "oniguruma", "6.9.10-1", "darwin/arm64")
	if err != nil {
		t.Fatalf("VerifyAgainstStore: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("VerifyAgainstStore returned %#v, want %#v", got, want)
	}
}

// TestVerifyAgainstStoreNeedsTheClosureInstalled pins the
// limitation rather than hiding it. A source record whose build
// dependency has since been collected is perfectly valid, and this
// function still fails on it, because it recomputes from what is on
// disk. Design section 12 permits build deps to disappear: the
// activation gate compares the stored digest against one recomputed
// from the lock, not from the store. This test exists so that a
// later reader who finds the failure surprising sees it is intended
// and reaches for ReadUnverified plus a lock comparison instead of
// making this function lenient.
func TestVerifyAgainstStoreNeedsTheClosureInstalled(t *testing.T) {
	storeRoot := t.TempDir()
	tool := onigNode()
	tool.Name, tool.Version = "autoconf", "2.72-1"
	installAt(t, storeRoot, "autoconf", "2.72-1", recordFor(t, tool, nil))

	src := onigNode()
	src.Name, src.Version = "jq", "1.8.1-2"
	src.Method = lockgraph.MethodSource
	src.Edges = []lockgraph.Edge{
		{Kind: lockgraph.KindBuild, Name: "autoconf", Version: "2.72-1"},
	}
	rec, err := New(storeRoot, src)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	installAt(t, storeRoot, "jq", "1.8.1-2", rec)

	if _, err := VerifyAgainstStore(storeRoot, "jq", "1.8.1-2", "darwin/arm64"); err != nil {
		t.Fatalf("VerifyAgainstStore with the closure present: %v", err)
	}

	// gc removes the build tool. The record is unchanged and still
	// truthful about what produced the artifact.
	if err := os.RemoveAll(filepath.Join(storeRoot, "autoconf")); err != nil {
		t.Fatalf("remove build dep: %v", err)
	}
	if _, err := VerifyAgainstStore(storeRoot, "jq", "1.8.1-2", "darwin/arm64"); err == nil {
		t.Fatal("VerifyAgainstStore succeeded without the build dep installed; " +
			"if this is now intended, the gate's contract changed too")
	}
	if _, err := ReadUnverified(filepath.Join(storeRoot, "jq", "1.8.1-2")); err != nil {
		t.Errorf("ReadUnverified must still accept the record: %v", err)
	}
}

// TestNewChain: the digest is recursive, so a parent's record is
// computable exactly when its serialized deps are provenanced, and
// carries their digests rather than their hashes.
func TestNewChain(t *testing.T) {
	storeRoot := t.TempDir()
	dep := recordFor(t, onigNode(), nil)
	installAt(t, storeRoot, "oniguruma", "6.9.10-1", dep)

	got, err := New(storeRoot, jqNode())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	want := recordFor(t, jqNode(), map[string]string{
		"oniguruma@6.9.10-1": dep.GraphDigest,
	})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("chain record = %#v, want %#v", got, want)
	}
}

// TestNewResolvesRevisionOneInBareDir: a canonical "<v>-1" identity
// legitimately lives in a bare "<v>" directory for installs that
// predate revisions (store.resolveVersion rule 3). A raw path join
// would report every pre-revision dependency as unprovenanced,
// which looks exactly like the legacy case it is not.
func TestNewResolvesRevisionOneInBareDir(t *testing.T) {
	storeRoot := t.TempDir()
	dep := recordFor(t, onigNode(), nil)
	installAt(t, storeRoot, "oniguruma", "6.9.10", dep) // bare dir

	got, err := New(storeRoot, jqNode())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	want := recordFor(t, jqNode(), map[string]string{
		"oniguruma@6.9.10-1": dep.GraphDigest,
	})
	if got.GraphDigest != want.GraphDigest {
		t.Errorf("GraphDigest = %q, want %q", got.GraphDigest, want.GraphDigest)
	}
}

// TestNewRecomputesDepDigest: a stored digest is evidence, not
// authority. Believing it would let one hand-written provenance
// file certify an arbitrary closure above itself, which is the
// single thing this record exists to prevent.
func TestNewRecomputesDepDigest(t *testing.T) {
	storeRoot := t.TempDir()
	dep := recordFor(t, onigNode(), nil)
	dep.GraphDigest = digestM // well-formed, and not the real digest
	installAt(t, storeRoot, "oniguruma", "6.9.10-1", dep)

	_, err := New(storeRoot, jqNode())
	if !errors.Is(err, lockgraph.ErrMissingDep) {
		t.Fatalf("err = %v, want lockgraph.ErrMissingDep", err)
	}
}

// TestNewUnusableDepIsUnavailable: unusable dependency provenance
// is treated exactly as unavailable, never as a weaker form of
// usable. Every case must reach the same outcome, so the caller has
// one branch to write rather than a second tri-state one level
// down.
func TestNewUnusableDepIsUnavailable(t *testing.T) {
	depDir := func(t *testing.T, storeRoot string) string {
		t.Helper()
		dir := filepath.Join(storeRoot, "oniguruma", "6.9.10-1")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		return dir
	}
	tests := []struct {
		name  string
		setup func(t *testing.T, storeRoot string)
	}{
		{
			name:  "dep dir absent",
			setup: func(*testing.T, string) {},
		},
		{
			name: "dep dir present without provenance",
			setup: func(t *testing.T, storeRoot string) {
				depDir(t, storeRoot)
			},
		},
		{
			name: "dep provenance unparseable",
			setup: func(t *testing.T, storeRoot string) {
				writeRaw(t, depDir(t, storeRoot), "name = \n")
			},
		},
		{
			name: "dep provenance names another identity",
			setup: func(t *testing.T, storeRoot string) {
				dir := depDir(t, storeRoot)
				if err := Write(dir, recordFor(t, jqNode(), map[string]string{
					"oniguruma@6.9.10-1": digestM,
				})); err != nil {
					t.Fatalf("Write: %v", err)
				}
			},
		},
		{
			name: "dep provenance is another platform",
			setup: func(t *testing.T, storeRoot string) {
				n := onigNode()
				n.GOOS, n.GOARCH = "linux", "amd64"
				if err := Write(depDir(t, storeRoot), recordFor(t, n, nil)); err != nil {
					t.Fatalf("Write: %v", err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storeRoot := t.TempDir()
			tt.setup(t, storeRoot)
			_, err := New(storeRoot, jqNode())
			if !errors.Is(err, lockgraph.ErrMissingDep) {
				t.Fatalf("err = %v, want lockgraph.ErrMissingDep", err)
			}
			if !strings.Contains(err.Error(), "oniguruma@6.9.10-1") {
				t.Errorf("error does not name the edge: %v", err)
			}
		})
	}
}
