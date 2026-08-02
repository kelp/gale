package installer

import (
	"fmt"

	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/lockplan"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/recipe"
)

// plannedNode returns the plan's node for one package. The bool
// reports whether this installer is locked at all, so an unlocked
// caller is distinguished from a locked one whose lookup succeeded
// rather than being folded into a nil node.
//
// A locked installer asked for a package the plan does not name, or
// names at another version, fails: the plan is the complete set of
// installs, so either case means live resolution reached a locked
// operation.
func (inst *Installer) plannedNode(
	name, storeVersion string,
) (lockplan.Node, bool, error) {
	if inst.Plan == nil {
		return lockplan.Node{}, false, nil
	}
	n, ok := inst.Plan.ForName(name)
	if !ok {
		return lockplan.Node{}, true, fmt.Errorf("%w: %s", ErrUnplanned, name)
	}
	if n.Version != storeVersion {
		return lockplan.Node{}, true, fmt.Errorf(
			"%w: %s resolved to %s, the lock names %s",
			ErrUnplanned, name, storeVersion, n.Version,
		)
	}
	return n, true, nil
}

// resolveDep answers with the recipe for one dependency name. Under
// a plan the answer comes from the plan, which is what makes the
// lock the exclusive version selector on the recursion that
// installDepsInner and ResolveDirectDeps walk: those walk recipe
// dependency NAMES, and before #182 each name went straight to the
// registry, so a committed lock never reached a transitive install.
//
// A name the plan does not carry is ErrUnplanned rather than a
// registry lookup. Plan construction already proved every locked
// node's recipe declares exactly the locked edges
// (lockplan.validateEdges), so an unknown name here means the recipe
// on disk has moved out from under the lock.
func (inst *Installer) resolveDep(name string) (*recipe.Recipe, error) {
	if inst.Plan == nil {
		return inst.Resolver(name)
	}
	n, ok := inst.Plan.ForName(name)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnplanned, name)
	}
	return n.Recipe, nil
}

// canResolve reports whether dependency resolution is possible at
// all. A locked installer needs no Resolver: the plan carries every
// recipe it will use.
func (inst *Installer) canResolve() bool {
	return inst.Plan != nil || inst.Resolver != nil
}

// lockedCacheHit answers whether an occupied store dir satisfies the
// locked node. The bool reports a hit; false with a nil error means
// nothing is on disk and the install proceeds.
//
// There is deliberately no repair branch. Design §4 permits
// committing only ABSENT canonical dirs and prohibits in-plan
// replaceStoreDir, so an occupied dir that does not attest the lock
// is a conflict the operator resolves (gale lock --refresh, gale
// migrate), never something a sync silently overwrites. That is what
// makes acceptance 2 hold: the directory survives byte-for-byte.
//
// Resolution goes through Store.StorePath rather than a canonical
// join, so the check looks at the directory the generation would
// actually link — including a pre-revision bare dir, which then
// reports as unprovenanced and routes to gale migrate rather than
// being missed and shadowed by a fresh canonical install beside it.
func (inst *Installer) lockedCacheHit(
	n lockplan.Node, name, version string,
) (InstallResult, bool, error) {
	dir, ok := inst.Store.StorePath(name, n.Version)
	if !ok {
		return InstallResult{}, false, nil
	}
	if _, err := provenance.VerifyAgainstLock(dir, n.Graph, n.GraphDigest); err != nil {
		return InstallResult{}, false, fmt.Errorf("%s@%s: %w", name, n.Version, err)
	}
	return InstallResult{
		Name:           name,
		Version:        version,
		Method:         MethodCached,
		SHA256:         n.SHA256,
		ManifestDigest: n.ManifestDigest,
	}, true, nil
}

// VerifyPlanned reports whether the store already holds the locked
// artifact for name, mutating nothing. It exists for dry run, which
// must answer the same question a real locked sync asks — is this
// directory absent, does it attest the lock, or is it a conflict —
// without installing anything.
//
// It runs the identical check, deliberately: a dry run that computed
// "up to date" a cheaper way would be free to disagree with the sync
// it is predicting, and the disagreement would show up as a sync
// failing with an integrity error that the dry run called clean.
func (inst *Installer) VerifyPlanned(name string) (bool, error) {
	if inst.Plan == nil {
		return false, nil
	}
	n, ok := inst.Plan.ForName(name)
	if !ok {
		return false, fmt.Errorf("%w: %s", ErrUnplanned, name)
	}
	_, hit, err := inst.lockedCacheHit(n, name, n.Version)
	return hit, err
}

// checkSourceArtifact compares a finished build's archive hash
// against the locked artifact, before anything is promoted into the
// store (design §4 invariant A, acceptance 10). The comparison is on
// build.BuildResult.SHA256, computed from the archive, never on a
// rehash of the store directory: fixupExtracted rewrites Mach-O load
// commands in staging, so post-fixup bytes cannot match.
func (inst *Installer) checkSourceArtifact(name, storeVersion, got string) error {
	planned, locked, err := inst.plannedNode(name, storeVersion)
	if err != nil || !locked || planned.SHA256 == got {
		return err
	}
	return fmt.Errorf(
		"%w: %s built to %s, the lock names %s",
		provenance.ErrInvalid, lockgraph.Key(name, storeVersion),
		got, planned.SHA256,
	)
}
