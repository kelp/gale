// Package activation is the read-only check that runs before a
// project's binaries reach PATH.
//
// It exists because no other check covers the upgrade boundary. Sync
// enforces the lock when it runs, and the direnv hook runs sync only
// when gale.toml is newer than the generation symlink — but upgrading
// the gale binary modifies no file in the project, so a lock this
// build refuses to honor would otherwise reach PATH unexamined. The
// gate therefore runs on every activation and compares nothing but
// mtimes' opposite: recorded identities.
//
// The gate is strictly read-only and hashes nothing. It reads the
// lockfile and the store's provenance records, which is enough to
// answer "was every directory the generation links verified as the
// bytes the lock names". Hashing a store directory could not answer it
// anyway: the darwin fixup rewrites staged bytes after the artifact
// hash is taken, so a directory hash never equals the artifact SHA.
//
// It runs on every `cd`, so a failure must be one actionable line
// rather than a diagnostic dump, and a bug here locks a user out of
// their own shell.
package activation

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/store"
)

// ErrDrift reports that the active generation links something the
// lock does not name.
//
// It is deliberately not an integrity conflict. Drift means the
// generation needs rebuilding — `carryForwardMissingVersions` seeds it
// with the previous generation's version when a newly pinned one is
// not installed — and `gale sync` fixes it. An integrity conflict
// means bytes disagreed with the lock and deserves a human. Collapsing
// the two would make every carried-forward package read as tampering,
// and users who learn to ignore that message ignore the real one too.
var ErrDrift = errors.New("active generation does not match the lock")

// Request is everything the gate reads. Platform and Installed are
// passed in rather than discovered here so the check stays a pure
// function of its inputs: the caller supplies the host's platform and
// the active generation's contents from generation.CurrentVersions.
type Request struct {
	LockPath string
	// Host selects the lock's overlay targets; empty means the shared
	// default target only.
	Host string
	// Platform is GOOS/GOARCH, the lockfile's artifact dimension.
	Platform  string
	StoreRoot string
	// Installed maps a package name to the store-directory basename
	// the active generation links, as generation.CurrentVersions
	// reports it.
	Installed map[string]string
}

// Check reports whether the active generation may be activated.
//
// A lockfile that is present and not v1 is refused. That is the whole
// point of running unconditionally: a legacy lock records checksums
// nothing ever verified, and no file's mtime changes when the binary
// that would have honored it is replaced by one that will not.
func Check(req Request) error {
	v, err := lockfile.Load(req.LockPath)
	if err != nil {
		return err
	}
	switch v.Kind {
	case lockfile.KindAbsent:
		// Unlocked mode. Absence is the one case that is genuinely not
		// a lock, so it stays an ordinary path rather than a refusal.
		return nil
	case lockfile.KindLegacy:
		return fmt.Errorf(
			"%s: %w: run gale lock to record what is installed",
			req.LockPath, lockfile.ErrLegacySchema,
		)
	case lockfile.KindV1:
		return checkV1(req, v.V1)
	default:
		return fmt.Errorf("unhandled lockfile kind %s", v.Kind)
	}
}

// checkV1 resolves the locked closure for this host and platform,
// confirms it names everything the generation links, then verifies
// each linked package's runtime closure against store provenance.
func checkV1(req Request, lf *lockfile.V1) error {
	roots, err := lf.EffectiveRoots(req.Host)
	if err != nil {
		return err
	}
	// The closure is resolved from the lock's own roots, not from what
	// is installed, because the naming check needs the full set of
	// identities this host is entitled to. A root with no artifact for
	// this platform fails here, which is design §29's missing-platform
	// error: `gale lock` writes no artifacts for a platform it could
	// not derive, and running on one is a refusal rather than a skip.
	nodes, err := nodesFrom(lf, req.Platform, sortedValues(roots))
	if err != nil {
		return err
	}
	locked, err := identityByName(nodes)
	if err != nil {
		return err
	}
	if unlocked := UnlockedVersions(req.Installed, locked, req.StoreRoot); len(unlocked) > 0 {
		return fmt.Errorf(
			"%w: it links %s, which %s does not name; run gale sync",
			ErrDrift, strings.Join(unlocked, ", "), req.LockPath,
		)
	}
	// Closure recomputes every digest bottom-up from the lock. The
	// lock's own stored graph_digest is never believed, and never needs
	// to be: the recomputed value is what provenance is compared
	// against, so a hand-edited digest in the file cannot make
	// anything verify. Plan construction is where the file's internal
	// consistency is judged.
	digests, _, err := lockgraph.Closure(nodes)
	if err != nil {
		return err
	}
	return verifyRuntime(req.StoreRoot, nodes, digests,
		runtimeKeys(nodes, seedsFor(req.Installed, locked)))
}

// verifyRuntime compares each named store directory's provenance
// against the node the lock says should be there.
func verifyRuntime(
	storeRoot string, nodes map[string]lockgraph.Node,
	digests map[string]string, keys []string,
) error {
	s := store.NewStore(storeRoot)
	for _, key := range keys {
		n := nodes[key]
		// ResolveDir, not a path join: a canonical "<v>-1" identity
		// legitimately lives in a bare "<v>" directory for installs
		// predating revisions, and a join would report every one of
		// those as unprovenanced.
		dir := s.ResolveDir(n.Name, n.Version)
		if _, err := provenance.VerifyAgainstLock(dir, n, digests[key]); err != nil {
			return err
		}
	}
	return nil
}

// identityByName keys the closure by package name, which is how a
// generation addresses it: a generation links exactly one version per
// name. Two versions of one name in a single host's closure is a lock
// that cannot be modeled, because accepting both would defer the
// choice to symlink time and resolve it silently.
func identityByName(nodes map[string]lockgraph.Node) (map[string]string, error) {
	out := make(map[string]string, len(nodes))
	for _, key := range sortedKeys(nodes) {
		n := nodes[key]
		if other, dup := out[n.Name]; dup {
			return nil, fmt.Errorf(
				"%w: %s and %s are both in the locked closure",
				lockfile.ErrVersionConflict, other, key,
			)
		}
		out[n.Name] = key
	}
	return out, nil
}

// UnlockedVersions returns the installed identities the lock does not
// name, sorted.
//
// This is the one implementation of that comparison. The gate asks it
// of the active generation; locked sync's validation callback asks it
// of the package set a generation is about to be built from, because a
// carried-forward version is drift discovered after per-artifact
// verification and the callback is where such drift is rejected.
// Global scope needs the second caller specifically: nothing hooks
// global activation, so a version carried into the global generation
// that the lock does not name is what no later check would catch.
//
// installed maps a package name to a store-directory basename; locked
// maps a package name to a canonical name@version-revision identity.
// A name absent from locked, or one whose identity is malformed, is
// reported rather than skipped: the question is whether the lock names
// what is installed, and an unanswerable question is not a yes.
func UnlockedVersions(installed, locked map[string]string, storeRoot string) []string {
	s := store.NewStore(storeRoot)
	var out []string
	for name, version := range installed {
		if !namesInstalled(s, locked[name], name, version) {
			out = append(out, lockgraph.Key(name, version))
		}
	}
	sort.Strings(out)
	return out
}

// namesInstalled reports whether the locked identity id resolves to
// the store directory the generation actually links. The comparison is
// on resolved directories rather than version strings so a canonical
// "-1" identity matches the bare directory it lives in.
func namesInstalled(s *store.Store, id, name, version string) bool {
	if id == "" {
		return false
	}
	lockedName, lockedVersion, err := lockfile.ParseIdentity(id)
	if err != nil || lockedName != name {
		return false
	}
	return filepath.Base(s.ResolveDir(name, lockedVersion)) == version
}

// seedsFor collects the locked identity of every package the
// generation links, sorted so the walk and any error are the same on
// every run. Every name is present in locked: UnlockedVersions runs
// first and refuses activation otherwise.
func seedsFor(installed, locked map[string]string) []string {
	seeds := make([]string, 0, len(installed))
	for name := range installed {
		seeds = append(seeds, locked[name])
	}
	sort.Strings(seeds)
	return seeds
}

// sortedValues returns a map's values in sorted order, so the walk
// does not vary with map iteration order.
func sortedValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// sortedKeys keeps a map walk deterministic, so a conflict error names
// the same pair on every run.
func sortedKeys(nodes map[string]lockgraph.Node) []string {
	keys := make([]string, 0, len(nodes))
	for k := range nodes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// nodesFrom reads the locked closure reachable from seeds, following
// runtime edges always and build edges only where the node's own
// method is source.
//
// That is lockgraph's serialization rule, and matching it exactly is
// what makes the resulting graph sufficient for Closure and no larger:
// a prebuilt binary's recipe-derived build edges contribute to no
// digest, so pulling their nodes in would demand lock entries for
// tools that never touched these bytes.
func nodesFrom(
	lf *lockfile.V1, platform string, seeds []string,
) (map[string]lockgraph.Node, error) {
	nodes := make(map[string]lockgraph.Node, len(seeds))
	queue := append([]string(nil), seeds...)
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		if _, done := nodes[key]; done {
			continue
		}
		n, err := nodeFor(lf, platform, key)
		if err != nil {
			return nil, err
		}
		nodes[key] = n
		for _, e := range n.Edges {
			if e.Kind == lockgraph.KindBuild && n.Method != lockgraph.MethodSource {
				continue
			}
			queue = append(queue, lockgraph.Key(e.Name, e.Version))
		}
	}
	return nodes, nil
}

// nodeFor reads one locked artifact for one platform.
func nodeFor(lf *lockfile.V1, platform, key string) (lockgraph.Node, error) {
	name, version, err := lockfile.ParseIdentity(key)
	if err != nil {
		return lockgraph.Node{}, err
	}
	pkg, ok := lf.Packages[key]
	if !ok {
		return lockgraph.Node{}, fmt.Errorf("%w: %s", lockfile.ErrMissingNode, key)
	}
	art, ok := pkg.Artifacts[platform]
	if !ok {
		return lockgraph.Node{}, fmt.Errorf(
			"%w: %s has no artifact for %s",
			lockfile.ErrMissingArtifact, key, platform,
		)
	}
	edges, err := edgesOf(key, art)
	if err != nil {
		return lockgraph.Node{}, err
	}
	goos, goarch, _ := strings.Cut(platform, "/")
	return lockgraph.Node{
		Name:           name,
		Version:        version,
		GOOS:           goos,
		GOARCH:         goarch,
		Method:         art.Method,
		SHA256:         art.SHA256,
		ManifestDigest: art.ManifestDigest,
		Edges:          edges,
	}, nil
}

// edgesOf splits an artifact's recorded identities into edges. Each is
// parsed rather than trusted, so a malformed one fails as a lock that
// cannot be modeled instead of becoming an edge naming an empty
// version.
func edgesOf(key string, art lockfile.Artifact) ([]lockgraph.Edge, error) {
	kinds := []struct {
		kind lockgraph.Kind
		ids  []string
	}{
		{lockgraph.KindRuntime, art.RuntimeDeps},
		{lockgraph.KindBuild, art.BuildDeps},
	}
	edges := make([]lockgraph.Edge, 0, len(art.RuntimeDeps)+len(art.BuildDeps))
	for _, k := range kinds {
		for _, id := range k.ids {
			name, version, err := lockfile.ParseIdentity(id)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			edges = append(edges, lockgraph.Edge{
				Kind: k.kind, Name: name, Version: version,
			})
		}
	}
	return edges, nil
}

// runtimeKeys returns the nodes that must still be installed: the
// seeds plus everything reachable from them through runtime edges
// alone, sorted.
//
// Build dependencies are excluded deliberately. Design §12 lets them
// be collected, and a source artifact's stored graph_digest binds the
// build closure it was produced from without that closure needing to
// exist — which is exactly why the gate compares against a digest
// recomputed from the lock rather than calling
// provenance.VerifyAgainstStore.
func runtimeKeys(nodes map[string]lockgraph.Node, seeds []string) []string {
	seen := make(map[string]bool, len(nodes))
	queue := append([]string(nil), seeds...)
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		if seen[key] {
			continue
		}
		seen[key] = true
		for _, e := range nodes[key].Edges {
			if e.Kind == lockgraph.KindRuntime {
				queue = append(queue, lockgraph.Key(e.Name, e.Version))
			}
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
