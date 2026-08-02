package lockgraph

import (
	"errors"
	"strings"
	"testing"
)

// leafNode is the fixture whose digest was computed independently
// (outside Go) from the serialization in docs: a source-built leaf
// with no edges and no manifest digest.
func leafNode() Node {
	return Node{
		Name:    "oniguruma",
		Version: "6.9.10-1",
		GOOS:    "darwin",
		GOARCH:  "arm64",
		Method:  MethodSource,
		SHA256:  "aaaa",
	}
}

// parentNode depends on leafNode at runtime.
func parentNode() Node {
	return Node{
		Name:           "jq",
		Version:        "1.8.1-2",
		GOOS:           "darwin",
		GOARCH:         "arm64",
		Method:         MethodBinary,
		SHA256:         "bbbb",
		ManifestDigest: "sha256:cccc",
		Edges: []Edge{
			{Kind: KindRuntime, Name: "oniguruma", Version: "6.9.10-1"},
		},
	}
}

const (
	leafDigest   = "sha256:10cdab3aa30b92dcd771dbc69ffb556b2d126fa95af337f52bc5e04ebd239f9e"
	parentDigest = "sha256:4a0688630953a808ba3c53c5be58b8a34e08332052595c8b6467ad2884315535"
)

// TestDigestGolden pins the byte serialization against values
// computed outside this package. A change here means the on-disk
// graph_digest format changed and every lockfile is invalidated.
func TestDigestGolden(t *testing.T) {
	got, err := Digest(leafNode(), nil)
	if err != nil {
		t.Fatalf("Digest(leaf): %v", err)
	}
	if got != leafDigest {
		t.Errorf("leaf digest = %s, want %s", got, leafDigest)
	}

	deps := map[string]string{Key("oniguruma", "6.9.10-1"): leafDigest}
	got, err = Digest(parentNode(), deps)
	if err != nil {
		t.Fatalf("Digest(parent): %v", err)
	}
	if got != parentDigest {
		t.Errorf("parent digest = %s, want %s", got, parentDigest)
	}
}

// TestDigestEdgeOrderStable covers acceptance test 25: permuting
// the input edges must not change the digest, because
// serialization sorts them.
func TestDigestEdgeOrderStable(t *testing.T) {
	deps := map[string]string{
		Key("a", "1-1"): "sha256:aa",
		Key("b", "2-1"): "sha256:bb",
		Key("c", "3-1"): "sha256:cc",
	}
	forward := Node{
		Name: "n", Version: "1-1", GOOS: "linux", GOARCH: "amd64",
		Method: MethodSource, SHA256: "ffff",
		Edges: []Edge{
			{Kind: KindRuntime, Name: "a", Version: "1-1"},
			{Kind: KindBuild, Name: "b", Version: "2-1"},
			{Kind: KindRuntime, Name: "c", Version: "3-1"},
		},
	}
	reversed := forward
	reversed.Edges = []Edge{
		{Kind: KindRuntime, Name: "c", Version: "3-1"},
		{Kind: KindBuild, Name: "b", Version: "2-1"},
		{Kind: KindRuntime, Name: "a", Version: "1-1"},
	}

	first, err := Digest(forward, deps)
	if err != nil {
		t.Fatalf("Digest(forward): %v", err)
	}
	second, err := Digest(reversed, deps)
	if err != nil {
		t.Fatalf("Digest(reversed): %v", err)
	}
	if first != second {
		t.Errorf("edge order changed digest: %s != %s", first, second)
	}
}

// TestDigestFieldSensitivity covers the other half of acceptance
// test 25: every serialized field must affect the digest.
func TestDigestFieldSensitivity(t *testing.T) {
	deps := map[string]string{Key("oniguruma", "6.9.10-1"): leafDigest}
	base, err := Digest(parentNode(), deps)
	if err != nil {
		t.Fatalf("Digest(base): %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Node)
		deps   map[string]string
	}{
		{"name", func(n *Node) { n.Name = "jql" }, deps},
		{"version", func(n *Node) { n.Version = "1.8.1-3" }, deps},
		{"goos", func(n *Node) { n.GOOS = "linux" }, deps},
		{"goarch", func(n *Node) { n.GOARCH = "amd64" }, deps},
		{"method", func(n *Node) { n.Method = MethodSource }, deps},
		{"sha256", func(n *Node) { n.SHA256 = "bbbc" }, deps},
		{"manifest digest", func(n *Node) { n.ManifestDigest = "sha256:cccd" }, deps},
		{"edge kind", func(n *Node) { n.Edges[0].Kind = KindBuild }, deps},
		{"edge name", func(n *Node) {
			n.Edges[0].Name = "onig"
		}, map[string]string{Key("onig", "6.9.10-1"): leafDigest}},
		{"edge version", func(n *Node) {
			n.Edges[0].Version = "6.9.11-1"
		}, map[string]string{Key("oniguruma", "6.9.11-1"): leafDigest}},
		{"dep digest", nil, map[string]string{
			Key("oniguruma", "6.9.10-1"): "sha256:different",
		}},
		{"edge removed", func(n *Node) { n.Edges = nil }, deps},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := parentNode()
			if tt.mutate != nil {
				tt.mutate(&n)
			}
			got, err := Digest(n, tt.deps)
			if err != nil {
				t.Fatalf("Digest: %v", err)
			}
			if got == base {
				t.Errorf("mutating %s left the digest unchanged", tt.name)
			}
		})
	}
}

// TestDigestBuildEdgesOnlyForSource: build edges are serialized
// only when the node's own method is source (design section 5).
func TestDigestBuildEdgesOnlyForSource(t *testing.T) {
	deps := map[string]string{Key("autoconf", "2.72-1"): "sha256:ac"}
	withBuildEdge := Node{
		Name: "n", Version: "1-1", GOOS: "darwin", GOARCH: "arm64",
		Method: MethodBinary, SHA256: "ffff",
		Edges: []Edge{{Kind: KindBuild, Name: "autoconf", Version: "2.72-1"}},
	}
	withoutEdge := withBuildEdge
	withoutEdge.Edges = nil

	a, err := Digest(withBuildEdge, deps)
	if err != nil {
		t.Fatalf("Digest(binary with build edge): %v", err)
	}
	b, err := Digest(withoutEdge, deps)
	if err != nil {
		t.Fatalf("Digest(binary without edge): %v", err)
	}
	if a != b {
		t.Errorf("binary node serialized a build edge: %s != %s", a, b)
	}

	src := withBuildEdge
	src.Method = MethodSource
	c, err := Digest(src, deps)
	if err != nil {
		t.Fatalf("Digest(source with build edge): %v", err)
	}
	srcNoEdge := src
	srcNoEdge.Edges = nil
	d, err := Digest(srcNoEdge, deps)
	if err != nil {
		t.Fatalf("Digest(source without edge): %v", err)
	}
	if c == d {
		t.Error("source node ignored its build edge")
	}
}

func TestDigestMissingDepDigest(t *testing.T) {
	_, err := Digest(parentNode(), nil)
	if !errors.Is(err, ErrMissingDep) {
		t.Fatalf("err = %v, want ErrMissingDep", err)
	}
	if !strings.Contains(err.Error(), "oniguruma@6.9.10-1") {
		t.Errorf("error does not name the dep: %v", err)
	}
}

// chain builds A -> B -> C, all runtime edges.
func chain(cSHA string) map[string]Node {
	return map[string]Node{
		Key("a", "1-1"): {
			Name: "a", Version: "1-1", GOOS: "darwin", GOARCH: "arm64",
			Method: MethodSource, SHA256: "a1",
			Edges: []Edge{{Kind: KindRuntime, Name: "b", Version: "1-1"}},
		},
		Key("b", "1-1"): {
			Name: "b", Version: "1-1", GOOS: "darwin", GOARCH: "arm64",
			Method: MethodSource, SHA256: "b1",
			Edges: []Edge{{Kind: KindRuntime, Name: "c", Version: "1-1"}},
		},
		Key("c", "1-1"): {
			Name: "c", Version: "1-1", GOOS: "darwin", GOARCH: "arm64",
			Method: MethodSource, SHA256: cSHA,
		},
	}
}

// TestClosurePropagatesToGrandparent covers acceptance test 13: a
// dependency whose SHA changed while its version-revision did not
// must change the digest of nodes above it, not only its direct
// parent.
func TestClosurePropagatesToGrandparent(t *testing.T) {
	before, _, err := Closure(chain("c1"))
	if err != nil {
		t.Fatalf("Closure(before): %v", err)
	}
	after, _, err := Closure(chain("c2"))
	if err != nil {
		t.Fatalf("Closure(after): %v", err)
	}

	for _, key := range []string{Key("a", "1-1"), Key("b", "1-1"), Key("c", "1-1")} {
		if before[key] == after[key] {
			t.Errorf("%s digest unchanged after C's SHA changed", key)
		}
	}
}

func TestClosureTopologicalOrder(t *testing.T) {
	_, order, err := Closure(chain("c1"))
	if err != nil {
		t.Fatalf("Closure: %v", err)
	}
	pos := make(map[string]int, len(order))
	for i, k := range order {
		pos[k] = i
	}
	if len(order) != 3 {
		t.Fatalf("order = %v, want 3 entries", order)
	}
	if pos[Key("c", "1-1")] > pos[Key("b", "1-1")] {
		t.Errorf("c must come before b: %v", order)
	}
	if pos[Key("b", "1-1")] > pos[Key("a", "1-1")] {
		t.Errorf("b must come before a: %v", order)
	}
}

// TestClosureRejectsCycle covers acceptance test 24: plan
// construction needs a topological order, which does not exist for
// a cyclic graph, so the cycle is named and rejected.
func TestClosureRejectsCycle(t *testing.T) {
	nodes := chain("c1")
	c := nodes[Key("c", "1-1")]
	c.Edges = []Edge{{Kind: KindRuntime, Name: "a", Version: "1-1"}}
	nodes[Key("c", "1-1")] = c

	_, _, err := Closure(nodes)
	if !errors.Is(err, ErrCycle) {
		t.Fatalf("err = %v, want ErrCycle", err)
	}
	for _, want := range []string{"a@1-1", "b@1-1", "c@1-1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("cycle error does not name %s: %v", want, err)
		}
	}
}

func TestClosureMissingNode(t *testing.T) {
	nodes := map[string]Node{Key("a", "1-1"): chain("c1")[Key("a", "1-1")]}
	_, _, err := Closure(nodes)
	if !errors.Is(err, ErrMissingDep) {
		t.Fatalf("err = %v, want ErrMissingDep", err)
	}
	if !strings.Contains(err.Error(), "b@1-1") {
		t.Errorf("error does not name the missing node: %v", err)
	}
}
