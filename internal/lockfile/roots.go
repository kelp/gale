package lockfile

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/store"
)

var (
	// ErrMalformedRoot reports an identity that is not spelled
	// name@version-revision. It shares the lock-unusable class with
	// the schema errors: the file cannot be modeled, so it must be
	// regenerated rather than worked around.
	ErrMalformedRoot = errors.New("malformed lock root")

	// ErrVersionConflict reports one package required at two
	// versions. The store holds both happily, but a generation links
	// exactly one, so accepting both would defer the choice to
	// symlink time and resolve it silently.
	ErrVersionConflict = errors.New("package required at conflicting versions")

	// ErrStaleLock reports a lock whose roots no longer match the
	// manifest. It is separate from the missing-node errors because
	// the remedy differs: this one is fixed by rewriting the lock,
	// those by fixing the lock's contents.
	ErrStaleLock = errors.New("lock does not match gale.toml")

	// ErrMissingNode reports an edge or root the lock does not define.
	// Under a lock this is fatal rather than an invitation to resolve
	// the package live: resolving it is exactly the behavior the lock
	// exists to prevent.
	ErrMissingNode = errors.New("package missing from lock")

	// ErrMissingArtifact reports a locked node that records nothing
	// for the platform being asked about.
	ErrMissingArtifact = errors.New("platform missing from lock")
)

// ParseIdentity splits a canonical name@version-revision identity.
//
// The split is this package's because the lockfile is where an
// identity is spelled as one string. What makes each half canonical
// belongs to store, because that rule is about addressing exactly one
// store directory, and it bites here for the same reason: a local
// --recipes directory joins the name onto a path, so a name that is
// itself a path escapes it.
func ParseIdentity(id string) (string, string, error) {
	name, version, ok := strings.Cut(id, "@")
	if !ok || strings.Contains(version, "@") {
		return "", "", fmt.Errorf(
			"%w: %q is not name@version-revision", ErrMalformedRoot, id,
		)
	}
	if err := store.CheckIdentity(name, version); err != nil {
		return "", "", fmt.Errorf("%w: %w", ErrMalformedRoot, err)
	}
	return name, version, nil
}

// EffectiveRoots merges the default target with every host selector
// matching host, in config's precedence order, keyed by package name
// so a later selector replaces an earlier selector's pin for the same
// package rather than adding a second one.
//
// Host keys are gale.toml's selector strings verbatim, so reading
// this file at all requires the manifest's precedence rule; that
// coupling is in the schema, not in this function.
func (lf *V1) EffectiveRoots(host string) (map[string]string, error) {
	roots := make(map[string]string)
	// Replacement across successively more specific selectors is the
	// point of the overlay. Two identities for one package inside a
	// single roots list is not replacement, it is a malformed lock:
	// taking the last would pick a winner by list order, and neither
	// selector precedence nor any later conflict check would ever see
	// the loser.
	apply := func(list []string) error {
		within := make(map[string]string, len(list))
		for _, id := range list {
			name, _, err := ParseIdentity(id)
			if err != nil {
				return err
			}
			if other, dup := within[name]; dup && other != id {
				return fmt.Errorf(
					"%w: one target roots both %s and %s",
					ErrVersionConflict, other, id,
				)
			}
			within[name] = id
			roots[name] = id
		}
		return nil
	}
	if lf.Targets.Default != nil {
		if err := apply(lf.Targets.Default.Roots); err != nil {
			return nil, err
		}
	}
	if host == "" {
		return roots, nil
	}
	keys := config.MatchingHostKeys(slices.Collect(maps.Keys(lf.Targets.Host)), host)
	for _, k := range keys {
		if err := apply(lf.Targets.Host[k].Roots); err != nil {
			return nil, err
		}
	}
	return roots, nil
}

// CheckDeclared compares a locked root set against the effective
// gale.toml package set: the same names, each pinned to the same
// version.
//
// Versions are compared through VersionMatches rather than for
// equality, because the two files spell one pin differently by
// design: gale.toml records the bare version so an entry tracks
// revision bumps automatically, and the lock records the canonical
// version-revision it resolved to (see WriteConfigAndLock).
// Comparing them at all is what makes an edited pin visible: the
// manifest holds an exact version, not a constraint, so a changed
// pin means the lock no longer describes what was asked for.
func CheckDeclared(roots, declared map[string]string) error {
	var unlocked, orphaned, repinned []string
	for name, want := range declared {
		id, ok := roots[name]
		if !ok {
			unlocked = append(unlocked, name)
			continue
		}
		// Already validated by EffectiveRoots, which is the only
		// producer of a roots map.
		_, locked, err := ParseIdentity(id)
		if err != nil {
			return err
		}
		if !VersionMatches(locked, want) {
			repinned = append(repinned, fmt.Sprintf(
				"%s is declared %s but locked at %s", name, want, locked,
			))
		}
	}
	for name := range roots {
		if _, ok := declared[name]; !ok {
			orphaned = append(orphaned, name)
		}
	}
	parts := staleParts(unlocked, orphaned, repinned)
	if len(parts) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrStaleLock, strings.Join(parts, "; "))
}

// staleParts renders the three ways a lock disagrees with the
// manifest, sorted so the message does not vary with map order.
func staleParts(unlocked, orphaned, repinned []string) []string {
	sort.Strings(unlocked)
	sort.Strings(orphaned)
	sort.Strings(repinned)
	var parts []string
	if len(unlocked) > 0 {
		parts = append(parts, fmt.Sprintf(
			"gale.toml declares %s with no locked root",
			strings.Join(unlocked, ", "),
		))
	}
	if len(orphaned) > 0 {
		parts = append(parts, fmt.Sprintf(
			"the lock roots %s which gale.toml no longer declares",
			strings.Join(orphaned, ", "),
		))
	}
	if len(repinned) > 0 {
		parts = append(parts, strings.Join(repinned, ", "))
	}
	return parts
}
