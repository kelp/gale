// Package lockwrite turns what is installed and verified into the
// lockfile document that describes it.
//
// It is the inverse of internal/lockplan, and deliberately a separate
// package from internal/lockfile: the schema package stays pure, while
// producing a document requires the store, the provenance records
// beside it, and eventually recipes. Every writer goes through here so
// there is one answer to what a lock says about a closure, rather than
// one per command.
//
// Nothing here touches the filesystem's lock path. Build returns a
// document and the caller writes it in a single atomic write, which is
// what makes a failed resolution leave the previous lockfile
// byte-identical.
package lockwrite

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/provenance"
)

// Request is everything the writer needs to describe one target.
type Request struct {
	// StoreRoot is the package store the closure was installed into.
	StoreRoot string
	// Platform is GOOS/GOARCH, the artifact dimension being written.
	Platform string
	// Target is the host selector to regenerate, verbatim and
	// post-alias, or empty for [targets.default]. Only this target is
	// rewritten; every other one is carried forward.
	Target string
	// Roots maps a package name to the canonical version-revision it
	// resolved to. These are the roots this write verified itself.
	Roots map[string]string
	// Declared is the effective gale.toml package set for the section
	// written, name to the version as the manifest spells it. The lock's
	// roots must agree with it or the lock reads as stale, so the writer
	// needs it to know which prior roots are still wanted.
	Declared map[string]string
	// Existing is the lock currently on disk, or nil when absent. It is
	// read and never mutated.
	Existing *lockfile.V1
}

// Section is one target's contribution to a write: which target to
// regenerate, from which verified roots, against which manifest
// section. The fields mean exactly what Request's do.
type Section struct {
	Target   string
	Roots    map[string]string
	Declared map[string]string
}

// AllRequest regenerates several targets in one document.
type AllRequest struct {
	// StoreRoot is the package store the closure was installed into.
	StoreRoot string
	// Platform is GOOS/GOARCH, the artifact dimension being written.
	Platform string
	// Sections are the targets to regenerate, applied in order. Every
	// target not named here is carried forward.
	Sections []Section
	// Existing is the lock currently on disk, or nil when absent. It is
	// read and never mutated.
	Existing *lockfile.V1
}

// UnlockedRoot is one declared package a write could not back, and
// the target it was declared for.
//
// The target travels with the name because the remedy depends on it:
// restoring a root declared in a host overlay needs `gale install
// --host <selector>`, since a plain install writes shared [packages]
// unless this machine's exact overlay already lists the package. A
// bare name cannot say which.
type UnlockedRoot struct {
	Target string
	Name   string
}

// Result is the document to write plus what the write could not cover.
type Result struct {
	// Doc is the document to write, or nil when there is nothing to
	// write and no prior document to amend. Nil means leave the lock
	// path alone: a removal in an unlocked project must not invent a
	// lockfile and put it into locked mode as a side effect.
	Doc *lockfile.V1
	// Unlocked names the declared packages this write could back
	// neither by fresh verification nor by a carried subgraph, sorted.
	// The document is internally complete without them; it is merely
	// stale against the manifest, which is a state with named remedies.
	Unlocked []UnlockedRoot
}

// Build resolves one target's closure and returns the document to
// write. It is BuildAll for the single-section case.
//
// The closure comes from provenance rather than from recipes, because
// the lock must record what was actually verified. A recipe knows what
// should have been fetched; only a provenance record says what was.
func Build(req Request) (*Result, error) {
	return BuildAll(AllRequest{
		StoreRoot: req.StoreRoot,
		Platform:  req.Platform,
		Existing:  req.Existing,
		Sections: []Section{{
			Target: req.Target, Roots: req.Roots, Declared: req.Declared,
		}},
	})
}

// BuildAll folds every section into one document and validates that
// document once, at the end.
//
// Validating per section instead would judge each target against a
// document in which the other touched targets still hold their old
// roots, and that intermediate state can be illegal while both the
// before and after states are legal: two roots moving together onto a
// new shared dependency require the old version and the new one at the
// same time, in whichever order they are rebuilt. `gale update` across
// a shared dependency is exactly that shape.
//
// The check is not exported for the caller to run afterwards. A caller
// that forgets it loses cross-target validation silently, which is the
// same "one rule in two places" trap the writer exists to close.
func BuildAll(req AllRequest) (*Result, error) {
	doc := req.Existing
	var unlocked []UnlockedRoot
	for _, s := range req.Sections {
		res, err := buildSection(Request{
			StoreRoot: req.StoreRoot,
			Platform:  req.Platform,
			Target:    s.Target,
			Roots:     s.Roots,
			Declared:  s.Declared,
			Existing:  doc,
		})
		if err != nil {
			return nil, fmt.Errorf("locking %s: %w", targetLabel(s.Target), err)
		}
		doc = res.Doc
		unlocked = append(unlocked, res.Unlocked...)
	}
	if doc != nil {
		if err := checkEveryEffectiveGraph(doc); err != nil {
			return nil, err
		}
	}
	slices.SortFunc(unlocked, func(a, b UnlockedRoot) int {
		if c := strings.Compare(a.Target, b.Target); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return &Result{Doc: doc, Unlocked: slices.Compact(unlocked)}, nil
}

// targetLabel names a target the way gale.lock spells it, so a
// failure says which section could not be locked.
func targetLabel(target string) string {
	if target == "" {
		return "[targets.default]"
	}
	return fmt.Sprintf("[targets.host.%q]", target)
}

// checkRootsDeclared refuses a freshly verified root the section
// being written does not back.
//
// Carried roots are already held to this: priorRoot drops one whose
// pin no longer matches. Fresh roots need the same test for the same
// reason, and they are the ones that can be wrong in the worse
// direction — a root nobody declares. The reader compares roots
// against gale.toml through CheckDeclared, so emitting one it does
// not back writes a lock that reads as stale on arrival, which is
// §11's recoverable fault manufactured by the writer rather than met.
//
// The comparison goes through VersionMatches, not string equality:
// gale.toml records the bare version by design so an entry tracks
// revision bumps, and the lock records the canonical
// version-revision, so the two spell one pin differently on every
// ordinary install.
func checkRootsDeclared(req Request) error {
	for _, name := range slices.Sorted(maps.Keys(req.Roots)) {
		want, declared := req.Declared[name]
		if !declared {
			return fmt.Errorf(
				"%s@%s was verified but %s does not declare it",
				name, req.Roots[name], manifestSection(req.Target),
			)
		}
		if !lockfile.VersionMatches(req.Roots[name], want) {
			return fmt.Errorf(
				"%s@%s was verified but %s declares %s",
				name, req.Roots[name], manifestSection(req.Target), want,
			)
		}
	}
	return nil
}

// manifestSection names the gale.toml section a target mirrors, so a
// disagreement between the two files is reported against the one the
// user edits.
func manifestSection(target string) string {
	if target == "" {
		return "[packages]"
	}
	return fmt.Sprintf("[hosts.%q.packages]", target)
}

// buildSection folds one section into the document it is handed. It
// does not validate the graph: BuildAll does that once over the
// result, because a document mid-fold is not one any reader will ever
// see. Agreement with the manifest is per section and belongs here.
func buildSection(req Request) (*Result, error) {
	if err := checkRootsDeclared(req); err != nil {
		return nil, err
	}
	records, err := provenance.Closure(req.StoreRoot, req.Platform, req.Roots)
	if err != nil {
		return nil, fmt.Errorf("resolving the installed closure: %w", err)
	}
	carried, unlocked := carriedRoots(req)
	roots := append(sortedIdentities(req.Roots), carried...)
	if len(roots) == 0 {
		// Nothing verified and nothing carryable: the target is dropped
		// rather than written empty, and rather than refused. An empty
		// target would erase the difference between "no shared roots"
		// and "shared roots, currently none"; refusing would turn §11's
		// stale-but-recoverable state into a hard failure, which is what
		// `remove` meets on any machine that has not locked the
		// surviving declarations. They are named in Unlocked instead.
		return &Result{
			Doc: dropTarget(req.Existing, req.Target), Unlocked: unlocked,
		}, nil
	}
	// Other targets' and carried nodes first, so this platform's freshly
	// verified artifacts overwrite rather than merge with whatever a
	// shared node looked like before.
	pkgs := preserved(req, carried)
	for key, r := range records {
		node, foreign := nodeFor(req, key, r)
		pkgs[key] = node
		// A retained foreign artifact carries its own edges, so the
		// nodes behind them come with it. Merging without overwriting
		// keeps this platform's freshly verified node authoritative.
		mergeAbsent(pkgs, foreign)
	}
	return &Result{
		Doc: &lockfile.V1{
			Version:  lockfile.SchemaVersion,
			Targets:  targetsFor(req, roots),
			Packages: pkgs,
		},
		Unlocked: unlocked,
	}, nil
}

// checkEveryEffectiveGraph applies the one-version rule to every graph a
// reader can plan from this document.
//
// Checking the rewritten target alone is not enough: the reader plans
// EffectiveRoots, which merges the default target with every matching
// host overlay. A default root and an exact-host root can each be
// internally consistent while the host that sees both requires two
// versions of one transitive dependency.
func checkEveryEffectiveGraph(doc *lockfile.V1) error {
	hosts, err := witnessHosts(doc)
	if err != nil {
		return checkAllTargetsAtOnce(doc, err)
	}
	for _, host := range hosts {
		roots, err := doc.EffectiveRoots(host)
		if err != nil {
			return err
		}
		// Every platform the document mentions, not just the one being
		// written. Retaining a foreign artifact asserts it, so a conflict
		// that exists only on linux is this writer's to refuse rather
		// than the linux reader's to discover.
		for _, platform := range platformsOf(doc) {
			if err := checkOneVersionPerName(doc, platform, sortedValues(roots)); err != nil {
				return fmt.Errorf("host %q on %s: %w", host, platform, err)
			}
		}
	}
	return nil
}

// witnessHosts returns concrete hostnames covering every distinct set of
// targets a machine can see.
//
// Selector strings cannot be tested directly, and neither can one witness
// per pair. A third selector matching the same hostname masks a
// disagreement between two others through replacement, while a different
// hostname matching only those two still exposes it, so coverage has to
// enumerate distinct effective sets. config.HostSelectorSets owns that
// enumeration because the selector language is config's.
func witnessHosts(doc *lockfile.V1) ([]string, error) {
	// The empty host is the default target alone: what a machine matching
	// no overlay plans.
	hosts := []string{""}
	keys := slices.Sorted(maps.Keys(doc.Targets.Host))
	sets, err := config.HostSelectorSets(keys)
	if err != nil {
		return nil, err
	}
	return append(hosts, sets...), nil
}

// checkAllTargetsAtOnce is the fallback for a document whose selectors
// cannot be enumerated: it checks every target's roots merged into one
// graph.
//
// This is the conservative direction. Merging targets that cannot
// actually co-apply can report a conflict that no real machine would see,
// while failing to check them can emit a lock that a real machine
// refuses. Only the second is a silent hole, so uncertainty resolves
// toward refusing to write.
//
// Reachable only through selector syntax outside the documented grammar,
// since everything within it is decidable.
//
// Roots are passed raw, never collapsed by package name. Collapsing
// would imitate replacement, and replacement is only legitimate between
// selectors known to co-apply, which is exactly what could not be
// determined here. A more specific target's root would then overwrite the
// conflicting one and hide the disagreement, which is the masking failure
// one level down. Two mutually exclusive versions therefore report a
// conflict: that false positive is the intended conservative behavior.
func checkAllTargetsAtOnce(doc *lockfile.V1, cause error) error {
	var roots []string
	if doc.Targets.Default != nil {
		roots = append(roots, doc.Targets.Default.Roots...)
	}
	for _, k := range slices.Sorted(maps.Keys(doc.Targets.Host)) {
		roots = append(roots, doc.Targets.Host[k].Roots...)
	}
	roots = slices.Compact(slices.Sorted(slices.Values(roots)))
	for _, platform := range platformsOf(doc) {
		if err := checkOneVersionPerName(doc, platform, roots); err != nil {
			return fmt.Errorf(
				"checking every target together because the host selectors "+
					"could not be enumerated (%w): %w", cause, err,
			)
		}
	}
	return nil
}

// platformsOf lists every platform any node in the document records.
func platformsOf(doc *lockfile.V1) []string {
	seen := make(map[string]bool)
	for _, p := range doc.Packages {
		for platform := range p.Artifacts {
			seen[platform] = true
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// sortedValues renders a name-keyed root map as sorted identities.
func sortedValues(roots map[string]string) []string {
	ids := make([]string, 0, len(roots))
	for _, id := range roots {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// checkOneVersionPerName refuses one graph that requires two versions of
// a package.
//
// The store holds several versions happily, so a verified closure can
// legitimately contain two: one root built against a dependency at one
// version, another root at a different one. A generation links exactly
// one, and lockplan.traverse rejects such a graph outright, so emitting
// it would produce a document this project's own reader refuses. Failing
// here reports the conflict against the store that actually has it,
// rather than against a lockfile that looks corrupt.
func checkOneVersionPerName(doc *lockfile.V1, platform string, roots []string) error {
	chosen := make(map[string]string)
	seen := make(map[string]bool)
	var visit func(key string) error
	visit = func(key string) error {
		if seen[key] {
			return nil
		}
		seen[key] = true
		name, _, err := lockfile.ParseIdentity(key)
		if err != nil {
			return err
		}
		if other, dup := chosen[name]; dup && other != key {
			return fmt.Errorf(
				"%w: %s and %s are both required",
				lockfile.ErrVersionConflict, other, key,
			)
		}
		chosen[name] = key
		// A hole is not reported here. Fresh nodes come from a verified
		// closure and carried roots were proven complete per platform, so
		// a hole can only reach this walk from another target this write
		// did not touch, whose completeness is not this write's to
		// assert.
		p, ok := doc.Packages[key]
		if !ok {
			return nil
		}
		for _, dep := range serializedDeps(p.Artifacts[platform]) {
			if err := visit(dep); err != nil {
				return err
			}
		}
		return nil
	}
	for _, id := range roots {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

// mergeAbsent copies entries that dst does not already define.
func mergeAbsent(dst, src map[string]lockfile.Package) {
	for k, v := range src {
		if _, ok := dst[k]; !ok {
			dst[k] = v
		}
	}
}

// serializedDeps lists the dependencies of a lock artifact that the
// graph actually traverses: runtime always, build only for a source
// artifact.
//
// The rule cannot be skipped for lock artifacts the way it can for
// provenance records. A record's edges are already the serialized set,
// because validate forbids build dependencies on a binary record. A
// *lock* artifact is different: lockplan.validateEdges deliberately
// permits a binary artifact to record build dependencies and checks
// them against the recipe, while lockgraph.serializes and
// lockplan.traverse never follow them, since a prebuilt artifact was
// not produced from them here.
//
// Walking such an edge anyway breaks both ways: it drops a carried root
// whose build dependency was collected long ago, and it reports version
// conflicts through edges the reader never traverses.
//
// slices.Concat and slices.Clone rather than append: appending build
// deps onto the runtime slice can write into its backing array and
// corrupt the document being read.
func serializedDeps(a lockfile.Artifact) []string {
	if a.Method == lockgraph.MethodSource {
		return slices.Concat(a.RuntimeDeps, a.BuildDeps)
	}
	return slices.Clone(a.RuntimeDeps)
}

// carriedRoots splits the declared packages this write did not verify
// into those the prior document can still describe and those it cannot.
//
// Carrying a root forward keeps a complete committed lock complete when
// one package is installed on a machine that has not installed the
// rest. Omitting the remainder is the deliberate alternative to failing
// the whole write: it yields a stale lock, which has named remedies,
// rather than a lock that claims a package it cannot describe.
//
// Three conditions must all hold, and each rules out a different way of
// asserting something untrue: the prior root must belong to the target
// being rewritten, its version must still match the manifest's pin, and
// its subgraph must be complete in the prior document.
func carriedRoots(req Request) (carried []string, unlocked []UnlockedRoot) {
	for _, name := range slices.Sorted(maps.Keys(req.Declared)) {
		if _, fresh := req.Roots[name]; fresh {
			continue
		}
		id, ok := priorRoot(req, name)
		if !ok {
			unlocked = append(unlocked, UnlockedRoot{
				Target: req.Target, Name: name,
			})
			continue
		}
		carried = append(carried, id)
	}
	return carried, unlocked
}

// priorRoot finds a still-valid prior root for name in the target being
// rewritten.
func priorRoot(req Request, name string) (string, bool) {
	if req.Existing == nil {
		return "", false
	}
	for _, id := range sameTargetRoots(req) {
		n, version, err := lockfile.ParseIdentity(id)
		if err != nil || n != name {
			continue
		}
		// A changed pin means the prior root describes a version nobody
		// asked for any more, so it is not evidence about what is
		// declared now.
		if !lockfile.VersionMatches(version, req.Declared[name]) {
			return "", false
		}
		if !completeEveryPlatform(req.Existing, id) {
			return "", false
		}
		return id, true
	}
	return "", false
}

// sameTargetRoots returns the prior roots of the target being
// rewritten, which are the only ones this write may carry forward.
func sameTargetRoots(req Request) []string {
	if req.Target == "" {
		if req.Existing.Targets.Default == nil {
			return nil
		}
		return req.Existing.Targets.Default.Roots
	}
	return req.Existing.Targets.Host[req.Target].Roots
}

// completeEveryPlatform reports whether id's subgraph is whole on every
// platform id itself records.
//
// Per platform, not once: a node being present is not enough, because
// planning a platform needs that platform's artifact on every node in
// the graph. A root locked for darwin and linux must be complete for
// both, and a root locked only for linux is complete when its linux
// graph is, which is the honest reading of a lock written on another
// machine.
func completeEveryPlatform(lf *lockfile.V1, id string) bool {
	p, ok := lf.Packages[id]
	if !ok {
		return false
	}
	// A node with no artifacts is not vacuously complete, it is
	// unmodelable: it describes nothing on any platform, so a reader
	// planning any platform reports a missing artifact. An empty range
	// would have called that complete.
	if len(p.Artifacts) == 0 {
		return false
	}
	for platform := range p.Artifacts {
		if _, whole := reachFor(lf, []string{id}, platform); !whole {
			return false
		}
	}
	return true
}

// reachFor collects the nodes reachable from ids through one platform's
// artifacts, reporting whether every edge resolved to a node that itself
// records that platform.
//
// The platform check is the point. A node can exist while recording
// nothing for the platform whose artifact depends on it, and planning
// that platform then fails with ErrMissingArtifact. So an artifact whose
// closure is not whole for its own platform is not evidence about it.
// A separate open/done state is why this does not use the output map as
// its visited set. Doing that treats a node currently being walked as
// already finished, so a serialized cycle reports complete — and a cycle
// has no commit order, so lockgraph refuses it. Carrying such a root
// forward would assert a graph no reader can plan.
func reachFor(
	lf *lockfile.V1, ids []string, platform string,
) (map[string]lockfile.Package, bool) {
	out := make(map[string]lockfile.Package)
	const (
		open = 1
		done = 2
	)
	state := make(map[string]int)
	whole := true
	var visit func(key string)
	visit = func(key string) {
		switch state[key] {
		case done:
			return
		case open:
			whole = false
			return
		}
		p, ok := lf.Packages[key]
		if !ok {
			whole = false
			return
		}
		a, ok := p.Artifacts[platform]
		if !ok {
			whole = false
			return
		}
		state[key] = open
		out[key] = p
		for _, dep := range serializedDeps(a) {
			visit(dep)
		}
		state[key] = done
	}
	for _, id := range ids {
		visit(id)
	}
	return out, whole
}

// reachPrior collects the prior nodes reachable from ids.
//
// Unlike reachFor it reports no completeness, because its callers do
// not need one: preserving another target's graph and dropping a
// target both mean keeping what the other targets recorded, not
// repairing a document a reader will reject on its own terms. A
// dangling edge is therefore skipped rather than reported.
func reachPrior(lf *lockfile.V1, ids []string) map[string]lockfile.Package {
	out := make(map[string]lockfile.Package)
	var visit func(key string)
	visit = func(key string) {
		if _, done := out[key]; done {
			return
		}
		p, ok := lf.Packages[key]
		if !ok {
			return
		}
		// Recorded before recursing, so a cyclic prior document
		// terminates here rather than looping; rejecting the cycle is a
		// reader's job.
		out[key] = p
		// Every platform's edges, not just one: a node another target
		// needs only on linux is still referenced.
		for _, a := range p.Artifacts {
			for _, dep := range serializedDeps(a) {
				visit(dep)
			}
		}
	}
	for _, id := range ids {
		visit(id)
	}
	return out
}

// nodeFor assembles one node: this platform's verified artifact, plus
// the foreign-platform artifacts that are still evidence about it.
//
// A foreign hash survives exactly while the node itself is unchanged.
// Once this platform's artifact differs, the foreign entry describes a
// package that no longer exists, and keeping it would leave a stale
// hash that still reads as locked; dropping it forces an honest
// re-lock on that platform instead.
// It returns the node plus the prior nodes that its retained foreign
// artifacts depend on. Retaining an artifact while dropping the nodes
// behind its edges would emit a recorded edge with nothing behind it,
// which reads as a tampered lock rather than a stale one. When the prior
// document cannot supply that closure, the artifact itself is dropped,
// forcing an honest re-lock on that platform.
func nodeFor(
	req Request, key string, r provenance.Record,
) (lockfile.Package, map[string]lockfile.Package) {
	fresh := artifactOf(r)
	arts := map[string]lockfile.Artifact{req.Platform: fresh}
	if req.Existing == nil {
		return lockfile.Package{Artifacts: arts}, nil
	}
	prior, ok := req.Existing.Packages[key]
	if !ok || !sameNode(prior.Artifacts[req.Platform], fresh) {
		return lockfile.Package{Artifacts: arts}, nil
	}
	foreign := make(map[string]lockfile.Package)
	for p, a := range prior.Artifacts {
		if p == req.Platform {
			continue
		}
		nodes, whole := reachFor(req.Existing, serializedDeps(a), p)
		if !whole {
			continue
		}
		arts[p] = a
		mergeAbsent(foreign, nodes)
	}
	return lockfile.Package{Artifacts: arts}, foreign
}

// sameNode reports whether a prior artifact describes the same node as
// a freshly verified one. The version is the map key, so only the
// method and the digest remain to compare.
//
// Comparing the method looks redundant, since the method is one of the
// digest's serialized fields and a real change to it changes the
// digest. It is kept because the case it catches is a prior artifact
// whose method and digest disagree with each other, which is a
// hand-edited or corrupt entry: exactly the entry whose foreign hashes
// must not be carried forward on the strength of its own digest.
func sameNode(prior, fresh lockfile.Artifact) bool {
	return prior.Method == fresh.Method && prior.GraphDigest == fresh.GraphDigest
}

// preserved carries forward every node reachable from a target other
// than the one being rewritten.
//
// Reachability is computed over the prior document's own recorded
// edges, across every platform: a node only a linux target needs is
// still referenced while darwin is being rewritten. Nodes no target
// reaches are dropped, so a removed root does not leave the lock
// growing forever.
//
// A dangling edge is skipped rather than treated as an error. This
// writer's contract is to preserve what other targets recorded, not to
// repair a document a reader will reject on its own terms.
func preserved(req Request, carried []string) map[string]lockfile.Package {
	if req.Existing == nil {
		return make(map[string]lockfile.Package)
	}
	// Carried roots are seeded alongside the other targets' roots. Their
	// completeness was already proven, so the incompleteness this walk
	// tolerates can only come from another target.
	return reachPrior(req.Existing, append(otherRoots(req), carried...))
}

// otherRoots lists the roots of every target except the one being
// rewritten, sorted so the walk is deterministic.
func otherRoots(req Request) []string {
	var ids []string
	t := req.Existing.Targets
	if req.Target != "" && t.Default != nil {
		ids = append(ids, t.Default.Roots...)
	}
	for _, k := range slices.Sorted(maps.Keys(t.Host)) {
		if k != req.Target {
			ids = append(ids, t.Host[k].Roots...)
		}
	}
	slices.Sort(ids)
	return ids
}

// artifactOf is the single translation from a verified record to a
// locked artifact. The record's fields are the digest's serialized
// fields, so the two documents describe one graph by construction
// rather than by two writers agreeing.
func artifactOf(r provenance.Record) lockfile.Artifact {
	return lockfile.Artifact{
		SHA256:         r.SHA256,
		ManifestDigest: r.ManifestDigest,
		Method:         r.Method,
		RuntimeDeps:    r.RuntimeDeps,
		BuildDeps:      r.BuildDeps,
		GraphDigest:    r.GraphDigest,
	}
}

// targetsFor replaces one target's roots and carries every other
// target forward untouched, so a concrete-host operation never
// rewrites a wildcard profile.
//
// The maps are copied rather than reused: the caller's Existing is the
// document it read from disk, and mutating it would leave a failed
// build having already changed what the caller believes is on disk.
func targetsFor(req Request, roots []string) lockfile.Targets {
	out := lockfile.Targets{}
	if req.Existing != nil {
		out.Default = req.Existing.Targets.Default
		if len(req.Existing.Targets.Host) > 0 {
			out.Host = maps.Clone(req.Existing.Targets.Host)
		}
	}
	slices.Sort(roots)
	target := lockfile.Target{Roots: roots}
	if req.Target == "" {
		out.Default = &target
		return out
	}
	if out.Host == nil {
		out.Host = make(map[string]lockfile.Target, 1)
	}
	out.Host[req.Target] = target
	return out
}

// sortedIdentities renders the roots as canonical identities in a
// stable order, so relocking an unchanged closure produces
// byte-identical output instead of diffing on map order.
func sortedIdentities(roots map[string]string) []string {
	ids := make([]string, 0, len(roots))
	for name, version := range roots {
		ids = append(ids, lockgraph.Key(name, version))
	}
	slices.Sort(ids)
	return ids
}

// dropTarget removes one target from a document and with it every
// node no remaining target reaches.
//
// A target with nothing left to root is dropped rather than written
// empty, because an empty target claims the section declares nothing
// to lock while dropping says the section no longer roots anything.
// buildSection is the only caller, and `remove` is the only command
// that reaches it.
//
// Pruning follows the same rule Build applies: nodes no target
// reaches are dropped, so a removed root does not leave the lock
// growing forever. A dangling edge in another target's graph is
// tolerated here for the same reason it is there — preserving what a
// target recorded is this writer's job, repairing it is not.
//
// A nil document stays nil: there is no lock to amend.
func dropTarget(doc *lockfile.V1, target string) *lockfile.V1 {
	if doc == nil {
		return nil
	}
	// The prior targets minus this one are exactly what otherRoots
	// enumerates, so the surviving roots and the surviving targets come
	// from one definition of "every target except this one".
	req := Request{Target: target, Existing: doc}
	out := &lockfile.V1{
		Version:  lockfile.SchemaVersion,
		Packages: reachPrior(doc, otherRoots(req)),
	}
	if target != "" {
		out.Targets.Default = doc.Targets.Default
	}
	for _, k := range slices.Sorted(maps.Keys(doc.Targets.Host)) {
		if k == target {
			continue
		}
		if out.Targets.Host == nil {
			out.Targets.Host = make(map[string]lockfile.Target, len(doc.Targets.Host))
		}
		out.Targets.Host[k] = doc.Targets.Host[k]
	}
	return out
}
