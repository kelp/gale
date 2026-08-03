package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"sort"

	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/projects"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
)

// errMigrateUnreadable reports store state migrate cannot classify,
// found before anything was replaced.
//
// Design §13's second qualifying property: fail BEFORE replacing on
// unreadable state. Migrating past a directory gale cannot read would
// replace other directories while a known-corrupt one sits in the
// same store, unexplained, and the whole point of the machine-wide
// unit is that one pass leaves the machine in one describable state.
var errMigrateUnreadable = errors.New(
	"a store directory could not be classified",
)

// migrateTarget is one unprovenanced store directory and the
// identity a replacement would refetch.
//
// version is the recipe's canonical version-revision, never the
// directory basename: the basename is whatever a pre-revision
// install happened to write, while the canonical form is what a
// refetch produces and what the record would name.
type migrateTarget struct {
	name    string
	version string
	dir     string
	recipe  *recipe.Recipe
}

// migrateScan classifies every directory in the store, reading the
// STORE rather than any scope's closure.
//
// That is design §13 revision 7's fifth qualifying property, and it
// is what separates migrate from `gale lock --refresh`. A
// closure-based scan covers only what some generation still links, so
// a directory left behind by a removed root, or one whose dependency
// metadata predates the format, would never be migrated — and would
// go on vetoing every per-scope replacement forever, with no command
// able to reach it. The machine-wide unit exists to close exactly
// that loop.
//
// Three outcomes per directory. A provenanced one is not migrate's
// business at all: §13 forbids writing a record beside a directory
// migrate did not replace, and replacing one that already attests
// itself destroys bytes for nothing. An unprovenanced one whose
// recipe declares a prebuilt binary for this platform is a candidate,
// because refetching and stream-verifying is the only honest way to
// make its bytes attestable. An unprovenanced one without a binary
// cannot be migrated by refetching at all, so it is listed and left
// alone.
//
// Nothing here mutates. The whole store is classified before the
// first replacement, which is what lets a refusal cost nothing.
func migrateScan(
	storeRoot string,
	resolve func(name, version string) (*recipe.Recipe, error),
) (migrateScanResult, error) {
	var out migrateScanResult
	pkgs, err := store.NewStore(storeRoot).List()
	if err != nil {
		return out, fmt.Errorf("reading the store: %w", err)
	}
	// Sorted, so two runs over one store report the same directory
	// first and a refusal names the same one every time.
	sort.Slice(pkgs, func(i, j int) bool {
		if pkgs[i].Name != pkgs[j].Name {
			return pkgs[i].Name < pkgs[j].Name
		}
		return pkgs[i].Version < pkgs[j].Version
	})
	for _, p := range pkgs {
		if err := classifyForMigrate(storeRoot, p, resolve, &out); err != nil {
			return migrateScanResult{}, err
		}
	}
	return out, nil
}

// migrateScanResult is the whole machine's state, classified.
type migrateScanResult struct {
	// candidates are unprovenanced directories a refetch can replace.
	candidates []migrateTarget
	// sourceOnly are unprovenanced directories no refetch can reach.
	// §13 says migrate prints them with what rebuilding costs rather
	// than stamping them, and never claims to have converged them.
	sourceOnly []migrateTarget
}

// classifyForMigrate places one installed directory into the scan.
func classifyForMigrate(
	storeRoot string, p store.InstalledPackage,
	resolve func(name, version string) (*recipe.Recipe, error),
	out *migrateScanResult,
) error {
	dir := filepath.Join(storeRoot, p.Name, p.Version)
	_, err := provenance.ReadUnverified(dir)
	switch {
	case err == nil:
		// Already attested. Not migrate's business.
		return nil
	case errors.Is(err, provenance.ErrAbsent):
	default:
		// ErrInvalid, a permission problem, an I/O error: all mean
		// gale cannot say what this directory is, and none of them is
		// something a bulk replacement should walk past.
		return fmt.Errorf(
			"%w: %s: %w", errMigrateUnreadable, dir, err,
		)
	}
	r, rerr := resolve(p.Name, p.Version)
	if rerr != nil {
		// A directory whose recipe cannot be resolved cannot be
		// classified either, and guessing would put it in the silent
		// half of the report.
		return fmt.Errorf(
			"%w: %s: resolving a recipe: %w", errMigrateUnreadable, dir, rerr,
		)
	}
	t := migrateTarget{
		name: p.Name, version: r.Package.Full(), dir: dir, recipe: r,
	}
	if r.BinaryForPlatform(runtime.GOOS, runtime.GOARCH) == nil {
		out.sourceOnly = append(out.sourceOnly, t)
		return nil
	}
	out.candidates = append(out.candidates, t)
	return nil
}

// orderCandidates puts a candidate that another candidate depends on
// ahead of the candidate above it.
//
// Design §5 and §7 make this load-bearing rather than cosmetic, for
// the same reason `gale lock` orders its roots. Provenance is
// all-or-nothing, so refetching a dependent while its dependency is
// still unprovenanced commits an artifact with no record at all:
// bytes destroyed, nothing repaired, and the candidate check then
// refuses the replacement. Alphabetical order would decide whether
// the machine can converge.
//
// `orderRoots` cannot be reused, close as the shape is. It keys both
// its node map and its traversal state on the package name, which
// holds for a manifest section — one pin per name — and fails here:
// the scan reads the whole store, so two versions of one package are
// two candidates and one would silently displace the other.
//
// Nodes are therefore the candidates themselves, addressed by index,
// while EDGES are still drawn by name. That over-orders on purpose.
// Which version of a dependency a refetch will actually link is the
// resolver's business and is not knowable from the store, so every
// candidate sharing the name is ordered first. An extra edge fixes an
// order that did not need fixing; a missing one destroys bytes.
//
// Runtime dependencies only. Migrate replaces binary-method
// directories, whose records serialize runtime edges alone, so a
// build tool cannot leave its dependent unattestable the way it does
// for a source artifact — and an unrelated build cycle would
// otherwise abandon the runtime ordering that does matter.
//
// A cycle returns the input order unchanged, exactly as orderRoots
// does: a depth-first walk emits a cycle's members in an order that
// satisfies nothing, and the run proceeds to whichever check names
// the cycle properly.
func orderCandidates(candidates []migrateTarget) []migrateTarget {
	byName := make(map[string][]int, len(candidates))
	for i, t := range candidates {
		byName[t.name] = append(byName[t.name], i)
	}
	const (
		visiting = 1
		done     = 2
	)
	state := make([]int, len(candidates))
	ordered := make([]migrateTarget, 0, len(candidates))
	cycled := false
	var visit func(i int)
	visit = func(i int) {
		switch state[i] {
		case visiting:
			// Reached from inside its own subtree, which is not the
			// same as already placed.
			cycled = true
			return
		case done:
			return
		}
		state[i] = visiting
		for _, dep := range runtimeDepNames(candidates[i].recipe) {
			for _, j := range byName[dep] {
				// A recipe naming itself is not a cycle to report; it
				// is an edge with nothing on the other end.
				if j != i {
					visit(j)
				}
			}
		}
		state[i] = done
		ordered = append(ordered, candidates[i])
	}
	// The scan sorts its output, so ties break the same way on every
	// run over one store.
	for i := range candidates {
		visit(i)
	}
	if cycled {
		return candidates
	}
	return ordered
}

// runtimeDepNames names one recipe's runtime dependencies for this
// platform, deduplicated and in a stable order so the traversal that
// consumes them is deterministic.
func runtimeDepNames(r *recipe.Recipe) []string {
	deps := r.DependenciesForPlatform(runtime.GOOS, runtime.GOARCH).Runtime
	out := slices.Clone(deps)
	slices.Sort(out)
	return slices.Compact(out)
}

// migratePreflight checks every candidate against every scope before
// the first replacement.
//
// Two of design §13 revision 7's qualifying properties, together
// because they are one loop. Failing BEFORE replacing is what makes
// the machine-wide unit meaningful: a pass that replaced half the
// store and then met a disagreement would leave the machine in a
// state neither the old nor the new description covers, which is the
// outcome the machine-wide unit exists to prevent.
//
// And no scope is exempt. `gale lock --refresh` exempts the
// initiating scope because it is about to write the lock that
// ratifies the change; migrate writes no lock for anyone, so it may
// exempt nobody, and every scope is a participant against one
// proposed machine-wide state. Passing an empty selfGaleDir is how
// that is said: sameDir never matches it against a real scope.
//
// That last property is structural rather than tested, and the test
// says so: with no initiating scope there is nothing observable to
// distinguish exempting nobody from exempting a scope that is not
// there. It is held here, at the call site, or nowhere.
//
// The hash compared is the one migrate PROPOSES, taken from the
// recipe, so two scopes resolving different artifacts for one
// identity conflict even where neither lock records a hash.
func migratePreflight(
	galeHome, storeRoot string, scan migrateScanResult,
) error {
	scopes, err := projects.Scopes(galeHome)
	if err != nil {
		return err
	}
	platform := currentPlatform()
	for _, t := range scan.candidates {
		b := t.recipe.BinaryForPlatform(runtime.GOOS, runtime.GOARCH)
		if b == nil {
			// classifyForMigrate put it here because there was one, so
			// its absence now means the recipe changed underneath.
			return fmt.Errorf(
				"%w: %s: the recipe no longer declares a binary for %s",
				errMigrateUnreadable, t.dir, platform,
			)
		}
		if err := checkReplaceable(replaceQuery{
			galeHome:  galeHome,
			storeRoot: storeRoot,
			// No scope is exempt; see the doc comment.
			selfGaleDir: "",
			name:        t.name,
			version:     t.version,
			targetDir:   t.dir,
			wantSHA:     b.SHA256,
			platform:    platform,
			// One proposed state for the whole machine, so a scope
			// that merely references a candidate is a participant
			// rather than a vetoer. See replaceQuery.machineWide: the
			// per-scope rule refuses every upgrade-day store, which is
			// the state migrate exists to end.
			machineWide: true,
		}); err != nil {
			return err
		}
		if err := checkMigrateDependents(scopes, storeRoot, t); err != nil {
			return err
		}
	}
	return nil
}

// checkMigrateDependents refuses a candidate whose replacement would
// invalidate a provenance record some scope actively loads.
//
// Every scope, where `--refresh` walks only its own. Refresh can
// afford that because checkReplaceable refuses for every OTHER scope
// on the wider ground that it cannot tell which bytes that scope
// needs; migrate drops exactly that veto, so without this walk no
// other scope is watched at all.
//
// The argument for skipping the check entirely is that §7 makes
// provenance all-or-nothing, so nothing provenanced can record an
// unprovenanced dependency. That holds only for an undamaged
// history: a deleted record or a partial restore produces the state,
// and migrateScan establishes that a record PARSES, not that its
// digest still describes the store.
//
// The target is the physical directory, never the canonical
// spelling. A pre-revision candidate lives in a bare directory, and
// asking about the canonical one would exclude the wrong directory
// from the scan's own judgement while missing the one at risk.
func checkMigrateDependents(
	scopes []projects.Scope, storeRoot string, t migrateTarget,
) error {
	id := lockgraph.Key(t.name, t.version)
	for _, s := range scopes {
		if err := checkDependentsIn(dependentQuery{
			galeDir:   s.GaleDir,
			label:     s.Label,
			storeRoot: storeRoot,
			id:        id,
			targetDir: t.dir,
			// Not postMigrate: migrate is what is running. Unreadable
			// dependency metadata is §13's fail-before-replacing state,
			// and the exit is repairing the directory that holds it.
			remedy: "reinstall the package whose dependency metadata " +
				"cannot be read, then run gale migrate again",
		}); err != nil {
			return err
		}
	}
	return nil
}
