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

	// ErrDigestMismatch reports a stored graph digest that does not
	// survive recomputation from the locked closure. Recomputing is
	// the point: a digest read and believed could certify itself.
	//
	// It lives here rather than in one of the packages that make the
	// comparison because plan construction and the activation gate
	// both make it, of the same file, and a lock asserting a digest
	// its own contents do not produce must classify identically
	// whichever one notices. Unlike its neighbours above this is an
	// integrity violation, not a lock to regenerate: design §8's
	// table names a graph_digest mismatch in the same row as an
	// artifact SHA mismatch.
	ErrDigestMismatch = errors.New("graph digest disagrees with the locked closure")
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
	roots, _, err := lf.EffectiveRootsWithOrigin(host)
	return roots, err
}

// EffectiveRootsWithOrigin is EffectiveRoots plus, per package, the
// key of the target that rooted it: "" for [targets.default] and the
// selector for a host overlay.
//
// The origin exists so a stale-lock message can name one command
// instead of a form the user has to complete. `gale lock` regenerates
// [targets.default] and carries every host target forward untouched,
// so a root that lives in an overlay is repaired only by `gale lock
// --host <that selector>`. Merging discards exactly the fact that
// decides which of the two to print.
func (lf *V1) EffectiveRootsWithOrigin(
	host string,
) (map[string]string, map[string]string, error) {
	roots := make(map[string]string)
	origin := make(map[string]string)
	// Replacement across successively more specific selectors is the
	// point of the overlay. Two identities for one package inside a
	// single roots list is not replacement, it is a malformed lock:
	// taking the last would pick a winner by list order, and neither
	// selector precedence nor any later conflict check would ever see
	// the loser.
	apply := func(target string, list []string) error {
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
			// Later, more specific selectors overwrite earlier ones,
			// which is the overlay's purpose; the origin has to follow
			// the identity that survived, not the first one seen.
			origin[name] = target
		}
		return nil
	}
	if lf.Targets.Default != nil {
		if err := apply("", lf.Targets.Default.Roots); err != nil {
			return nil, nil, err
		}
	}
	if host == "" {
		return roots, origin, nil
	}
	keys := config.MatchingHostKeys(slices.Collect(maps.Keys(lf.Targets.Host)), host)
	for _, k := range keys {
		if err := apply(k, lf.Targets.Host[k].Roots); err != nil {
			return nil, nil, err
		}
	}
	return roots, origin, nil
}

// CheckDeclared compares a locked root set against the effective
// gale.toml package set: the same names, each pinned to the same
// version.
//
// Versions are compared through VersionMatches rather than for
// equality, because the two files spell one pin differently by
// design: gale.toml records the bare version so an entry tracks
// revision bumps automatically, and the lock records the canonical
// version-revision it resolved to (see cmdContext.WriteConfig).
// Comparing them at all is what makes an edited pin visible: the
// manifest holds an exact version, not a constraint, so a changed
// pin means the lock no longer describes what was asked for.
// Origins carries where each side's entry comes from, so the remedy
// names one exact command rather than a form the caller completes.
//
// The two halves are not interchangeable, and using one for both is a
// bug with a quiet failure mode. An orphaned root exists only in the
// lock, so its owning lock target decides the command. A repin exists
// on both sides and the MANIFEST decides: `gale lock --host K`
// regenerates a target from [hosts.K.packages], so when an overlay
// re-pins a package the lock roots only in its default target, the
// lock's origin points at the default and the command it suggests
// rewrites that target with the same pin, creating nothing and fixing
// nothing.
//
// A zero Origins keeps the general wording, which is what a caller
// with neither map to hand can honestly say.
type Origins struct {
	// Roots maps a locked root to its target key: "" for
	// [targets.default], the selector for a host target.
	Roots map[string]string
	// Declared maps a manifest package to the section supplying its
	// effective pin: "" for [packages], the selector for an overlay.
	Declared map[string]string
}

// originRef is one disagreement's owning section, plus whether the
// caller actually knew it. The flag is not redundant with an empty
// key: "" is a real answer meaning the default target, and a missing
// map lookup produces the same "" while meaning nothing at all.
// Collapsing the two prints a command that rewrites the wrong section
// and reports success.
type originRef struct {
	key   string
	known bool
}

func (o Origins) declaredRef(name string) originRef {
	key, ok := o.Declared[name]
	return originRef{key: key, known: o.Declared != nil && ok}
}

func (o Origins) rootRef(name string) originRef {
	key, ok := o.Roots[name]
	return originRef{key: key, known: o.Roots != nil && ok}
}

func CheckDeclared(roots, declared map[string]string, origin Origins) error {
	var unlocked, orphaned, repinned []string
	// Two groups, because they get two different sentences: a root the
	// lock has never seen is installed or locked, while one it has is
	// relocked.
	var newRefs, staleRefs []originRef
	for name, want := range declared {
		id, ok := roots[name]
		if !ok {
			unlocked = append(unlocked, name)
			// The manifest's side. `gale install` is target-independent,
			// but the `gale lock` alternative beside it is not: a new
			// root already in the store, declared under an overlay, is
			// not locked by rewriting the default target.
			newRefs = append(newRefs, origin.declaredRef(name))
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
			// The manifest's side: the section supplying the pin the
			// lock disagrees with is the section that has to be locked.
			staleRefs = append(staleRefs, origin.declaredRef(name))
		}
	}
	for name := range roots {
		if _, ok := declared[name]; !ok {
			orphaned = append(orphaned, name)
			// The lock's side: an orphan exists nowhere else.
			staleRefs = append(staleRefs, origin.rootRef(name))
		}
	}
	parts := staleParts(unlocked, orphaned, repinned)
	if len(parts) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s; run %s", ErrStaleLock,
		strings.Join(parts, "; "), staleRemedy(newRefs, staleRefs))
}

// staleRemedy names the commands that end this state. Design §9 makes
// naming them mandatory: a stale lock fails every sync, including the
// one direnv runs on cd, where the user sees a single line and has no
// way to ask a follow-up question.
//
// Every required action is rendered, not the first that applies. A
// manifest can be stale in several directions at once, and naming one
// remedy leaves the user fixing it, rerunning, and meeting the next.
func staleRemedy(newRefs, staleRefs []originRef) string {
	var actions []string
	if len(newRefs) > 0 {
		// Both commands, per design §11: `gale install` handles the
		// package that is not installed yet, `gale lock` one that is
		// already in the store. The user knows which case they are in
		// and gale does not.
		actions = append(actions,
			"'gale install' to install and lock the new package(s), or "+
				lockPhrase(newRefs)+" to lock what is already installed")
	}
	if len(staleRefs) > 0 {
		actions = append(actions,
			lockPhrase(staleRefs)+" to regenerate the affected target(s)")
	}
	if len(actions) == 0 {
		return "'gale lock' to regenerate it"
	}
	return strings.Join(actions, ", and ")
}

// lockPhrase renders the `gale lock` invocations for one group. One
// unknown origin makes the whole group general: naming some targets
// and silently omitting others would read as a complete list.
func lockPhrase(refs []originRef) string {
	keys := make([]string, 0, len(refs))
	for _, r := range refs {
		if !r.known {
			return "'gale lock' (or 'gale lock --host <selector>' when " +
				"the package belongs to a host section)"
		}
		keys = append(keys, r.key)
	}
	return lockCommands(uniqueTargets(keys))
}

// lockCommands renders one `gale lock` invocation per owning target.
// All of them, since running one leaves the sync failing on the
// targets it did not rewrite.
func lockCommands(targets []string) string {
	cmds := make([]string, 0, len(targets))
	for _, t := range targets {
		if t == "" {
			cmds = append(cmds, "'gale lock'")
			continue
		}
		cmds = append(cmds, fmt.Sprintf("'gale lock --host %s'", quoteSelector(t)))
	}
	return strings.Join(cmds, " and ")
}

// quoteSelector wraps a host key so the printed command survives a
// shell. Double quotes, not single: the message already wraps whole
// commands in single quotes, and nesting one inside the other renders
// 'gale lock --host 'work-*” — which is not a command anyone can
// paste. A selector holds no shell expansions of its own, so double
// quotes suppress the glob just as well.
func quoteSelector(sel string) string {
	return `"` + sel + `"`
}

// uniqueTargets sorts and dedupes so the message does not vary with
// map iteration order.
func uniqueTargets(targets []string) []string {
	seen := make(map[string]bool, len(targets))
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

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
