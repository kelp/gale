package generation

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/farm"
)

// AuthoritativeClosure returns the store directories reachable from
// roots, and whether the walk could see all of them.
//
// roots are exact store directories, from
// AuthoritativeGenerationDirs, never a name-and-version set the walk
// re-resolves: re-resolution picks the canonical sibling, so a scope
// loading a pre-revision bare directory would be reported as
// protecting a different one.
//
// It exists for decisions that destroy bytes (design §13's migration
// veto). Those need a scope's references even where its lock cannot
// state them — a legacy lock records roots only, so a transitive
// dependency carries no hash, and the only remaining record of the
// reference is the generation's links plus each directory's own
// dependency metadata.
//
// The completeness flag is the point, and it is why FarmStoreDirs
// cannot serve here. That walk reads through depsmeta.Read, which
// decodes a missing file to an empty Metadata, so a package with
// dependencies and no recorded metadata reads as a leaf and its
// dependencies look unreferenced — the walk reports success while
// having seen none of them. Here an absent, unreadable, non-regular
// or malformed file marks the closure unknown, and a caller deciding
// whether to delete something treats unknown as a refusal.
//
// Directories already known are still returned when the walk is
// incomplete: what is missing is everything BEYOND the unreadable
// node, not the node itself.
func AuthoritativeClosure(
	roots []string, storeRoot string,
) (map[string]bool, bool) {
	return walkClosure(roots, storeRoot, nil, "").authoritative()
}

// ReferenceClosure is AuthoritativeClosure for a caller about to
// REPLACE one of the directories it would otherwise judge.
//
// replacing is reported as reachable and then descended no further,
// and its own dependency metadata is neither read nor held against
// the closure's completeness. Both halves are needed and for
// different reasons. Its metadata belongs to the artifact being
// superseded, so letting it mark the closure unknown would let an
// unprovenanced legacy directory refuse its own replacement. And
// nothing below it can be a reverse dependent of it, so the
// directories the descent would add answer no question the caller
// asked.
//
// Every other directory keeps AuthoritativeClosure's strict reading,
// which is the point: a caller deciding whether a replacement
// invalidates somebody's record must not read a blocked descent as
// nobody's.
func ReferenceClosure(
	roots []string, storeRoot, replacing string,
) (map[string]bool, bool) {
	return walkClosure(
		roots, storeRoot, nil, canonicalDir(replacing),
	).authoritative()
}

// ProposedClosure is the walk for a closure that must be SATISFIED
// rather than merely enumerated: the one a scope will have once a
// set of staged artifacts commit.
//
// Two differences from AuthoritativeClosure, and both come from
// that. A referenced directory that is absent makes the closure
// INCOMPLETE here, where the migration veto treats absence as
// nothing to protect: a dependency that is gone is a dependency the
// proposed closure cannot satisfy, and dependency locks are
// released before the root commits, so one can be collected between
// the mint and this walk.
//
// And the proposed store view substitutes for canonical. It says,
// for each directory, where that directory's bytes can be read from
// right now, so the walk reads the artifact about to land rather
// than the one being replaced. Without it a refresh reads the
// metadata it is superseding: malformed metadata in an
// unprovenanced legacy artifact would refuse the very operation that
// replaces it, and a package still on disk under the old closure
// would be claimed as though the replacement kept it.
//
// A nil view substitutes nothing.
func ProposedClosure(
	roots []string, storeRoot string, view *farm.ProposedStore,
	requirePresent bool,
) (map[string]bool, bool) {
	return walkClosure(roots, storeRoot, view, "").proposed(requirePresent)
}

// closureNode is one directory the walk reached, recorded rather
// than judged. Every tolerance difference between the walk's
// interpretations is a difference in how these fields are read, so
// the observation is taken once and the policies are pure functions
// over it.
type closureNode struct {
	// Dir is the canonical store directory, the identity spelling.
	Dir string
	// ReadFrom is where its bytes were read: Dir itself, or a
	// staging directory standing in for it.
	ReadFrom string
	// Staged is whether ReadFrom is somewhere else. Decided by the
	// proposed-store view, never by comparing path strings here.
	Staged bool
	// Present is whether there are bytes to read at all. A staged
	// directory is present by definition — that is what staging it
	// means — even before its canonical path exists.
	Present bool
	// Leaf marks the one directory a caller is about to replace. Its
	// metadata is neither read nor judged, because it describes the
	// artifact being superseded.
	Leaf bool
	// Meta is what its dependency metadata said. Meaningful only for
	// a node that is Present and not Leaf; the walk does not read it
	// otherwise.
	Meta depsmeta.State
	// StatErr is a stat failure that is not simple absence. Absence
	// is an answer; anything else is a question the walk could not
	// ask.
	StatErr error
}

// closure is the walk's whole observation, sorted by Dir so every
// interpretation of it iterates deterministically.
type closure struct {
	nodes []closureNode
}

// dirs is the set of directories the closure reaches: the present
// ones. An absent directory is reported by no interpretation —
// what differs is whether its absence is tolerated.
func (c closure) dirs() map[string]bool {
	out := make(map[string]bool, len(c.nodes))
	for _, n := range c.nodes {
		if n.Present {
			out[n.Dir] = true
		}
	}
	return out
}

// authoritative is design §13's reading, for a decision that
// destroys bytes: any node whose dependency record is not readable
// leaves the closure unknown, and a caller deciding whether to
// delete something treats unknown as a refusal.
//
// An absent directory is not incomplete. There is nothing there to
// protect, which is a complete answer to the question this walk
// asks.
func (c closure) authoritative() (map[string]bool, bool) {
	complete := true
	for _, n := range c.nodes {
		switch {
		case !n.Present:
			// Absence is an answer; a stat that failed for any other
			// reason is not.
			if n.StatErr != nil {
				complete = false
			}
		case n.Leaf:
			// Deliberately unread: its contents describe the artifact
			// being superseded.
		case n.Meta != depsmeta.StateRecorded:
			complete = false
		}
	}
	return c.dirs(), complete
}

// proposed is the reading for a closure that must be SATISFIED
// rather than merely enumerated, with the two tolerances the farm
// claim has always had and the removal claim has never had.
//
// require is the caller's direction of travel. Populating retargets
// a soname and a dependency that is gone provides none, so a
// shrinking claim is honest there. Deleting a farm entry on the
// strength of a closure that could not be enumerated is the
// fail-open case, so absence and unreadable staged metadata both
// refuse once bytes go away.
func (c closure) proposed(require bool) (map[string]bool, bool) {
	complete := true
	for _, n := range c.nodes {
		switch {
		case !n.Present:
			// A dependency that is gone is one the proposed closure
			// cannot satisfy, but only where the answer decides a
			// deletion.
			if n.StatErr != nil || require {
				complete = false
			}
		case n.Leaf:
		case n.Meta == depsmeta.StateRecorded:
		case n.Staged && !require:
			// A STAGED artifact whose metadata does not decode is one
			// the installer commits anyway, with no provenance record
			// (design §7's all-or-nothing rule, recordProvenance's
			// deliberate nil return). A guard on the non-destructive
			// side must not be stricter than the commit it guards, or
			// it refuses installs that are policy-allowed today.
		case n.Meta == depsmeta.StateAbsent && !n.Staged:
			// A COMMITTED directory with no metadata is a leaf, which
			// is what the farm walk has always taken it for. Calling
			// it unknown would make the claim unusable for every
			// package installed before metadata existed, and an
			// unusable claim refuses every operation on the machine.
			//
			// A directory read from ELSEWHERE is different: gale
			// staged it moments ago, so a missing record means one was
			// not produced and the closure really is unknown.
		default:
			complete = false
		}
	}
	return c.dirs(), complete
}

// walkClosure observes the closure reachable from roots, reading
// each directory through view and stopping at leaf.
//
// It decides nothing. Descent is the one policy it must apply,
// because it cannot record a node it never reached, and descent is
// the same for every interpretation: a directory with a readable
// dependency record is descended into, and one without records no
// dependencies to descend into anyway.
//
// A nil view substitutes nothing, which is what the interpretations
// with no staged bytes want.
func walkClosure(
	roots []string, storeRoot string, view *farm.ProposedStore, leaf string,
) closure {
	// One spelling throughout, matching AuthoritativeGenerationDirs.
	// A caller comparing a directory it resolved itself against these
	// must not miss a match because of macOS /var versus /private/var.
	absStore := canonicalDir(storeRoot)
	queue := make([]string, 0, len(roots))
	for _, r := range roots {
		queue = append(queue, canonicalDir(r))
	}
	seen := make(map[string]bool, len(queue))
	for _, d := range queue {
		seen[d] = true
	}

	var out closure
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		n, deps := observe(dir, view, leaf)
		out.nodes = append(out.nodes, n)
		// The one policy the walk must apply, because it cannot
		// record a node it never reached. It is the same for every
		// interpretation: a directory whose record did not decode
		// declares no dependencies to descend into. Meta is the zero
		// state for a node observe did not read, so this covers the
		// absent and leaf cases too.
		if n.Meta != depsmeta.StateRecorded {
			continue
		}
		for _, dep := range deps {
			// By bare version, matching how the farm resolves: the
			// pin holds SONAME identity while the revision floats to
			// whatever is installed (gh#172).
			depDir := canonicalDir(
				resolveStoreDir(absStore, dep.Name, dep.Version),
			)
			if !seen[depDir] {
				seen[depDir] = true
				queue = append(queue, depDir)
			}
		}
	}
	slices.SortFunc(out.nodes, func(a, b closureNode) int {
		return strings.Compare(a.Dir, b.Dir)
	})
	return out
}

// observe records one directory without judging it, returning the
// dependencies it declares so the walk reads each record once.
func observe(
	dir string, view *farm.ProposedStore, leaf string,
) (closureNode, []depsmeta.ResolvedDep) {
	n := closureNode{Dir: dir, ReadFrom: dir, Leaf: leaf != "" && dir == leaf}
	if view != nil {
		if path, staged, known := view.ReadPath(dir); known {
			n.ReadFrom, n.Staged = path, staged
		}
	}
	if n.Staged {
		// The staged bytes stand in for the canonical dir entirely:
		// present even when the canonical path does not exist yet,
		// and read from where they are.
		n.Present = true
	} else if _, err := os.Stat(dir); err == nil {
		n.Present = true
	} else if !os.IsNotExist(err) {
		n.StatErr = err
	}
	if !n.Present || n.Leaf {
		return n, nil
	}
	deps, state := depsmeta.ReadStrict(n.ReadFrom)
	n.Meta = state
	return n, deps
}

// AuthoritativeGenerationDirs returns the store directories a scope's
// active generation links, exactly as linked.
//
// Two differences from CurrentVersions, both of which matter only to a
// caller deciding whether to destroy bytes.
//
// It returns the linked directory rather than a name-and-version pair
// that the caller re-resolves. Re-resolution picks the canonical
// sibling, so a generation linking a pre-revision bare dir while
// "1.7-1" also exists would be reported as protecting "1.7-1" while
// its binaries load "1.7" — and that shape is precisely upgrade day,
// which is when this scan runs.
//
// It fails rather than returning what it managed to read. genVersions
// is a deliberately best-effort walk: it swallows walk errors,
// unreadable links and store stat failures, which is right for
// rebuilding a generation and wrong here, because "I could not read
// this scope" would arrive as "this scope references nothing".
//
// An absent current symlink is still an empty scope, not a failure: a
// registered project that has never synced genuinely references
// nothing.
func AuthoritativeGenerationDirs(galeDir, storeRoot string) ([]string, error) {
	// The link target, not a path rebuilt from its number. Current
	// parses the basename as an integer, so a current pointing at
	// "alternate/1" yields 1 and reconstructing "gen/1" would inspect a
	// directory the scope is not using.
	curPath := filepath.Join(galeDir, "current")
	target, err := os.Readlink(curPath)
	if err != nil {
		if os.IsNotExist(err) {
			// A scope that has never synced references nothing.
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", curPath, err)
	}
	genDir := target
	if !filepath.IsAbs(genDir) {
		genDir = filepath.Join(galeDir, target)
	}
	if _, err := os.Stat(genDir); err != nil {
		return nil, fmt.Errorf(
			"the active generation %s is unreadable: %w", genDir, err,
		)
	}

	// One spelling for every path returned, so a caller comparing
	// against a store directory it resolved itself cannot miss a match
	// because of macOS /var versus /private/var.
	absStore := storeRoot
	if resolved, rerr := filepath.EvalSymlinks(storeRoot); rerr == nil {
		absStore = resolved
	}

	seen := map[string]bool{}
	var out []string
	walkErr := filepath.Walk(genDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return fmt.Errorf("walking %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return nil
		}
		linkTarget, readErr := os.Readlink(path)
		if readErr != nil {
			return fmt.Errorf("reading link %s: %w", path, readErr)
		}
		if !filepath.IsAbs(linkTarget) {
			linkTarget = filepath.Join(filepath.Dir(path), linkTarget)
		}
		// Resolve the link's own directory prefix so a target spelled
		// through /var matches a store root spelled through
		// /private/var, without resolving the leaf: the leaf may be
		// absent while its store directory still exists.
		linkDir := linkTarget
		if resolved, rerr := filepath.EvalSymlinks(filepath.Dir(linkTarget)); rerr == nil {
			linkDir = filepath.Join(resolved, filepath.Base(linkTarget))
		}
		rel := relWithinStore(absStore, linkDir)
		if rel == "" {
			rel = relWithinStore(absStore, linkTarget)
		}
		if rel == "" {
			return nil // outside the store; not ours to protect
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) < 2 {
			return nil
		}
		// Joined from the link text, never re-resolved by version.
		dir := filepath.Join(absStore, parts[0], parts[1])
		if !seen[dir] {
			seen[dir] = true
			out = append(out, dir)
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.Strings(out)
	return out, nil
}

// canonicalDir resolves a store directory's spelling without touching
// its version. It delegates to farm.CanonicalDir: the guard compares
// this package's keys against the farm's, so the two must be the same
// rule and not merely the same rule today.
func canonicalDir(dir string) string {
	return farm.CanonicalDir(dir)
}
