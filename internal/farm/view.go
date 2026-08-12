package farm

import (
	"fmt"
	"maps"
	"slices"
)

// This file holds the one representation of the store state an
// operation PROPOSES: the directories that will exist once it
// commits, and where each one's bytes can be read from right now.
//
// It exists because that hypothetical used to be encoded twice — a
// directory list beside a substitution map — and every consumer had
// to recombine the halves the same way. The rule they each
// re-implemented was "a canonical dir appears in both under the same
// spelling, and staged-ness is whether two path strings differ", and
// it was spelled at four sites. Three agreed. One did not, which is
// how gh#194 got its first defect.
//
// A ProposedStore makes that convention unrepresentable rather than
// merely documented: keys are canonicalized once at construction,
// each canonical directory has exactly one read path, and
// staged-ness is derived once and then read rather than re-tested.

// ProposedStore is the store as an operation proposes to leave it:
// one entry per canonical store directory, each knowing where its
// bytes can be read from now.
//
// Values are immutable once built. With returns a new view rather
// than mutating, so a claim handed to the guard cannot shift under
// it between the soname enumeration and the closure walk.
type ProposedStore struct {
	entries map[string]entry // key: canonicalized final dir
}

// entry is one proposed directory.
type entry struct {
	// read is the directory to enumerate now: a staging dir before
	// the commit, or the directory itself once the bytes are in
	// place. Kept as supplied, since that is what gets scanned.
	read string
	// final is the directory's spelling AS SUPPLIED, which is what
	// every reported target and every refusal message must carry.
	// The map key is canonicalized for identity; this is not, so
	// canonicalizing keys cannot silently rewrite an observation.
	final string
	// staged is whether read differs from the directory itself,
	// decided once at construction. Every consumer reads this rather
	// than comparing path strings again, which is the whole point of
	// the type.
	staged bool
}

// Committed returns the view of a set of directories whose bytes are
// already at their canonical paths, for callers with nothing staged.
func Committed(dirs ...string) *ProposedStore {
	v := &ProposedStore{entries: make(map[string]entry, len(dirs))}
	for _, d := range dirs {
		key := CanonicalDir(d)
		if _, ok := v.entries[key]; ok {
			// Closures legitimately overlap: the same dep directory
			// reached from several roots. First spelling wins, which
			// matches the first-seen dedup this replaces.
			continue
		}
		v.entries[key] = entry{read: d, final: d}
	}
	return v
}

// NewProposedStore builds the view an operation proposes: the
// committed directories it keeps, with placements layered over them.
//
// A placement OVERRIDES a committed directory of the same identity,
// and that is the substitution the type exists for: before the
// rename the canonical path still holds the artifact being replaced,
// so reading it would let the superseded bytes decide the operation
// that supersedes them.
//
// Two placements naming one canonical directory from different read
// paths is a construction error, not a last-wins merge. No single
// store state can satisfy both, and silently keeping one of them is
// how a claim shrinks without anybody noticing.
func NewProposedStore(
	placements []Placement, committed []string,
) (*ProposedStore, error) {
	return Committed(committed...).With(placements...)
}

// With returns a copy of the view with more placements layered over
// it, leaving the receiver untouched.
func (v *ProposedStore) With(more ...Placement) (*ProposedStore, error) {
	out := &ProposedStore{
		entries: make(map[string]entry, len(v.entries)+len(more)),
	}
	maps.Copy(out.entries, v.entries)
	placed := make(map[string]bool, len(more))
	for _, p := range more {
		key := CanonicalDir(p.FinalDir)
		// Staged-ness, decided here and nowhere else. A placement
		// whose bytes are already canonical — what At builds — is a
		// committed directory by definition, so the stricter reading
		// staged artifacts get cannot reach an ordinary install.
		e := entry{
			read:   p.ScanDir,
			final:  p.FinalDir,
			staged: CanonicalDir(p.ScanDir) != key,
		}
		if prev, ok := out.entries[key]; ok && placed[key] &&
			CanonicalDir(prev.read) != CanonicalDir(e.read) {
			return nil, fmt.Errorf(
				"%s is proposed from two different directories, %s and %s",
				identityOfDir(key), prev.read, e.read,
			)
		}
		placed[key] = true
		out.entries[key] = e
	}
	return out, nil
}

// ReadPath returns where dir's bytes can be read from now, whether
// they are staged, and whether the view knows dir at all. dir may be
// spelled any way that names the same directory.
func (v *ProposedStore) ReadPath(dir string) (path string, staged, known bool) {
	e, ok := v.entries[CanonicalDir(dir)]
	if !ok {
		return "", false, false
	}
	return e.read, e.staged, true
}

// Dirs returns every canonical directory in the view, sorted. This
// is the identity spelling: use it to compare directories, never to
// report one.
func (v *ProposedStore) Dirs() []string {
	return slices.Sorted(maps.Keys(v.entries))
}

// Placements returns the view as the placement list the farm
// predicate consumes, ordered by canonical directory.
//
// Each placement carries the spellings it was built from, not the
// canonical key: a reported target must name the path the caller
// gave, or a refusal message and a cross-claimant comparison would
// both change under a caller whose store root is reached through a
// symlink.
func (v *ProposedStore) Placements() []Placement {
	out := make([]Placement, 0, len(v.entries))
	for _, key := range v.Dirs() {
		e := v.entries[key]
		out = append(out, Placement{ScanDir: e.read, FinalDir: e.final})
	}
	return out
}

// Len reports how many directories the view holds. A view with none
// claims nothing.
func (v *ProposedStore) Len() int {
	return len(v.entries)
}

// Has reports whether the view names dir, under any spelling of it.
func (v *ProposedStore) Has(dir string) bool {
	_, ok := v.entries[CanonicalDir(dir)]
	return ok
}
