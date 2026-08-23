package main

import (
	"fmt"

	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/lockplan"
)

// lockedSyncPlan decides how a sync treats the lockfile it found, and
// resolves the complete plan before any install runs.
//
// Three results rather than two because "unlocked" is not one state.
// A nil plan with a nil error means proceed unlocked, and warn carries
// the single line saying why — a lock that has never been written is
// legitimate, and it has to say so out loud. A non-nil error means
// refuse; the plan is always nil then, so no caller can install from a
// half-built plan.
//
// Fail-closed is the whole of it (design §9). Every condition that
// stops a locked sync — legacy schema, unknown version, stale roots, a
// missing package, dep or platform entry, a recipe that no longer backs
// the locked node — is an error here, before the installer sees
// anything. There is no bypass: `--no-frozen` was the escape hatch and
// it was removed with the fetch cutover, so absence is the only route
// to a nil plan.
func lockedSyncPlan(
	lv *lockfile.View, req lockplan.Request,
) (*lockplan.Plan, string, error) {
	switch lv.Kind {
	case lockfile.KindAbsent:
		return nil, "No gale.lock — installing from recipes without " +
			"integrity enforcement. Run 'gale lock' to record what is " +
			"installed.", nil

	case lockfile.KindLegacy:
		// The legacy file records checksums nothing ever enforced and
		// has no platform dimension, so it cannot answer the questions
		// a locked sync asks. Silently treating it as absent was
		// considered and rejected (design §9): it downgrades a security
		// control without telling anyone.
		return nil, "", fmt.Errorf(
			"%w: gale.lock predates integrity enforcement; run 'gale lock' to regenerate it",
			lockfile.ErrLegacySchema,
		)

	case lockfile.KindV1:
		req.Lock = lv.V1
		plan, err := lockplan.Build(req)
		if err != nil {
			return nil, "", err
		}
		return plan, "", nil

	default:
		// Load classifies into exactly the three kinds above, so this
		// is unreachable rather than defensive. It fails closed anyway:
		// a kind this function does not understand must never fall
		// through to unlocked mode, which is what a bare nil return
		// would mean.
		return nil, "", fmt.Errorf("%w: unhandled lockfile kind %s",
			lockfile.ErrMalformed, lv.Kind)
	}
}

// rejectSourceOnly refuses --build against a plan that locks any node
// to a binary artifact (design §9, acceptance 9).
//
// The installer already refuses to demote a locked binary node, but it
// discovers that per node, after installing that node's dependencies.
// Checking the whole plan up front is what "before any dep or store
// mutation" means: the plan is complete before the first install runs,
// so nothing has to be undone.
//
// The check is over every node, not only the roots. --build means
// build everything from source, and a transitive binary node is just
// as unbuildable as a declared one.
//
// Ordinary failure class, not integrity: nothing disagrees with the
// lock. The user asked for something the lock forbids.
func rejectSourceOnly(plan *lockplan.Plan) error {
	if plan == nil {
		return nil
	}
	// Reported in plan order so the same lock always names the same
	// package first; ranging the node map would pick one at random and
	// the message would differ run to run.
	for _, key := range plan.Order {
		n := plan.Nodes[key]
		if n.Method == lockgraph.MethodBinary {
			return fmt.Errorf(
				"--build conflicts with gale.lock: %s is locked to a "+
					"binary artifact; run 'gale lock' after changing the "+
					"recipe to a source build, or drop --build",
				lockgraph.Key(n.Name, n.Version),
			)
		}
	}
	return nil
}
