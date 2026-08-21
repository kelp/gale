package main

import (
	"errors"
	"runtime"

	"github.com/kelp/gale/internal/lockfile"
)

// currentPlatform is the artifact dimension of the machine in hand.
// The lockfile spells a platform GOOS/GOARCH, and every read-only
// consumer wants the local one.
func currentPlatform() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}

// lockIsStale reports whether the lockfile at lockPath still describes
// the declared packages, understanding either schema.
//
// declared is the effective gale.toml package set for host, already
// host-merged by ApplyHost. host selects the lock's overlay targets.
//
// Staleness and unusability are kept apart. A lock whose roots no
// longer match the manifest is stale, and syncing resolves it. A lock
// that cannot be modeled at all — a malformed root, an unknown schema,
// a missing downgrade guard — is an error, because reporting it as
// merely stale would send sync to fix it by writing, hiding a
// lock-unusable condition behind a routine one.
//
// This is deliberately cheap: it reads the lockfile and the manifest
// and nothing else. It runs on every direnv `cd`, so it resolves no
// recipes, hashes nothing, and never validates artifact contents.
func lockIsStale(lockPath string, declared map[string]string, host string) (bool, error) {
	v, err := lockfile.Load(lockPath)
	if err != nil {
		return false, err
	}
	switch v.Kind {
	case lockfile.KindAbsent:
		// No lock is stale by definition, which is what makes the first
		// sync in a fresh project run.
		return true, nil
	case lockfile.KindLegacy:
		return v.Legacy.Stale(declared), nil
	case lockfile.KindV1:
		return v1Stale(v.V1, declared, host)
	case lockfile.KindV2:
		if err := checkV2Declared(v.V2, declared); err != nil {
			if errors.Is(err, lockfile.ErrStaleLock) {
				return true, nil
			}
			return false, err
		}
		return false, nil
	default:
		return false, errors.New("unhandled lockfile kind " + v.Kind.String())
	}
}

// v1Stale compares the enforced schema's declared roots against the
// manifest. Package entries are not counted: an enforced lock records
// the whole closure, so a count comparison would report stale forever
// as soon as one root has a dependency.
func v1Stale(lf *lockfile.V1, declared map[string]string, host string) (bool, error) {
	roots, origin, err := lf.EffectiveRootsWithOrigin(host)
	if err != nil {
		return false, err
	}
	// Roots only: this function answers a boolean and discards the
	// message, so the manifest half would sharpen a remedy nobody
	// reads. runSync's plan construction is where the user sees it.
	if err := lockfile.CheckDeclared(roots, declared, lockfile.Origins{
		Roots: origin,
	}); err != nil {
		if errors.Is(err, lockfile.ErrStaleLock) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}
