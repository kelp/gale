package main

import (
	"errors"

	"github.com/kelp/gale/internal/activation"
	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/provenance"
)

// Exit codes. A Go error type is invisible to the shell scripts
// docs/ci-cd.md tells users to write, and a pipeline must be able to
// tell "artifact tampered" from "build broke". gale used only exit 1
// before enforcement, so the taxonomy is additive and breaks
// nothing.
//
// The full taxonomy, of which this file implements what has
// producers today:
//
//	1  ordinary failure: build error, network error, usage error
//	3  lock integrity violation: artifact SHA, manifest digest,
//	   provenance or graph_digest mismatch; provenance conflict;
//	   cross-project farm conflict
//	4  lock unusable: any lock that is present but cannot be parsed
//	   or fully modeled
//	5  activation drift: the active generation does not match the
//	   target graph, including carry-forward
//
// The split that matters is 3 against 4 and 5. Code 3 means
// something disagreed with bytes the lock names, which deserves a
// human. Codes 4 and 5 mean the lock or the generation needs
// regenerating, which a pipeline can often handle itself.
//
// All four numbers are declared here even though 3 and 5 have no
// producers until the verified-unit commit model and the activation
// gate land. They are a published shell API, so reserving them in
// one place is the point of assigning them centrally; a comment
// reserves nothing. TestExitCodeValues pins the literals.
const (
	exitFailure         = 1
	exitLockIntegrity   = 3
	exitLockUnusable    = 4
	exitActivationDrift = 5
)

// exitCodeFor classifies a top-level error. Sentinels are matched
// with errors.Is so the class survives however much context callers
// wrap around it.
func exitCodeFor(err error) int {
	switch {
	case err == nil:
		return 0
	// Order matters here, and each of the three cases below is checked
	// ahead of something that would otherwise swallow it.
	case errors.Is(err, activation.ErrDrift):
		// Before the integrity class, because drift is the narrower
		// condition and the two must never collapse: a carried-forward
		// package reading as tampering trains users to ignore the
		// message that means tampering.
		return exitActivationDrift
	case errors.Is(err, farm.ErrClaimConflict):
		// Before the lock-unusable sentinels: the guard fails closed on
		// an unreadable claimant by wrapping that scope's lock error,
		// and regenerating the initiating scope's lock — the class-4
		// remedy — cannot fix another scope's file. A farm refusal is
		// always class 3, whatever it wraps.
		return exitLockIntegrity
	case errors.Is(err, provenance.ErrInvalid):
		// A store directory's record disagrees with the lock, or there
		// is no record at all where the lock names bytes. Both mean
		// something on disk is not what the lock says, which is the
		// class that deserves a human. ErrAbsent arrives wrapped in
		// ErrInvalid from VerifyAgainstLock, deliberately.
		return exitLockIntegrity
	case errors.Is(err, lockfile.ErrLegacySchema),
		errors.Is(err, lockfile.ErrUnknownVersion),
		errors.Is(err, lockfile.ErrUnknownField),
		errors.Is(err, lockfile.ErrDowngradeGuard),
		errors.Is(err, lockfile.ErrMalformed),
		// A lock whose contents cannot be modeled shares the class
		// with one whose schema cannot: in every case the file is
		// present and the remedy is to regenerate it (design §8).
		errors.Is(err, lockfile.ErrStaleLock),
		errors.Is(err, lockfile.ErrMalformedRoot),
		errors.Is(err, lockfile.ErrVersionConflict),
		errors.Is(err, lockfile.ErrMissingNode),
		errors.Is(err, lockfile.ErrMissingArtifact),
		// A locked closure that cannot be serialized is a lock that
		// cannot be modeled: an edge pointing at a node the lock omits,
		// or a cycle, for which no digest and no install order exist.
		errors.Is(err, lockgraph.ErrMissingDep),
		errors.Is(err, lockgraph.ErrCycle):
		return exitLockUnusable
	default:
		return exitFailure
	}
}
