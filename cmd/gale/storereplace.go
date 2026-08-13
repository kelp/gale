package main

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/projects"
	"github.com/kelp/gale/internal/provenance"
)

// errScopeDisagrees reports another scope's lock naming a different
// hash for the identity a replacement is about to overwrite.
//
// A lock-integrity violation rather than a lock to regenerate: two
// scopes want two different sets of bytes at one store path, which the
// store cannot represent and no rewrite of either lock can resolve.
// Only a human can decide which is right.
var errScopeDisagrees = errors.New("another scope's lock names different bytes")

// replaceQuery is one proposed store-directory replacement.
type replaceQuery struct {
	galeHome  string
	storeRoot string
	// selfGaleDir is the initiating scope, exempt from its own veto.
	// Design §13: `gale lock --refresh` exists to move an identity from
	// one hash to another, so counting the initiating lock's current
	// value would reject every refresh as a conflict with itself. It is
	// evaluated against the lock the operation is about to write, which
	// the caller already holds.
	selfGaleDir   string
	name, version string
	// targetDir is the exact store directory the replacement destroys,
	// already resolved by the caller. Never re-resolved here: resolution
	// picks the canonical sibling, so a scope loading a pre-revision
	// bare directory would be checked against a different one — and that
	// shape is precisely upgrade day, which is when this runs.
	targetDir string
	// wantSHA is the hash of the artifact the replacement installs.
	wantSHA  string
	platform string
	// machineWide marks a replacement that is part of ONE proposed
	// state for the whole machine, which today means `gale migrate`.
	//
	// It relaxes exactly one refusal, and design §13 turns on that
	// distinction. A scope that loads the directory and names no hash
	// for it vetoes a PER-SCOPE replacement, because one scope cannot
	// know which bytes its neighbour needs. On upgrade day every scope
	// is legacy and every transitive dependency is such a reference,
	// so the same rule applied machine-wide refuses the one operation
	// that can end the state: "those rules make per-scope replacement
	// refuse on upgrade day, which is correct and would be circular
	// without an escape".
	//
	// Nothing else is relaxed. An unreadable closure still refuses
	// under both, because §13 names unreadable state as a migrate
	// failure in the same sentence, and an explicit hash disagreement
	// refuses under both because that is the other one.
	machineWide bool
}

// checkReplaceable enforces design §13's scope limits before a store
// directory is destroyed.
//
// Replacing an unprovenanced directory is convergence on the one
// supported state — the store never holds two sets of bytes for one
// name@version-revision — but only while every scope agrees on what
// that state is. So this consults every active lockfile on the machine
// in either schema, registered projects and the global scope alike,
// and allows the replacement only where each names the same hash or
// does not reference the identity at all.
//
// A lock is not the only way a scope references a directory. A legacy
// lock records roots only, so a transitive dependency carries no hash
// while the scope still loads it; checkScopeClosure covers that, and
// covers the case where the closure itself could not be read.
//
// Fails closed on a scope it knows about and cannot read. The point of
// the scan is to find disagreement, and a lock that will not parse is
// the one case where gale cannot tell whether it disagrees; treating
// that as consent would destroy bytes for the scope least able to
// object.
//
// The global scope's veto matters most and §13 says why: global has no
// activation gate, so a global lock naming a hash that a project
// replaced underneath stays silently violated until someone runs a
// global sync, which may be never. A project at least refuses
// PATH_add at its next activation.
//
// Unregistered projects remain the accepted residual risk. Nothing can
// see them, and §13 prices that in: such a project is refused
// activation with a conflict error rather than corrupted silently.
//
// What this does NOT prevent, stated because it is easy to assume
// otherwise: replacement can break later accesses through already-open
// shells or running processes in any scope. §13 accepts that, and the
// alternative does not protect them either — it preserves an
// unprovenanced canonical directory indefinitely. What the scan
// prevents is a scope's recorded REQUIREMENT being contradicted, not a
// running process being interrupted.
func checkReplaceable(q replaceQuery) error {
	scopes, err := projects.Scopes(q.galeHome)
	if err != nil {
		return err
	}
	id := lockgraph.Key(q.name, q.version)
	for _, s := range scopes {
		if sameDir(s.GaleDir, q.selfGaleDir) {
			continue
		}
		claim, err := lockedSHA(s.LockPath, q)
		if err != nil {
			return fmt.Errorf(
				"cannot read the lock of %s, so gale cannot tell whether it "+
					"requires %s: %w", s.Label, id, err,
			)
		}
		if claim.named && claim.sha != q.wantSHA {
			return fmt.Errorf(
				"%w: %s requires %s at %s, but this would install %s; "+
					"resolve which is correct rather than letting one "+
					"overwrite the other",
				errScopeDisagrees, s.Label, id, claim.sha, q.wantSHA,
			)
		}
		if claim.statesClosure {
			// A coherent v1 lock records the whole closure, so its
			// silence about this identity IS the answer. Nothing on
			// disk can add a requirement the lock does not name.
			continue
		}
		if err := checkScopeClosure(s, q, id, claim.named); err != nil {
			return err
		}
	}
	return nil
}

// postMigrate is the sequence that clears a legacy scope's veto,
// stated in full because migrate does not finish the job (design
// §13).
//
// `gale migrate` replaces unprovenanced BINARY directories only, so a
// scope holding source-built packages is still legacy when it
// returns, still vetoing, and the user has been sent to a command
// that appeared to work. The order is the instruction: locking a
// scope before its source packages are rebuilt locks nothing.
const postMigrate = "run 'gale migrate' to converge the whole machine " +
	"at once, rebuild the source-method packages it lists, run plain " +
	"'gale lock' in every scope that is still legacy, then retry " +
	"--refresh"

// checkScopeClosure covers what a lock cannot state, and completeness
// of the reading itself.
//
// Two refusals with different scopes. A closure the walk could not
// fully see refuses whatever the scope's lock said, because another
// package in it may have been built against bytes nothing records;
// revision 7 makes that a general veto. A directory inside the closure
// refuses only when the scope named no hash for it — an agreeing hash
// has already settled that question, and claimed is passed in so the
// two are not conflated.
//
// Only the second is relaxed for a machine-wide replacement, and the
// order of the two checks is what makes that possible: the
// completeness refusal is reached first, so migrate keeps it.
func checkScopeClosure(
	s projects.Scope, q replaceQuery, id string, claimed bool,
) error {
	roots, err := generation.AuthoritativeGenerationDirs(s.GaleDir, q.storeRoot)
	if err != nil {
		return fmt.Errorf(
			"cannot read the active generation of %s, so gale cannot tell "+
				"whether it loads %s: %w", s.Label, id, err,
		)
	}
	dirs, complete := generation.AuthoritativeClosure(roots, q.storeRoot)
	if !complete {
		return fmt.Errorf(
			"%w: %s has a store directory whose dependency metadata gale "+
				"cannot read, so it cannot tell what that scope loads; %s",
			errScopeDisagrees, s.Label, postMigrate,
		)
	}
	if q.machineWide {
		// Every scope is a participant in one proposed state rather
		// than a party with a veto, so a reference carrying no hash is
		// not an objection. See replaceQuery.machineWide.
		return nil
	}
	// Canonicalized before comparing. The readers return one spelling,
	// and targetDir arrives however the caller spelled it; on macOS a
	// /var versus /private/var mismatch would silently miss the very
	// directory being protected.
	if !claimed && dirs[canonicalStoreDir(q.targetDir)] {
		return fmt.Errorf(
			"%w: %s loads %s but records no hash for it, so gale cannot "+
				"tell which bytes it needs; %s",
			errScopeDisagrees, s.Label, id, postMigrate,
		)
	}
	return nil
}

// errScopeClosureUnreadable reports a scope whose active closure the
// walk could not enumerate, so it cannot say which of its packages
// record the artifact being replaced.
var errScopeClosureUnreadable = errors.New(
	"an active closure could not be read in full",
)

// errDependentRecord reports a package a scope actively loads whose
// provenance record names the identity a replacement is about to
// overwrite.
//
// Design §5: replacing an artifact changes the graph_digest of every
// node above it, and §11 and §13 permit regenerating a reverse
// dependent only where its directory is absent, unreferenced, or
// unprovenanced. A referenced one that records the target is a
// conflict.
//
// A record here is one that PARSES, which is weaker than §5's
// "validly provenanced" and weaker in the safe direction: the digest
// is not recomputed, so a record whose closure would not verify still
// refuses. Recomputing it would mean verifying every active directory
// before every replacement, and the answer would not change the
// outcome — a record that names the target is one the replacement
// invalidates either way.
var errDependentRecord = errors.New(
	"a package a scope loads records the artifact being replaced",
)

// dependentQuery is one proposed replacement, as the reverse-dependent
// scan sees it.
type dependentQuery struct {
	// galeDir is the scope whose active generation is walked, and
	// label how a refusal names it.
	galeDir, label string
	storeRoot      string
	// id is the identity being replaced, in name@version form.
	id string
	// targetDir is the physical directory the replacement destroys.
	// Not derived from id: a pre-revision install lives in a bare
	// directory that the canonical spelling would miss entirely, and
	// the scan would then exclude the wrong directory from its own
	// judgement while missing the one actually at risk.
	targetDir string
	// remedy is appended to the unreadable-closure refusal. It is a
	// parameter because the two commands have different exits: a
	// per-scope refresh is told to run migrate, and telling migrate
	// to run migrate would be the circularity §13 exists to break.
	remedy string
}

// checkDependentsIn refuses a replacement that would invalidate a
// record one scope actively loads, BEFORE any bytes are destroyed.
//
// Discovering the conflict on the way out would be no cheaper for
// the user and a great deal more expensive for the store: the run
// would have destroyed the candidate to learn something it could
// have read first, and the reverse dependent it stopped for would
// still be stale.
//
// A closure the walk could not read in full refuses the replacement.
// Unreadable dependency metadata blocks the descent, so a dependent
// below it is invisible and the scan would report "no conflict"
// having seen nothing — absence of evidence standing in for evidence
// of absence, on a decision that destroys bytes.
//
// The candidate's own directory is excluded from that judgement by
// ReferenceClosure. Its metadata belongs to the artifact being
// superseded, so letting it decide would let an unprovenanced legacy
// directory refuse its own replacement, which is the deadlock cycle
// 22 removed once already.
func checkDependentsIn(q dependentQuery) error {
	roots, err := generation.AuthoritativeGenerationDirs(
		q.galeDir, q.storeRoot,
	)
	if err != nil {
		return fmt.Errorf(
			"reading the active generation of %s before replacing %s: %w",
			q.label, q.id, err,
		)
	}
	target := canonicalStoreDir(q.targetDir)
	dirs, complete := generation.ReferenceClosure(roots, q.storeRoot, target)
	if !complete {
		return fmt.Errorf(
			"%w in %s, so gale cannot tell which packages there record "+
				"%s; %s", errScopeClosureUnreadable, q.label, q.id, q.remedy,
		)
	}
	for _, dir := range slices.Sorted(maps.Keys(dirs)) {
		if dir == target {
			continue
		}
		// An absent or unreadable record has nothing to invalidate:
		// §7's all-or-nothing rule means such a directory attests no
		// closure, so replacing what it links changes no claim it made.
		rec, err := provenance.ReadUnverified(dir)
		if err != nil {
			continue
		}
		if !slices.Contains(rec.RuntimeDeps, q.id) &&
			!slices.Contains(rec.BuildDeps, q.id) {
			continue
		}
		return fmt.Errorf(
			"%w: %s is active in %s and its provenance records %s, so "+
				"replacing it would leave that record describing a closure "+
				"the store no longer holds; re-pin %s to a new "+
				"version-revision instead",
			errDependentRecord, rec.Key(), q.label, q.id, rec.Key(),
		)
	}
	return nil
}

// scopeClaim is what one scope's lock says about the identity being
// replaced.
//
// statesClosure is the field that keeps the completeness rule from
// over-reaching. A coherent v1 lock records the entire closure, so its
// silence about an identity is a complete answer and no amount of
// missing dependency metadata changes that. Revision 7 scopes the
// metadata-based rule to a legacy scope for exactly that reason, and
// applying it to v1 would be unrepairable: a valid v1 package may
// carry no .gale-deps.toml at all, so such a scope would veto every
// replacement on the machine forever, with migrate unable to help
// because its directory is already provenanced.
type scopeClaim struct {
	sha   string
	named bool
	// statesClosure means the lock is a complete record of what the
	// scope requires — true only for a v1 document that passed the
	// coherence check.
	statesClosure bool
}

// lockedSHA reports the hash one scope's lock records for the identity
// being replaced. The bool separates "records nothing" from a hash
// that happens to be empty.
//
// Both schemas are consulted, per revision 7. Consulting only v1 was
// the original text and the original implementation, and it was wrong:
// on upgrade day every scope holds a legacy lock, and that lock records
// a sha256 per package. Skipping it means reading a file that says "I
// require these bytes", finding different ones, and replacing anyway.
//
// Failing closed on any legacy lock was the other candidate and it
// deadlocks the upgrade, since no scope can mint a v1 lock before a
// replacement has happened. The escape is that `gale migrate` converges
// the whole machine at once.
func lockedSHA(lockPath string, q replaceQuery) (scopeClaim, error) {
	view, err := lockfile.Load(lockPath)
	if err != nil {
		return scopeClaim{}, err
	}
	switch view.Kind {
	case lockfile.KindAbsent:
		return scopeClaim{}, nil
	case lockfile.KindLegacy:
		sha, ok := legacySHA(view.Legacy, q)
		return scopeClaim{sha: sha, named: ok}, nil
	case lockfile.KindV1:
		sha, ok, verr := v1SHA(
			view.V1, lockgraph.Key(q.name, q.version), q.platform,
		)
		// statesClosure only when the document is coherent, which
		// v1SHA has just proved: a lock that omits a node it
		// references cannot be said to state anything.
		return scopeClaim{sha: sha, named: ok, statesClosure: verr == nil}, verr
	default:
		// A schema this build cannot model may require anything, so it
		// is treated like any other scope gale cannot read.
		return scopeClaim{}, fmt.Errorf("unhandled lockfile kind %s", view.Kind)
	}
}

// legacySHA reads the flat pre-enforcement schema, which is keyed by
// package name and carries no platform dimension.
//
// The hash is treated as a conservative, platformless byte claim.
// Equality proves agreement on bytes; disagreement vetoes even though
// the recorded hash may belong to another platform, because a project
// gale.lock travels in git and may have been written elsewhere.
// Over-refusing is safer than destroying bytes another scope names.
//
// Versions compare through VersionMatches: the legacy file spells a
// version bare where a v1 identity is canonical, so "1.7" and "1.7-1"
// name the same store directory.
func legacySHA(lf *lockfile.LockFile, q replaceQuery) (string, bool) {
	entry, ok := lf.Packages[q.name]
	if !ok || entry.SHA256 == "" {
		return "", false
	}
	if entry.Version != q.version &&
		!lockfile.VersionMatches(q.version, entry.Version) {
		return "", false
	}
	return entry.SHA256, true
}

// v1SHA reads the enforced schema, for the current platform.
//
// The lookup is over the whole document rather than the effective root
// set for this host. A transitive dependency is exactly the kind of
// entry a shared store directory belongs to, and a node the lock
// carries is a node that lock may come to need; for an operation that
// destroys bytes, counting every reference is the conservative
// direction.
func v1SHA(lf *lockfile.V1, id, platform string) (string, bool, error) {
	// Before any negative answer is trusted. Load validates syntax and
	// schema, not coherence, so a lock that roots or depends on this
	// identity while omitting its node parses cleanly and would report
	// "not referenced" for the very entry it is missing — permission
	// to destroy the directory, derived from the document's own defect.
	if err := lf.CheckReferences(); err != nil {
		return "", false, err
	}
	pkg, ok := lf.Packages[id]
	if !ok {
		return "", false, nil
	}
	art, ok := pkg.Artifacts[platform]
	if !ok {
		return "", false, nil
	}
	return art.SHA256, true, nil
}

// canonicalStoreDir resolves a store directory's spelling without
// touching its version, matching what the generation and closure
// readers return. It delegates to farm.CanonicalDir, which is the
// one spelling those readers use.
func canonicalStoreDir(dir string) string {
	return farm.CanonicalDir(dir)
}
