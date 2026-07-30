package farm

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// This file is the cross-project farm claimant guard (design §4).
// The farm is one shared tree, so a fully successful operation in
// one scope can retarget or delete the soname link another scope's
// binaries resolve through — with no store mutation and no
// generation swap in the harmed scope, so nothing else would ever
// notice. The guard therefore runs before every farm mutation
// (create, remove, and retarget all count: Depopulate deletes a
// claimed soname and Rebuild deletes before recreating), and what
// makes it apply is the closure being harmed, never the privilege
// of the operation asking.
//
// The canonical rule, implemented once, here:
//
//  1. The claimant set is the initiating scope's PROPOSED closure
//     plus every other scope's current closure. The initiating
//     scope stays a claimant; only its old pre-operation closure is
//     superseded — which is why callers must never pass the
//     initiating scope's old claim, or the guard becomes a verb
//     veto that deadlocks the scope against its own update, remove,
//     repair, and refresh.
//  2. Compute the farm mapping the operation would leave behind.
//  3. Allow it exactly when that mapping satisfies every claim.
//
// It is a check on the resulting state, not on the verb: an
// external claimant that agrees with the proposed mapping never
// blocks anything, restoring a missing link is allowed, and only a
// conflicting claim refuses the operation.

// ErrClaimConflict classifies every guard refusal. It is a lock
// integrity class (exit code 3): something disagreed with the
// closure a scope is entitled to, which deserves a human rather
// than a retry.
var ErrClaimConflict = errors.New("cross-project farm conflict")

// Claimant is one scope whose closure claims sonames in the shared
// farm: the initiating scope's proposed closure, or another scope's
// current closure.
type Claimant struct {
	// Label names the scope in refusal messages ("global",
	// "project /path/to/a").
	Label string
	// StoreDirs is the resolved store dir of every package in the
	// scope's closure. Dirs absent from the store contribute no
	// claims: the farm can only map bytes that exist, and a scope
	// cannot be loading a version that has none.
	StoreDirs []string
	// Err marks a scope that is known to exist but whose closure
	// could not be read. The guard fails closed on it: an unreadable
	// claim could be hiding exactly the conflict the guard exists to
	// refuse.
	Err error
}

// Placement is one package about to enter the farm: where its libs
// can be read right now, and the canonical store dir the farm links
// will point into once it is committed.
//
// The two differ whenever the guard runs before the commit, which
// is where it has to run: a guard that scans the canonical dir can
// only see it after the install has already replaced it, and by
// then a refusal cannot undo the bytes an existing generation
// reaches. Scanning staging and reporting the canonical destination
// is what lets the check precede the mutation it is checking.
type Placement struct {
	// ScanDir holds the lib directory to enumerate now: a staging
	// dir before the commit, or the canonical dir when the bytes are
	// already in place.
	ScanDir string
	// FinalDir is the canonical store dir the farm's links will
	// carry. Every reported target and every conflict message names
	// this path, never ScanDir.
	FinalDir string
}

// At returns placements for dirs whose bytes are already at their
// canonical paths, for callers with nothing staged.
func At(storeDirs ...string) []Placement {
	out := make([]Placement, 0, len(storeDirs))
	for _, d := range storeDirs {
		out = append(out, Placement{ScanDir: d, FinalDir: d})
	}
	return out
}

// GuardPopulate checks that populating placements would leave every
// claim satisfied. The resulting mapping for each touched soname is
// the placement's final target; sonames the populate does not touch
// keep their current entry, so only touched sonames can newly
// violate a claim and only they are checked — an unrelated
// pre-existing violation between two other scopes must not freeze
// this one.
//
// placements is the batch being populated. Callers populating a
// whole plan pass all of it at once, so a plan that conflicts with
// itself is refused here with no external claimant present.
func GuardPopulate(placements []Placement, claimants []Claimant) error {
	proposed, err := placedSonameTargets("the proposed closure", placements)
	if err != nil {
		return err
	}
	return eachClaim(claimants, func(label, soname, target string) error {
		want, touched := proposed[soname]
		if !touched || want == target {
			return nil
		}
		return fmt.Errorf(
			"%w: %s requires %s from %s, operation would repoint it to %s",
			ErrClaimConflict, label, soname,
			identityOf(target), identityOf(want),
		)
	})
}

// GuardDepopulate checks that removing storeDir's farm links leaves
// every claim satisfied. A farm entry is removed when its target
// lies under storeDir (Depopulate's own predicate), and a removed
// soname satisfies no claim at all — deletion is as much a
// violation as retargeting, which is why remove is a guarded verb.
// Entries pointing elsewhere survive the depopulate, so claims on
// them cannot be newly violated and are not checked.
func GuardDepopulate(storeDir, farmDir string, claimants []Claimant) error {
	removed, err := depopulatedSonames(storeDir, farmDir)
	if err != nil {
		return err
	}
	return eachClaim(claimants, func(label, soname, target string) error {
		if !removed[soname] {
			return nil
		}
		return fmt.Errorf(
			"%w: %s requires %s from %s, removal would delete its farm entry",
			ErrClaimConflict, label, soname, identityOf(target),
		)
	})
}

// GuardRebuild checks a farm rebuild against every claim and
// returns the store dir set the rebuild must use: the proposed dirs
// plus every claimant's dirs. Rebuild wipes the farm before
// repopulating, so rebuilding from the proposed dirs alone would
// delete every soname only another scope claims — the union is what
// makes the resulting mapping satisfy claims the initiating scope
// does not share, and it is also what lets a rebuild repair a
// missing link with exactly the target every claimant wants.
func GuardRebuild(proposedDirs []string, claimants []Claimant) ([]string, error) {
	merged, err := sonameTargets("the proposed closure", proposedDirs)
	if err != nil {
		return nil, err
	}
	union := dedupDirs(proposedDirs)
	seen := make(map[string]bool, len(union))
	for _, d := range union {
		seen[filepath.Clean(d)] = true
	}
	err = eachClaim(claimants, func(label, soname, target string) error {
		if have, ok := merged[soname]; ok && have != target {
			return fmt.Errorf(
				"%w: %s requires %s from %s, rebuild would leave %s",
				ErrClaimConflict, label, soname,
				identityOf(target), identityOf(have),
			)
		}
		merged[soname] = target
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, c := range claimants {
		for _, d := range c.StoreDirs {
			if clean := filepath.Clean(d); !seen[clean] {
				seen[clean] = true
				union = append(union, d)
			}
		}
	}
	return union, nil
}

// eachClaim visits every (soname, target) claim of every claimant
// in a deterministic order, failing closed on a claimant whose
// closure could not be read or enumerated. Claim enumeration errors
// wrap the claimant's own error with %w so a lock-unusable cause
// keeps its exit-code class.
func eachClaim(
	claimants []Claimant,
	visit func(label, soname, target string) error,
) error {
	for _, c := range claimants {
		if c.Err != nil {
			return fmt.Errorf(
				"%w: cannot read the closure of %s: %w",
				ErrClaimConflict, c.Label, c.Err,
			)
		}
		claims, err := sonameTargets(c.Label, c.StoreDirs)
		if err != nil {
			return err
		}
		for _, soname := range slices.Sorted(maps.Keys(claims)) {
			if err := visit(c.Label, soname, claims[soname]); err != nil {
				return err
			}
		}
	}
	return nil
}

// sonameTargets maps every soname a set of store dirs provides to
// the lib path a farm link for it would carry. Claims are always
// read from bytes already at their canonical paths, so a claimant
// never needs a placement.
func sonameTargets(owner string, storeDirs []string) (map[string]string, error) {
	return placedSonameTargets(owner, At(storeDirs...))
}

// placedSonameTargets is sonameTargets over placements: sonames are
// enumerated from ScanDir and reported at FinalDir, using the same
// farming predicate as Populate. One closure providing one soname
// from two different store dirs is a conflict with itself: no farm
// mapping can satisfy it, so it is refused here with no external
// claimant needed.
func placedSonameTargets(
	owner string, placements []Placement,
) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range dedupPlacements(placements) {
		sonames, err := libSonames(p.ScanDir)
		if err != nil {
			return nil, fmt.Errorf("enumerating sonames of %s: %w", owner, err)
		}
		for _, soname := range slices.Sorted(maps.Keys(sonames)) {
			// Rebuilt from FinalDir rather than taken from the scan:
			// a staging path would compare unequal to every claimant's
			// target and name the wrong identity in the message.
			target := filepath.Join(p.FinalDir, "lib", soname)
			if have, ok := out[soname]; ok && have != target {
				return nil, fmt.Errorf(
					"%w: %s claims %s from both %s and %s",
					ErrClaimConflict, owner, soname,
					identityOf(have), identityOf(target),
				)
			}
			out[soname] = target
		}
	}
	return out, nil
}

// libSonames returns soname -> lib path for one store dir. Same
// farming predicate as Populate and CheckDrift: versioned basenames
// only, real files and symlink aliases that resolve to a regular
// file, dangling aliases dropped. A dir with no lib/ — including a
// dir not installed at all — provides nothing.
func libSonames(storeDir string) (map[string]string, error) {
	libDir := filepath.Join(storeDir, "lib")
	out := map[string]string{}
	entries, err := os.ReadDir(libDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return nil, fmt.Errorf("read lib dir: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !IsVersionedDylib(name) {
			continue
		}
		path := filepath.Join(libDir, name)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		out[name] = path
	}
	return out, nil
}

// depopulatedSonames returns the set of farm entries Depopulate
// would delete for storeDir: entries whose symlink target lies
// under it. Reads the same state Depopulate does, before Depopulate
// mutates it.
func depopulatedSonames(storeDir, farmDir string) (map[string]bool, error) {
	out := map[string]bool{}
	entries, err := os.ReadDir(farmDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return nil, fmt.Errorf("read farm dir: %w", err)
	}
	prefix := filepath.Clean(storeDir) + string(filepath.Separator)
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(farmDir, entry.Name()))
		if err != nil {
			continue // not a symlink — Depopulate won't touch it
		}
		if strings.HasPrefix(filepath.Clean(target), prefix) {
			out[entry.Name()] = true
		}
	}
	return out, nil
}

// dedupPlacements drops duplicates (path-clean comparison on both
// paths), preserving first-seen order, for the same reason
// dedupDirs exists: closures legitimately overlap and a repeated
// dir must not read as a self-conflict.
func dedupPlacements(placements []Placement) []Placement {
	seen := make(map[Placement]bool, len(placements))
	out := make([]Placement, 0, len(placements))
	for _, p := range placements {
		clean := Placement{
			ScanDir:  filepath.Clean(p.ScanDir),
			FinalDir: filepath.Clean(p.FinalDir),
		}
		if seen[clean] {
			continue
		}
		seen[clean] = true
		out = append(out, p)
	}
	return out
}

// dedupDirs drops duplicate store dirs (path-clean comparison),
// preserving first-seen order. Claimant closures legitimately
// overlap — the same dep dir reached from several scopes — and a
// duplicate must not read as a self-conflict.
func dedupDirs(dirs []string) []string {
	seen := make(map[string]bool, len(dirs))
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		clean := filepath.Clean(d)
		if seen[clean] {
			continue
		}
		seen[clean] = true
		out = append(out, d)
	}
	return out
}

// identityOf renders the store identity ("name@version-revision")
// that owns a farm target path, for refusal messages that must name
// both versions. Falls back to the raw path when the target does
// not have the store shape.
func identityOf(target string) string {
	storeDir := filepath.Dir(filepath.Dir(target))
	name := packageName(storeDir)
	if name == "" {
		return target
	}
	return name + "@" + filepath.Base(storeDir)
}
