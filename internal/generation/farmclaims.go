package generation

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"runtime"
	"slices"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/filelock"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/projects"
	"github.com/kelp/gale/internal/store"
)

// This file collects the claimant set for the farm guard (design
// §4): the scopes whose closures the shared farm must keep
// satisfied. It lives in this package because the unlocked side of
// a claim is the scope's active generation, which only this package
// can read, and internal/farm cannot import it back.
//
// Claimants are the registered projects from the ~/.gale/projects
// walk plus the global scope, which registerProject skips by design
// and a registry-only scan would therefore miss entirely.
// Unregistered projects and already-open shells remain the
// acknowledged limitation until the scoped-farm follow-up.

// FarmClaimants collects every scope that claims sonames in the
// shared farm, excluding the scope at selfGaleDir: the initiating
// scope's claim is its proposed closure, which the caller supplies
// to the guard itself, and passing its old claim here would
// deadlock the scope against its own update, remove and repair.
//
// A scope claims its active generation's farm closure — what its
// binaries resolve through the farm right now (design §4's "current
// active closure") — and, where it has a v1 lock, the runtime
// closure that lock records as well.
//
// Both, never one or the other. The lock is the authority on what
// the scope REQUIRES, including a version this machine has not
// installed yet, which provenance and the generation by
// construction cannot describe. The generation is the authority on
// what the scope is LOADING, which a lock stops describing the
// moment a repin lands: between the edit and the sync that
// satisfies it, the lock names the new version while the live
// generation still links the old one. Substituting the lock for the
// generation there unclaims a soname a running binary resolves
// through, and another scope may then retarget it underneath.
//
// A scope that is known but cannot be read is returned with Err
// set, and the guard fails closed on it. Scopes with nothing to
// claim are dropped.
func FarmClaimants(storeRoot, selfGaleDir string) []farm.Claimant {
	// One definition of the scope set, shared with the migration veto
	// (design §13), which asks a different question of exactly these
	// scopes. Two walks would be two chances to miss one, and a missed
	// scope does not fail here — it silently approves.
	scopes, err := projects.Scopes(filepath.Dir(storeRoot))
	if err != nil {
		// The registry names scopes the walk cannot see without it,
		// so an unreadable registry fails the guard closed rather
		// than silently shrinking the claimant set.
		return []farm.Claimant{{Label: "the project registry", Err: err}}
	}

	host := config.CurrentHost()
	platform := runtime.GOOS + "/" + runtime.GOARCH
	var out []farm.Claimant
	for _, s := range scopes {
		if samePath(s.GaleDir, selfGaleDir) {
			continue
		}
		dirs, err := scopeClosureDirs(
			s.LockPath, s.GaleDir, storeRoot, host, platform,
		)
		if err != nil {
			out = append(out, farm.Claimant{Label: s.Label, Err: err})
			continue
		}
		if len(dirs) > 0 {
			out = append(out, farm.Claimant{
				Label: s.Label, Claims: farm.At(dirs...),
			})
		}
	}
	return out
}

// ProposedClaimant builds the initiating scope's own claim: the
// farm closure it will have once the operation completes.
//
// Design §4 step 1 names it a claimant alongside every other
// scope, and without it a whole class of self-harm passes. A scope
// installing a second version of a package one of its own binaries
// links repoints the shared soname under that binary: farm.Populate
// allows it because both dirs belong to the same package, the batch
// being populated is one dir so it cannot conflict with itself, and
// a project generation rebuilds into its own lib dir, so the
// whole-closure check at the rebuild boundary never runs for the
// scope that caused it.
//
// changed maps a package name to the store-dir basename the
// operation leaves it at; an empty version removes it. The claim is
// then recomputed from that package set rather than patched, which
// is what separates a superseded root from a version a dependent
// still records: an updated root disappears, while the same version
// reached through another package's deps stays.
//
// A dir the operation is about to create contributes nothing here,
// because it is not in the store yet. It does not need to: it is
// the mapping being proposed, and the guard compares this claim
// against it.
//
// LIMIT, and it is a real one: this models the ACTIVE generation
// plus the change in front of it, which is the whole operation only
// when the operation is a single commit against a settled scope. A
// multi-package sync installs every root before it rebuilds
// anything, so each per-commit call here sees the same pre-sync
// generation, and two roots that first meet in the final closure —
// say one recording zlib 1 and another zlib 2 — conflict in a
// closure no single call ever sees. Catching that needs the
// command's eventual package set together with the staged dep
// metadata, which is P7's whole-plan batch. Until then this covers
// a change against an established closure and does not enforce the
// complete proposed-closure rule.
func ProposedClaimant(
	changed map[string]string, galeDir, storeRoot string,
) (farm.Claimant, error) {
	c := farm.Claimant{Label: "this scope"}
	pkgs, err := CurrentVersions(galeDir, storeRoot)
	if err != nil {
		return c, fmt.Errorf("reading active generation: %w", err)
	}
	for name, version := range changed {
		if version == "" {
			delete(pkgs, name)
			continue
		}
		pkgs[name] = version
	}
	dirs, err := FarmStoreDirsStrict(pkgs, storeRoot)
	if err != nil {
		return c, fmt.Errorf("reading the proposed closure: %w", err)
	}
	c.Claims = farm.At(dirs...)
	return c, nil
}

// ProposedClaimantStaged is ProposedClaimant for a caller whose new
// bytes are still STAGED, which is every guard that runs before the
// commit it is guarding.
//
// ProposedClaimant alone is wrong there, and wrong in the one
// direction that matters. It resolves each changed package to its
// canonical store dir, and before the rename that dir still holds
// the artifact being replaced, so the "proposed" closure describes
// the OLD libraries. A guard asking whether the scope still needs a
// library the replacement drops would find it in the scope's own
// claim and refuse the replacement on the scope's behalf: the verb
// veto design §4 forbids, deadlocking a scope against its own
// repair.
//
// So each changed package's own dir is dropped from the claim and
// its placement carries it instead: sonames read from staging,
// targets reported at the canonical path.
//
// Its DEPENDENCIES come from the STAGED metadata too, not from what
// the canonical directory currently records. Reading the old
// metadata is not merely imprecise, it is fail-open in one
// direction: a candidate that ADDS a runtime dependency leaves that
// dependency out of the claim, and if the dependency provides a
// library the root itself stopped providing, the guard sees no claim
// on it and approves deleting a farm entry the proposed closure
// needs.
func ProposedClaimantStaged(
	placements []farm.Placement, galeDir, storeRoot string,
) (farm.Claimant, error) {
	return proposedClaimant(placements, galeDir, storeRoot, false)
}

// ProposedClaimantRequired is ProposedClaimantStaged for a guard
// about to DELETE something: a dependency the closure names and the
// store no longer has makes the claim unusable rather than smaller.
//
// The asymmetry is deliberate. Populating retargets a soname, and a
// dependency that is gone provides none, so a shrinking claim is
// honest there and has been since FarmStoreDirsStrict. Deleting a
// farm entry on the strength of a closure that could not be
// enumerated is the fail-open case, so the strict walk is used
// exactly where bytes go away.
func ProposedClaimantRequired(
	placements []farm.Placement, galeDir, storeRoot string,
) (farm.Claimant, error) {
	return proposedClaimant(placements, galeDir, storeRoot, true)
}

func proposedClaimant(
	placements []farm.Placement, galeDir, storeRoot string, require bool,
) (farm.Claimant, error) {
	c := farm.Claimant{Label: "this scope"}
	pkgs, err := CurrentVersions(galeDir, storeRoot)
	if err != nil {
		return c, fmt.Errorf("reading active generation: %w", err)
	}
	// Canonicalized, because the walk returns canonicalized paths and
	// this map is compared against them. filepath.Clean alone leaves
	// macOS /var and /private/var as different strings, and the
	// mismatch is silent: the staged dir survives the filter below,
	// its OLD sonames enter the claim, and the scope vetoes its own
	// refresh.
	staged := make(map[string]string, len(placements))
	for _, p := range placements {
		staged[canonicalDir(p.FinalDir)] = p.ScanDir
	}
	for name, version := range ChangedBy(placements) {
		pkgs[name] = version
	}

	roots := make([]string, 0, len(pkgs))
	for name, version := range pkgs {
		roots = append(roots, resolveStoreDir(storeRoot, name, version))
	}
	// The whole closure in one walk, with staging substituted at the
	// dirs being replaced. Walking the canonical dirs first and
	// patching afterwards reads the artifact being superseded: its
	// metadata decides the claim, so malformed metadata in an
	// unprovenanced legacy artifact would refuse the operation that
	// replaces it, and a dependency only the OLD closure records
	// would be claimed as though the replacement kept it.
	closure, complete := ProposedClosure(roots, storeRoot, staged, require)
	if !complete {
		// An unreadable claim, handled as every other one is: the
		// guard fails closed rather than proceeding on a closure it
		// could not enumerate.
		c.Err = errStagedClosure
		return c, nil
	}
	// One list, built once. The committed part of the closure claims
	// from where it sits; the staged part claims through the caller's
	// own placements, which read from staging and report the
	// canonical destination.
	for _, d := range slices.Sorted(maps.Keys(closure)) {
		if _, isStaged := staged[canonicalDir(d)]; !isStaged {
			c.Claims = append(c.Claims, farm.At(d)...)
		}
	}
	c.Claims = append(c.Claims, placements...)
	return c, nil
}

// errStagedClosure marks a proposed claim that could not be
// enumerated in full: unreadable metadata anywhere in it, or a
// dependency it names that is no longer on disk.
var errStagedClosure = errors.New(
	"the proposed closure could not be read in full",
)

// ChangedBy reads the package set a batch of placements leaves
// behind, keyed the way ProposedClaimant expects. A placement's
// canonical dir is <storeRoot>/<name>/<version-revision>, which is
// the only place the identity of a staged install is recorded.
func ChangedBy(placements []farm.Placement) map[string]string {
	out := make(map[string]string, len(placements))
	for _, p := range placements {
		dir := filepath.Clean(p.FinalDir)
		out[filepath.Base(filepath.Dir(dir))] = filepath.Base(dir)
	}
	return out
}

// scopeClosureDirs resolves one scope's claimed closure to store
// dirs: its active generation always, plus its v1 lock's runtime
// closure when it has one.
func scopeClosureDirs(
	lockPath, galeDir, storeRoot, host, platform string,
) ([]string, error) {
	pkgs, err := CurrentVersions(galeDir, storeRoot)
	if err != nil {
		return nil, fmt.Errorf("reading active generation: %w", err)
	}
	dirs, err := FarmStoreDirsStrict(pkgs, storeRoot)
	if err != nil {
		return nil, fmt.Errorf("reading active generation: %w", err)
	}

	view, err := lockfile.Load(lockPath)
	if err != nil {
		return nil, fmt.Errorf("reading lock: %w", err)
	}
	switch view.Kind {
	case lockfile.KindV1:
		ids, err := lockRuntimeClosure(view.V1, host, platform)
		if err != nil {
			return nil, err
		}
		locked, err := identityStoreDirs(storeRoot, lockOnly(ids, dirs))
		if err != nil {
			return nil, err
		}
		return append(dirs, locked...), nil
	case lockfile.KindAbsent, lockfile.KindLegacy:
		// A legacy lock predates enforcement and records no closure,
		// so it adds nothing to what the generation already claims.
		return dirs, nil
	default:
		// A kind this build does not know claims something it
		// cannot read; fail closed like any unreadable scope.
		return nil, fmt.Errorf("unknown lock kind %s", view.Kind)
	}
}

// lockRuntimeClosure walks the lock's recorded runtime edges from
// the effective roots and returns every canonical identity
// reachable, sorted.
//
// Runtime edges only: farm links exist so binaries resolve the
// dylibs they load at run time, and the farm mapping generations
// maintain (FarmStoreDirs) walks the recorded runtime closure
// alone. Build deps never enter the farm, so claiming them would
// refuse operations over sonames no claimant binary loads.
//
// A node without an artifact for this platform contributes nothing
// and is not followed: platform minting is all-or-nothing (design
// §10), so a missing artifact means the lock was not minted for
// this platform and the scope cannot activate this closure here. A
// root or edge naming a node the lock does not define is a
// malformed lock and fails closed.
func lockRuntimeClosure(lf *lockfile.V1, host, platform string) ([]string, error) {
	roots, err := lf.EffectiveRoots(host)
	if err != nil {
		return nil, err
	}
	queue := make([]string, 0, len(roots))
	for _, id := range roots {
		queue = append(queue, id)
	}
	slices.Sort(queue)

	visited := map[string]bool{}
	var out []string
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if visited[id] {
			continue
		}
		visited[id] = true
		pkg, ok := lf.Packages[id]
		if !ok {
			return nil, fmt.Errorf("%w: %s", lockfile.ErrMissingNode, id)
		}
		art, minted := pkg.Artifacts[platform]
		if !minted {
			continue
		}
		out = append(out, id)
		queue = append(queue, art.RuntimeDeps...)
	}
	slices.Sort(out)
	return out, nil
}

// lockOnly drops the locked identities of packages the active
// generation already links, keeping the rest in order.
//
// The two sources disagree exactly when a repin has landed and its
// sync has not: the lock names the new version while the generation
// still links the old one. Both cannot be claimed, because one
// soname cannot map to two store dirs and the guard refuses a
// claimant that contradicts itself — which, left unresolved here,
// would freeze every operation on the machine for as long as any
// one project has an unsynced repin.
//
// The generation wins. Its binaries resolve that target right now,
// and that is what another scope must not pull out from under them;
// the locked version is loaded by nothing yet, and the scope's own
// sync is what will claim it. The tie-break lives here because only
// this function can tell which dir came from which source.
//
// genDirs, not the generation's package map: FarmStoreDirs walks
// each entry's recorded deps too, so the live closure includes
// transitive packages that are not generation entries at all, and
// those tear against a lock exactly as roots do. It also drops dirs
// missing from the store, so a generation entry with no bytes
// suppresses nothing and the lock's claim stands.
func lockOnly(ids, genDirs []string) []string {
	live := make(map[string]bool, len(genDirs))
	for _, dir := range genDirs {
		live[filepath.Base(filepath.Dir(dir))] = true
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		name, _, err := lockfile.ParseIdentity(id)
		// A malformed identity is kept so identityStoreDirs refuses
		// it: silently dropping it would turn an unreadable claim
		// into a smaller one.
		if err != nil || !live[name] {
			out = append(out, id)
		}
	}
	return out
}

// identityStoreDirs resolves canonical identities to store dirs.
// Identities come from lock dep lists, which nothing validated yet,
// so a malformed one is refused rather than looked up: a missed
// lookup is indistinguishable from a package with no dylibs.
func identityStoreDirs(storeRoot string, ids []string) ([]string, error) {
	st := store.NewStore(storeRoot)
	dirs := make([]string, 0, len(ids))
	for _, id := range ids {
		name, version, err := lockfile.ParseIdentity(id)
		if err != nil {
			return nil, err
		}
		dirs = append(dirs, st.ResolveDir(name, version))
	}
	return dirs, nil
}

// guardedRebuildDirs is the farm-guard step every rebuild boundary
// runs before its swap: it collects the claimant set and returns
// the store dirs the farm rebuild must use — the proposed closure
// plus every claim, so a wipe-and-recreate rebuild cannot delete a
// soname only another scope claims. A conflicting claim refuses
// the whole operation, which is why callers must run this BEFORE
// swapping the generation: after a refusal nothing may have moved.
//
// It guards in BOTH scopes. It used to exempt a project-scoped
// galeDir, on the reasoning that a project owns a local lib dir no
// binary rpath resolves through. The reasoning was sound and the
// conclusion was wrong: the rebuild it exempted was pointed at that
// local dir, but the shared farm was still being mutated on the same
// operation, per commit, from the installer. Now that population is
// deferred to this boundary and targets the shared farm at either
// scope, an exemption here would leave a project's whole-closure
// mutation unguarded entirely (design revision 6, section 4).
func guardedRebuildDirs(
	pkgs map[string]string, galeDir, storeRoot string,
) ([]string, error) {
	proposed := FarmStoreDirs(pkgs, storeRoot)
	return farm.GuardRebuild(proposed, FarmClaimants(storeRoot, galeDir))
}

// RebuildFarm establishes the shared farm the whole machine claims,
// once, under the generation lock.
//
// The machine-wide sibling of guardedRebuildDirs, for the one
// operation no scope's own Build can finish: `gale migrate`
// relocating a pre-revision install (design §13). Those commits defer
// their farm work, because the per-commit guard has to refuse them —
// a scope loading the library out of the BARE directory claims that
// soname there, and the canonical copy proposes it somewhere else —
// so when the pass is done the farm still describes directories that
// are about to be deleted. Rebuilding each scope's generation does
// not cover it either: a scope that requires the identity through its
// LOCK alone has no generation to rebuild.
//
// No proposed closure and no scope exempt, which is one statement
// rather than two. Nothing is being installed here and no scope is
// initiating, so the claimant set IS the proposed state: every
// scope's current closure, resolved through the store, where a bare
// "1.0" now reaches the canonical "1.0-1" beside it.
//
// The guard still runs. Rebuild wipes the farm before it repopulates,
// so a claimant set that cannot be satisfied would leave one scope's
// binaries resolving another scope's library; a refusal has to cost
// nothing, which is only true while nothing has moved.
func RebuildFarm(storeRoot string) error {
	// The lock Build takes (see build), so no scope can swap a
	// generation between the claim walk and the rebuild resting on it.
	lockPath := filepath.Join(filepath.Dir(storeRoot), "generation.lock")
	return filelock.With(lockPath, func() error {
		dirs, err := farm.GuardRebuild(nil, FarmClaimants(storeRoot, ""))
		if err != nil {
			return err
		}
		if err := farm.Rebuild(
			dirs, farm.DirFromStoreRoot(storeRoot),
		); err != nil {
			return fmt.Errorf("rebuilding the shared library farm: %w", err)
		}
		return nil
	})
}

// samePath reports whether two paths name the same location,
// resolving symlinks first so macOS /var vs /private/var spellings
// compare equal (the rule cmd/gale's sameDir applies). Resolution
// walks up to the nearest existing ancestor because the compared
// dirs may not exist yet — a project's .gale before its first sync
// — and EvalSymlinks fails outright on a missing path, which would
// make the two spellings of the initiating scope compare unequal
// and turn its own superseded claim into a self-veto.
func samePath(a, b string) bool {
	return resolveExisting(filepath.Clean(a)) ==
		resolveExisting(filepath.Clean(b))
}

// resolveExisting resolves symlinks in the longest existing prefix
// of a cleaned path and rejoins the missing tail unchanged.
func resolveExisting(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	parent := filepath.Dir(path)
	if parent == path {
		return path // filesystem root; nothing left to resolve
	}
	return filepath.Join(resolveExisting(parent), filepath.Base(path))
}
