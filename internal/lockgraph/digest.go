// Package lockgraph computes the graph digest that binds a locked
// artifact to the exact closure it was produced from.
//
// Locked dependency edges name a package by identity alone
// (name@version-revision), which is not enough: a refresh can
// change a dependency's artifact bytes without changing that
// identifier, and a parent built against the new bytes would still
// look unchanged. The digest is therefore recursive — an edge
// carries the dependency's own digest, so a change anywhere below
// a node changes that node's digest.
//
// The serialization is a persisted format. Changing any byte of it
// invalidates every lockfile and every provenance file on disk.
package lockgraph

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Install methods a locked artifact can carry.
const (
	MethodBinary = "binary"
	MethodSource = "source"
)

// Kind distinguishes a runtime dependency edge from a build-only
// one. Build edges are serialized only when the node's own method
// is source, because a prebuilt binary was not produced from them.
type Kind string

// Edge kinds.
const (
	KindRuntime Kind = "runtime"
	KindBuild   Kind = "build"
)

// prefix guards against a future serialization being mistaken for
// this one.
const prefix = "gale-graph-v1\n"

var (
	// ErrMissingDep reports an edge whose dependency digest was
	// not supplied, or a closure node an edge points at that is
	// absent from the graph.
	ErrMissingDep = errors.New("dependency digest not available")

	// ErrCycle reports a dependency cycle. A cyclic graph has no
	// topological order, so neither the digest nor the install
	// order the lock depends on can be computed.
	ErrCycle = errors.New("dependency cycle")
)

// Edge is one dependency of a node, identified the way the lockfile
// identifies it.
type Edge struct {
	Kind    Kind
	Name    string
	Version string // canonical version-revision
}

// Node is one locked artifact: a package at one version, for one
// platform, produced one way, from one set of dependencies.
type Node struct {
	Name           string
	Version        string // canonical version-revision
	GOOS           string
	GOARCH         string
	Method         string // MethodBinary or MethodSource
	SHA256         string // bare lowercase hex
	ManifestDigest string // empty when absent
	Edges          []Edge
}

// Key is the identifier a node is stored and referenced under.
func Key(name, version string) string {
	return name + "@" + version
}

// key is the node's own identifier.
func (n Node) key() string {
	return Key(n.Name, n.Version)
}

// Digest computes a node's graph digest. deps maps each edge's Key
// to that dependency's own digest; edges the node does not
// serialize need no entry.
func Digest(n Node, deps map[string]string) (string, error) {
	var b strings.Builder
	b.WriteString(prefix)
	b.WriteString("node\x00")
	b.WriteString(n.Name)
	b.WriteString("\x00")
	b.WriteString(n.Version)
	b.WriteString("\x00")
	b.WriteString(n.GOOS)
	b.WriteString("/")
	b.WriteString(n.GOARCH)
	b.WriteString("\x00")
	b.WriteString(n.Method)
	b.WriteString("\x00")
	b.WriteString(n.SHA256)
	b.WriteString("\x00")
	b.WriteString(n.ManifestDigest)
	b.WriteString("\n")

	lines, err := edgeLines(n, deps)
	if err != nil {
		return "", err
	}
	for _, line := range lines {
		b.WriteString(line)
	}

	sum := sha256.Sum256([]byte(b.String()))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// edgeLines serializes the node's serialized edges, sorted in
// ascending bytewise order of the whole line.
func edgeLines(n Node, deps map[string]string) ([]string, error) {
	lines := make([]string, 0, len(n.Edges))
	for _, e := range n.Edges {
		if !serializes(n, e) {
			continue
		}
		key := Key(e.Name, e.Version)
		digest, ok := deps[key]
		if !ok {
			return nil, fmt.Errorf(
				"edge %s of %s: %w", key, n.key(), ErrMissingDep,
			)
		}
		lines = append(lines, "edge\x00"+string(e.Kind)+"\x00"+
			e.Name+"\x00"+e.Version+"\x00"+digest+"\n")
	}
	sort.Strings(lines)
	return lines, nil
}

// serializes reports whether an edge contributes to its node's
// digest. A binary artifact was not produced from its build deps,
// so those edges are excluded.
func serializes(n Node, e Edge) bool {
	return e.Kind != KindBuild || n.Method == MethodSource
}

// Closure computes the digest of every node bottom-up and returns
// them alongside a topological order, dependencies first. That
// order is the order installs must commit in, so parents build
// against dependencies already at their final store paths.
//
// Cycles are rejected rather than collapsed: the commit order the
// lock depends on does not exist for a cyclic graph.
func Closure(nodes map[string]Node) (map[string]string, []string, error) {
	digests := make(map[string]string, len(nodes))
	order := make([]string, 0, len(nodes))
	state := make(map[string]int, len(nodes)) // 0 unseen, 1 open, 2 done

	var visit func(key string, stack []string) error
	visit = func(key string, stack []string) error {
		switch state[key] {
		case 2:
			return nil
		case 1:
			return fmt.Errorf("%w: %s", ErrCycle,
				strings.Join(append(stack, key), " -> "))
		}
		n, ok := nodes[key]
		if !ok {
			return fmt.Errorf("node %s: %w", key, ErrMissingDep)
		}
		state[key] = 1
		stack = append(stack, key)
		for _, e := range n.Edges {
			if !serializes(n, e) {
				continue
			}
			if err := visit(Key(e.Name, e.Version), stack); err != nil {
				return err
			}
		}
		digest, err := Digest(n, digests)
		if err != nil {
			return err
		}
		digests[key] = digest
		order = append(order, key)
		state[key] = 2
		return nil
	}

	for _, key := range sortedKeys(nodes) {
		if err := visit(key, nil); err != nil {
			return nil, nil, err
		}
	}
	return digests, order, nil
}

// sortedKeys keeps the walk deterministic so a cycle error names
// the same entry point on every run.
func sortedKeys(nodes map[string]Node) []string {
	keys := make([]string, 0, len(nodes))
	for k := range nodes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
