package main

import (
	"errors"

	"github.com/kelp/gale/internal/lockfile"
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
		errors.Is(err, lockfile.ErrMissingArtifact):
		return exitLockUnusable
	default:
		return exitFailure
	}
}
