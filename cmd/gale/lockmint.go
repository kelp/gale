package main

// Other-platform minting (design §11). A recipe's binaries table
// carries a per-platform sha256 and manifest digest, so a platform
// whose entire effective closure is prebuilt can be described without
// installing anything, and a committed lock then covers the machines
// the project is actually used on.
//
// Deriving it lives here rather than in internal/lockwrite because it
// reads recipes through the command's resolver, which reaches the
// network. lockwrite's inputs are the store and the provenance beside
// it; it folds the finished artifact set in and validates the
// document, which is the half that belongs to the document.

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/lockwrite"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/recipe"
)

// platform is one artifact dimension, which the two files spell
// differently: gale.lock keys artifacts <goos>/<goarch> and a recipe
// keys its prebuilt binaries <goos>-<goarch>. Both spellings come off
// this one value, so the translation exists once instead of being
// open-coded wherever a key is needed.
type platform struct {
	goos   string
	goarch string
}

// lockKey is how gale.lock keys the artifact.
func (p platform) lockKey() string { return p.goos + "/" + p.goarch }

// recipeKey is how a recipe keys the same platform, in its prebuilt
// table and in [package] platforms alike.
func (p platform) recipeKey() string { return p.goos + "-" + p.goarch }

// platformFromRecipeKey parses a [binary.<key>] table name. It splits
// on the first hyphen, exactly as BinaryForPlatform joins the two
// parts, so a key this accepts is one that recipe lookup reproduces.
func platformFromRecipeKey(key string) (platform, bool) {
	goos, goarch, ok := strings.Cut(key, "-")
	if !ok || goos == "" || goarch == "" {
		return platform{}, false
	}
	return platform{goos: goos, goarch: goarch}, true
}

// mintOtherPlatforms derives an artifact set for every platform other
// than this one whose entire closure the recipes describe, and names
// the ones it could not.
//
// Never returns an error. §11 makes minting opportunistic: a platform
// that cannot be derived is reported and the lock is written without
// it, because failing would, under the atomic-write rule, cost the
// user the whole lockfile over a platform they may not even use. A
// platform is derived whole or not at all, since a graph digest is
// recursive and cannot be computed over a dangling closure, and a
// partial entry would read as supported and fail on use.
func mintOtherPlatforms(
	resolve installer.RecipeResolver, roots []*recipe.Recipe,
) ([]lockwrite.Mint, []lockwrite.PlatformSkip) {
	m := &minter{resolve: resolve, cache: make(map[string]*recipe.Recipe)}
	var mints []lockwrite.Mint
	var skipped []lockwrite.PlatformSkip
	for _, p := range candidatePlatforms(roots) {
		artifacts, err := m.mint(roots, p)
		if err != nil {
			skipped = append(skipped, lockwrite.PlatformSkip{
				Platform: p.lockKey(), Reason: err.Error(),
			})
			continue
		}
		mints = append(mints, lockwrite.Mint{
			Platform: p.lockKey(), Artifacts: artifacts,
		})
	}
	return mints, skipped
}

// candidatePlatforms lists the platforms worth attempting: the ones
// some root declares a prebuilt binary for, minus this machine's own.
//
// The roots bound the search because a platform no root is prebuilt
// for is blocked at the root anyway, and this machine's own platform
// is excluded because the write already records what it verified
// there. Restating it from a recipe would replace a verified hash
// with a declared one.
func candidatePlatforms(roots []*recipe.Recipe) []platform {
	seen := make(map[string]platform)
	for _, r := range roots {
		for key := range r.Binary {
			p, ok := platformFromRecipeKey(key)
			if !ok || p.lockKey() == currentPlatform() {
				continue
			}
			seen[p.lockKey()] = p
		}
	}
	out := make([]platform, 0, len(seen))
	for _, key := range slices.Sorted(maps.Keys(seen)) {
		out = append(out, seen[key])
	}
	return out
}

// minter derives one platform's closure at a time, reusing the
// recipes it has already resolved. The cache is per run rather than
// per platform: the dependency names are the same set on most
// platforms, and each miss is a registry fetch.
type minter struct {
	resolve installer.RecipeResolver
	cache   map[string]*recipe.Recipe
}

// mint derives one platform's whole closure, or reports why it could
// not.
func (m *minter) mint(
	roots []*recipe.Recipe, p platform,
) (map[string]lockfile.Artifact, error) {
	nodes, blocked, err := m.closure(roots, p)
	if err != nil {
		return nil, err
	}
	if reason := blocked.reason(); reason != "" {
		// The blocking nodes are the diagnostic §11 asks for: a
		// source-built dependency is why the platform is missing, and
		// naming the platform alone leaves the user nothing to fix.
		return nil, errors.New(reason)
	}
	digests, _, err := lockgraph.Closure(nodes)
	if err != nil {
		// ErrCycle or ErrMissingDep here means the recipe graph itself
		// is broken. It is still only this platform's problem to
		// report: nothing was installed from it, so there is nothing
		// to fail about beyond the entry not being written.
		return nil, fmt.Errorf("deriving the graph digests: %w", err)
	}
	return artifactsOf(nodes, digests), nil
}

// closure walks the runtime closure of the roots on one platform,
// returning the derived nodes and the identities that blocked it.
//
// The edges come from the recipes, not from provenance: there is no
// provenance for a platform this machine never installed.
// DependenciesForPlatform applies the platform overlay, so a
// dependency that exists only on the foreign platform is followed and
// one that exists only here is not.
//
// Runtime edges only. A minted artifact is method = "binary", and §5
// serializes no build edge for a binary node, so recording them would
// put nodes in the lock that contribute to no digest.
func (m *minter) closure(
	roots []*recipe.Recipe, p platform,
) (map[string]lockgraph.Node, *blockers, error) {
	nodes := make(map[string]lockgraph.Node)
	visited := make(map[string]bool)
	blocked := newBlockers()
	var visit func(r *recipe.Recipe) error
	visit = func(r *recipe.Recipe) error {
		key := lockgraph.Key(r.Package.Name, r.Package.Full())
		if visited[key] {
			return nil
		}
		// Marked before recursing, so a cyclic recipe graph terminates
		// here. Rejecting the cycle is lockgraph.Closure's job, and it
		// still sees one: every node on the cycle keeps its edges.
		visited[key] = true
		// The allowlist is asked before the binaries table, because a
		// prebuilt for an excluded platform is the case that motivates
		// the check: index merging can publish one, and planning on
		// that machine rejects the node regardless. Reporting "no
		// prebuilt binary" for it would name the wrong remedy.
		if !r.Package.SupportsPlatform(p.recipeKey()) {
			blocked.excluded[key] = true
			return nil
		}
		bin := r.BinaryForPlatform(p.goos, p.goarch)
		if !mintable(bin) {
			blocked.noBinary[key] = true
			return nil
		}
		edges, err := m.edges(r, p, visit)
		if err != nil {
			return err
		}
		nodes[key] = lockgraph.Node{
			Name:           r.Package.Name,
			Version:        r.Package.Full(),
			GOOS:           p.goos,
			GOARCH:         p.goarch,
			Method:         lockgraph.MethodBinary,
			SHA256:         bin.SHA256,
			ManifestDigest: bin.ManifestDigest,
			Edges:          edges,
		}
		return nil
	}
	for _, r := range roots {
		if err := visit(r); err != nil {
			return nil, nil, err
		}
	}
	return nodes, blocked, nil
}

// blockers collects the nodes that stop a platform being derived,
// keeping the two causes apart because they name different remedies:
// publishing a prebuilt, or widening [package] platforms.
type blockers struct {
	excluded map[string]bool
	noBinary map[string]bool
}

func newBlockers() *blockers {
	return &blockers{
		excluded: make(map[string]bool),
		noBinary: make(map[string]bool),
	}
}

// reason renders the diagnostic, or "" when nothing blocked. Each
// group is sorted, so a platform blocked by several nodes reports the
// same line on every run rather than one per map iteration.
func (b *blockers) reason() string {
	var parts []string
	if len(b.excluded) > 0 {
		parts = append(parts, "excluded by [package] platforms: "+
			strings.Join(slices.Sorted(maps.Keys(b.excluded)), ", "))
	}
	if len(b.noBinary) > 0 {
		parts = append(parts, "no prebuilt binary for "+
			strings.Join(slices.Sorted(maps.Keys(b.noBinary)), ", "))
	}
	return strings.Join(parts, "; ")
}

// edges resolves one recipe's runtime dependencies on a platform and
// recurses into each, in the order the recipe declares them.
//
// The identity is the resolved recipe's Full(), the canonical
// version-revision a reinstall would write, which is the only
// spelling the lock and the store agree on.
//
// Constraints come from the same DependenciesForPlatform call as the
// names, so the platform overlay applies to both, and every level of
// the walk checks its own: a constraint a dependency declares binds
// exactly as one the root declares.
func (m *minter) edges(
	r *recipe.Recipe, p platform, visit func(*recipe.Recipe) error,
) ([]lockgraph.Edge, error) {
	deps := r.DependenciesForPlatform(p.goos, p.goarch)
	edges := make([]lockgraph.Edge, 0, len(deps.Runtime))
	for _, name := range deps.Runtime {
		dep, err := m.recipeFor(name)
		if err != nil {
			return nil, err
		}
		if err := checkConstraint(r, deps.Constraints[name], dep); err != nil {
			return nil, err
		}
		edges = append(edges, lockgraph.Edge{
			Kind:    lockgraph.KindRuntime,
			Name:    dep.Package.Name,
			Version: dep.Package.Full(),
		})
		if err := visit(dep); err != nil {
			return nil, err
		}
	}
	return edges, nil
}

// checkConstraint holds a resolved dependency to the version
// constraint its parent declares.
//
// Minting resolves a dependency by name, exactly as the installer
// does, so it inherits the installer's obligation to check what the
// resolver returned. Nothing downstream would catch a violation:
// locked planning compares dependency names and never constraints, so
// an unchecked mint commits a closure an install on that platform
// refuses to build.
//
// It goes through recipe.ParseConstraint and Constraint.Satisfies,
// the pair installDepsInner uses, so the two can only agree. A bare
// dependency declares no constraint and is unconstrained, which is
// today's resolve-to-latest behavior and not a violation.
func checkConstraint(parent *recipe.Recipe, expr string, dep *recipe.Recipe) error {
	if expr == "" {
		return nil
	}
	c, err := recipe.ParseConstraint(expr)
	if err != nil {
		return fmt.Errorf(
			"%s: invalid version constraint %q on %s: %w",
			parent.Package.Name, expr, dep.Package.Name, err,
		)
	}
	if !c.Satisfies(dep.Package.Version, dep.Package.Revision) {
		return fmt.Errorf(
			"%s resolved to %s, which does not satisfy constraint %q "+
				"declared in %s",
			dep.Package.Name, dep.Package.Full(), expr, parent.Package.Name,
		)
	}
	return nil
}

// recipeFor resolves a dependency by name, once per run.
func (m *minter) recipeFor(name string) (*recipe.Recipe, error) {
	if r, ok := m.cache[name]; ok {
		return r, nil
	}
	r, err := m.resolve(context.Background(), name)
	if err != nil {
		return nil, fmt.Errorf("resolving a recipe for %s: %w", name, err)
	}
	if r == nil {
		return nil, fmt.Errorf("no recipe found for %s", name)
	}
	m.cache[name] = r
	return r, nil
}

// mintable reports whether a prebuilt entry can back a locked
// artifact.
//
// Everything plan construction will demand of it, checked before the
// entry exists rather than after. A locked binary never falls back to
// source (§9), so an entry derived from a URL-less, untrusted, or
// malformed binary would be a platform that reads as supported and
// must fail on use — the state §11 forbids minting into being.
func mintable(b *recipe.Binary) bool {
	if b == nil || b.URL == "" || !lockgraph.IsHexSHA256(b.SHA256) {
		return false
	}
	if b.ManifestDigest != "" && !lockgraph.IsDigest(b.ManifestDigest) {
		return false
	}
	return b.CheckTrustPolicy() == nil
}

// artifactsOf renders the derived nodes as lock artifacts.
//
// Dependencies are sorted and deduplicated so relocking an unchanged
// closure produces byte-identical output. The digest is unaffected:
// §5 sorts the edge lines itself.
func artifactsOf(
	nodes map[string]lockgraph.Node, digests map[string]string,
) map[string]lockfile.Artifact {
	out := make(map[string]lockfile.Artifact, len(nodes))
	for key, n := range nodes {
		deps := make([]string, 0, len(n.Edges))
		for _, e := range n.Edges {
			deps = append(deps, lockgraph.Key(e.Name, e.Version))
		}
		out[key] = lockfile.Artifact{
			SHA256:         n.SHA256,
			ManifestDigest: n.ManifestDigest,
			Method:         n.Method,
			RuntimeDeps:    slices.Compact(slices.Sorted(slices.Values(deps))),
			GraphDigest:    digests[key],
		}
	}
	return out
}

// warnSkippedPlatforms reports the platforms the lock does not cover.
//
// A warning rather than an error, and printed after the write: the
// lockfile is complete for every platform it does describe, and
// running gale on one it does not produces §9's missing-platform
// error, which is the correct outcome rather than a surprise.
func warnSkippedPlatforms(out *output.Output, skipped []lockwrite.PlatformSkip) {
	for _, s := range skipped {
		out.Warn(fmt.Sprintf(
			"gale.lock records no %s artifacts: %s", s.Platform, s.Reason,
		))
	}
}
