// Package lockplan turns a lockfile into the exact set of installs a
// locked operation performs, before any of them run.
//
// The lock is the exclusive selector of versions, artifacts, methods,
// and dependency edges. Recipes are still read, because only a recipe
// knows how to fetch or build a node, but under a lock a recipe never
// selects anything: its metadata is validated against the locked node
// and disagreement is an error.
package lockplan

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/recipe"
)

var (
	// ErrRecipeMismatch reports a recipe that disagrees with the
	// locked node it was resolved for.
	ErrRecipeMismatch = errors.New("recipe disagrees with the lock")

	// ErrDigestMismatch reports a stored graph digest that does not
	// survive recomputation from the locked closure. The sentinel is
	// lockfile's, because the activation gate makes the same
	// comparison of the same file and the two must classify alike;
	// the name stays here so plan callers keep matching on it.
	ErrDigestMismatch = lockfile.ErrDigestMismatch

	// ErrMalformedArtifact reports an artifact outside the persisted
	// format: an unknown method, or a hash that is not the shape the
	// writers emit and the comparators expect.
	ErrMalformedArtifact = errors.New("malformed lock artifact")

	// ErrMalformedPlatform reports a platform that is not GOOS/GOARCH.
	// It is a caller error rather than a lock error, and it matters
	// because the platform is a serialized digest field: a malformed
	// one yields a digest that agrees with nothing.
	ErrMalformedPlatform = errors.New("platform is not GOOS/GOARCH")
)

// Request is everything plan construction needs. Platform is passed
// rather than read from runtime so a plan can be built for a platform
// other than the host's, which the lock writers need.
type Request struct {
	Lock     *lockfile.V1
	Host     string
	Platform string // GOOS/GOARCH
	// Declared is the effective gale.toml package set for Host, which
	// the locked roots must agree with exactly.
	Declared map[string]string
	// DeclaredOrigin maps each declared package to the manifest
	// section supplying its effective pin (config.PackageOrigins).
	// Optional: it only sharpens the stale-lock remedy, and a caller
	// that omits it gets the general wording rather than a wrong
	// command.
	DeclaredOrigin map[string]string
	// Resolve returns the recipe for one canonical identity.
	Resolve func(name, version string) (*recipe.Recipe, error)
}

// Plan is the resolved graph: what to install, and in what order.
type Plan struct {
	// Nodes is keyed by canonical name@version-revision.
	Nodes map[string]Node
	// Order is topological, dependencies first, which is the order
	// installs must commit in.
	Order []string
	// Roots are the declared packages, as canonical identities,
	// sorted.
	Roots []string
}

// Node is one planned install.
type Node struct {
	Name           string
	Version        string // canonical version-revision
	Method         string
	SHA256         string
	ManifestDigest string
	RuntimeDeps    []string
	BuildDeps      []string
	// GraphDigest is recomputed from the locked closure rather than
	// read from the file, so a hand-edited digest cannot certify
	// itself.
	GraphDigest string
	Recipe      *recipe.Recipe
	// Graph is this node in the form the digest was computed over.
	// It is retained rather than rebuilt at the call site because
	// provenance.VerifyAgainstLock takes exactly this, and a second
	// construction of it is a second serializer: the comparator and
	// the writer disagreeing by one field is the reinstall-loop
	// failure mode.
	Graph lockgraph.Node
}

// ForName answers for a package name rather than a canonical
// identity. The installer's dependency recursion walks recipe dep
// names, and answering here is what makes the lock the exclusive
// version selector on that path. One name maps to one node: a plan
// is a single closure, and cross-root version conflicts already
// failed in traverse.
func (p *Plan) ForName(name string) (Node, bool) {
	for _, n := range p.Nodes {
		if n.Name == name {
			return n, true
		}
	}
	return Node{}, false
}

// Build constructs the plan for one platform.
func Build(req Request) (*Plan, error) {
	if err := checkPlatform(req.Platform); err != nil {
		return nil, err
	}
	roots, origin, err := req.Lock.EffectiveRootsWithOrigin(req.Host)
	if err != nil {
		return nil, err
	}
	// With the origin, so a stale-lock refusal names the exact `gale
	// lock` invocation. This error is what direnv prints on cd.
	if err := lockfile.CheckDeclared(roots, req.Declared, lockfile.Origins{
		Roots:    origin,
		Declared: req.DeclaredOrigin,
	}); err != nil {
		return nil, err
	}
	nodes, err := traverse(req, roots)
	if err != nil {
		return nil, err
	}
	// Closure rejects cycles and computes every digest bottom-up, so
	// the order it returns is the order installs may commit in.
	graph := graphOf(nodes, req.Platform)
	digests, order, err := lockgraph.Closure(graph)
	if err != nil {
		return nil, err
	}
	for key, n := range nodes {
		if digests[key] != n.GraphDigest {
			return nil, fmt.Errorf(
				"%w: %s records %q, its closure computes %q",
				ErrDigestMismatch, key, n.GraphDigest, digests[key],
			)
		}
		n.Graph = graph[key]
		nodes[key] = n
	}
	return &Plan{Nodes: nodes, Order: order, Roots: sortedValues(roots)}, nil
}

// checkPlatform requires exactly one separator with both halves
// present. strings.Cut splits once, so a lenient check would accept
// "darwin/arm64/extra" and silently drop the tail into GOARCH — and
// the platform is a serialized digest field, so the resulting digest
// would agree with nothing.
func checkPlatform(platform string) error {
	goos, goarch, ok := strings.Cut(platform, "/")
	if !ok || goos == "" || goarch == "" || strings.Contains(goarch, "/") {
		return fmt.Errorf("%w: %q", ErrMalformedPlatform, platform)
	}
	return nil
}

// checkArtifact requires the persisted enum and hash formats. These
// values are read back and compared verbatim, so one outside the
// format describes a lock that cannot be modeled rather than one to
// interpret generously.
func checkArtifact(key string, art lockfile.Artifact) error {
	if art.Method != lockgraph.MethodBinary && art.Method != lockgraph.MethodSource {
		return fmt.Errorf("%w: %s has method %q", ErrMalformedArtifact, key, art.Method)
	}
	if !lockgraph.IsHexSHA256(art.SHA256) {
		return fmt.Errorf(
			"%w: %s sha256 %q is not 64 lowercase hex digits",
			ErrMalformedArtifact, key, art.SHA256,
		)
	}
	if art.ManifestDigest != "" && !lockgraph.IsDigest(art.ManifestDigest) {
		return fmt.Errorf(
			"%w: %s manifest digest %q is not sha256:<hex>",
			ErrMalformedArtifact, key, art.ManifestDigest,
		)
	}
	if !lockgraph.IsDigest(art.GraphDigest) {
		return fmt.Errorf(
			"%w: %s graph digest %q is not sha256:<hex>",
			ErrMalformedArtifact, key, art.GraphDigest,
		)
	}
	return nil
}

// traverse walks the locked edges from the roots, resolving and
// validating one recipe per node. Nodes already collected are skipped,
// which also makes a cyclic graph terminate here; rejecting the cycle
// is Closure's job, where the topological order is computed.
func traverse(req Request, roots map[string]string) (map[string]Node, error) {
	nodes := make(map[string]Node)
	// chosen records the one version planned per package name. The
	// store holds several happily, but a generation links exactly one,
	// so a second version reached from another root would defer the
	// choice to symlink time and resolve it silently.
	chosen := make(map[string]string)
	queue := sortedValues(roots)
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		if _, done := nodes[key]; done {
			continue
		}
		n, err := plannedNode(req, key)
		if err != nil {
			return nil, err
		}
		if other, dup := chosen[n.Name]; dup && other != key {
			return nil, fmt.Errorf(
				"%w: %s and %s are both required", lockfile.ErrVersionConflict, other, key,
			)
		}
		chosen[n.Name] = key
		nodes[key] = n
		queue = append(queue, n.RuntimeDeps...)
	}
	return nodes, nil
}

// plannedNode reads one locked node and pairs it with its recipe.
func plannedNode(req Request, key string) (Node, error) {
	name, version, err := lockfile.ParseIdentity(key)
	if err != nil {
		return Node{}, err
	}
	pkg, ok := req.Lock.Packages[key]
	if !ok {
		return Node{}, fmt.Errorf("%w: %s", lockfile.ErrMissingNode, key)
	}
	art, ok := pkg.Artifacts[req.Platform]
	if !ok {
		return Node{}, fmt.Errorf(
			"%w: %s has no artifact for %s", lockfile.ErrMissingArtifact, key, req.Platform,
		)
	}
	if err := checkArtifact(key, art); err != nil {
		return Node{}, err
	}
	for _, id := range append(append([]string{}, art.RuntimeDeps...), art.BuildDeps...) {
		if _, _, err := lockfile.ParseIdentity(id); err != nil {
			return Node{}, fmt.Errorf("%s: %w", key, err)
		}
	}
	n := Node{
		Name:           name,
		Version:        version,
		Method:         art.Method,
		SHA256:         art.SHA256,
		ManifestDigest: art.ManifestDigest,
		RuntimeDeps:    art.RuntimeDeps,
		BuildDeps:      art.BuildDeps,
		GraphDigest:    art.GraphDigest,
	}
	r, err := req.Resolve(name, version)
	if err != nil {
		return Node{}, fmt.Errorf("resolve %s: %w", key, err)
	}
	if r == nil {
		return Node{}, fmt.Errorf("%w: no recipe for %s", ErrRecipeMismatch, key)
	}
	if err := validateRecipe(n, r, req.Platform); err != nil {
		return Node{}, err
	}
	n.Recipe = r
	return n, nil
}

// validateRecipe checks that the recipe describes the locked node
// rather than selecting anything of its own.
func validateRecipe(n Node, r *recipe.Recipe, platform string) error {
	if r.Package.Name != n.Name || r.Package.Full() != n.Version {
		return fmt.Errorf(
			"%w: %s resolved to %s@%s", ErrRecipeMismatch,
			lockgraph.Key(n.Name, n.Version), r.Package.Name, r.Package.Full(),
		)
	}
	goos, goarch, _ := strings.Cut(platform, "/")
	if !r.Package.SupportsPlatform(goos + "-" + goarch) {
		return fmt.Errorf(
			"%w: %s is locked for %s, which the recipe does not support",
			ErrRecipeMismatch, n.Name, platform,
		)
	}
	// Leftover bottle and source locks refuse. Live install is fetch.
	return fmt.Errorf(
		"%w: %s is locked to leftover %s; use gale fetch or gale fetch-adopt",
		ErrRecipeMismatch, n.Name, n.Method,
	)
}

// validateEdges checks the locked dependency names against the ones
// the recipe declares, in the same runtime/build classification.
//
// Build edges are checked for a source node, and for any node whose
// lock carries them: a binary node need not record build deps, since
// it was not produced from them here, but recording a set that
// disagrees with the recipe is still a conflict.
func validateEdges(n Node, r *recipe.Recipe, goos, goarch string) error {
	deps := r.DependenciesForPlatform(goos, goarch)
	if err := compareEdges(n.Name, "runtime", n.RuntimeDeps, deps.Runtime); err != nil {
		return err
	}
	if len(n.BuildDeps) == 0 {
		return nil
	}
	return compareEdges(n.Name, "build", n.BuildDeps, deps.Build)
}

// compareEdges compares locked identities against declared names.
func compareEdges(pkg, kind string, locked, declared []string) error {
	got := make([]string, 0, len(locked))
	for _, id := range locked {
		name, _, _ := strings.Cut(id, "@")
		got = append(got, name)
	}
	got = uniqueSorted(got)
	want := uniqueSorted(declared)
	if slices.Equal(got, want) {
		return nil
	}
	return fmt.Errorf(
		"%w: %s locks %s deps %v, the recipe declares %v",
		ErrRecipeMismatch, pkg, kind, got, want,
	)
}

// graphOf converts planned nodes into the digest serializer's shape.
// The platform is threaded through because it is a serialized field:
// the same closure on two platforms must not produce one digest, and
// the provenance writer binds the platform the same way.
func graphOf(nodes map[string]Node, platform string) map[string]lockgraph.Node {
	goos, goarch, _ := strings.Cut(platform, "/")
	out := make(map[string]lockgraph.Node, len(nodes))
	for key, n := range nodes {
		edges := make([]lockgraph.Edge, 0, len(n.RuntimeDeps)+len(n.BuildDeps))
		edges = append(edges, edgesOf(lockgraph.KindRuntime, n.RuntimeDeps)...)
		edges = append(edges, edgesOf(lockgraph.KindBuild, n.BuildDeps)...)
		out[key] = lockgraph.Node{
			Name:           n.Name,
			Version:        n.Version,
			GOOS:           goos,
			GOARCH:         goarch,
			Method:         n.Method,
			SHA256:         n.SHA256,
			ManifestDigest: n.ManifestDigest,
			Edges:          edges,
		}
	}
	return out
}

// edgesOf splits canonical identities into edges of one kind.
func edgesOf(kind lockgraph.Kind, ids []string) []lockgraph.Edge {
	edges := make([]lockgraph.Edge, 0, len(ids))
	for _, id := range ids {
		name, version, _ := strings.Cut(id, "@")
		edges = append(edges, lockgraph.Edge{Kind: kind, Name: name, Version: version})
	}
	return edges
}

// sortedValues returns a map's values in sorted order, so traversal
// and the recorded root list do not vary with map iteration order.
func sortedValues(m map[string]string) []string {
	out := slices.Collect(maps.Values(m))
	sort.Strings(out)
	return out
}

// uniqueSorted deduplicates and sorts, so a comparison is about set
// membership rather than declaration order.
func uniqueSorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return slices.Compact(out)
}
