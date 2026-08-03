package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/kelp/gale/internal/installer"
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

// errMigrateCycle reports candidates whose runtime dependencies form
// a loop, so no order among them satisfies design §7.
var errMigrateCycle = errors.New(
	"candidates depend on each other in a cycle",
)

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
// Nodes are the candidates themselves and edges are drawn to the
// RESOLVED identity, never by name. Drawing them by name looks
// conservative and is not: a stale version of a dependency, still in
// the store and still a candidate, can name something above it, and
// the extra edge closes a loop that does not exist. A traversal that
// answers any cycle by returning its input then hands back
// alphabetical order — a dependent replaced before the dependency it
// actually links, which is the outcome this function exists to
// prevent. Resolving removes the edge instead of adding a lie.
//
// Runtime dependencies only. Migrate replaces binary-method
// directories, whose records serialize runtime edges alone, so a
// build tool cannot leave its dependent unattestable the way it does
// for a source artifact.
//
// A cycle that survives resolution is genuine, and it is refused
// rather than ordered arbitrarily. Replacement destroys bytes, so
// emitting a cycle's members in whatever order a depth-first walk
// reaches them would pay the whole cost for a sequence that
// satisfies nothing.
func orderCandidates(
	candidates []migrateTarget, resolve installer.RecipeResolver,
) ([]migrateTarget, error) {
	byID := make(map[string]int, len(candidates))
	names := make(map[string]bool, len(candidates))
	for i, t := range candidates {
		byID[lockgraph.Key(t.name, t.version)] = i
		names[t.name] = true
	}
	edges, err := candidateEdges(candidates, byID, names, resolve)
	if err != nil {
		return nil, err
	}
	const (
		visiting = 1
		done     = 2
	)
	state := make([]int, len(candidates))
	ordered := make([]migrateTarget, 0, len(candidates))
	var cycle []string
	var visit func(i int)
	visit = func(i int) {
		switch state[i] {
		case visiting:
			// Reached from inside its own subtree, which is not the
			// same as already placed.
			cycle = append(cycle, lockgraph.Key(
				candidates[i].name, candidates[i].version,
			))
			return
		case done:
			return
		}
		state[i] = visiting
		for _, j := range edges[i] {
			visit(j)
		}
		state[i] = done
		ordered = append(ordered, candidates[i])
	}
	// The scan sorts its output, so ties break the same way on every
	// run over one store.
	for i := range candidates {
		visit(i)
	}
	if len(cycle) > 0 {
		slices.Sort(cycle)
		return nil, fmt.Errorf(
			"%w: %s; re-pin one of them to a version that breaks the loop",
			errMigrateCycle, strings.Join(slices.Compact(cycle), ", "),
		)
	}
	return ordered, nil
}

// candidateEdges resolves each candidate's runtime dependencies to
// the exact identity a refetch would link, and keeps the edge only
// where that identity is itself a candidate.
//
// Resolution is attempted only for a dependency NAME some candidate
// carries, so an ordinary dependency that is already provenanced
// costs no registry lookup. A name that is a candidate and will not
// resolve is unreadable state under §13: gale cannot say which
// directory the refetch would bind to, and guessing would decide a
// destructive order.
func candidateEdges(
	candidates []migrateTarget, byID map[string]int, names map[string]bool,
	resolve installer.RecipeResolver,
) ([][]int, error) {
	resolved := make(map[string]string, len(names))
	edges := make([][]int, len(candidates))
	for i, t := range candidates {
		for _, dep := range runtimeDepNames(t.recipe) {
			if !names[dep] {
				continue
			}
			id, ok := resolved[dep]
			if !ok {
				r, err := resolve(dep)
				if err != nil {
					return nil, fmt.Errorf(
						"%w: ordering %s@%s: resolving its dependency %s: %w",
						errMigrateUnreadable, t.name, t.version, dep, err,
					)
				}
				id = lockgraph.Key(dep, r.Package.Full())
				resolved[dep] = id
			}
			if j, ok := byID[id]; ok && j != i {
				edges[i] = append(edges[i], j)
			}
		}
	}
	return edges, nil
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

// errMigrateNotBinary reports a candidate whose refetch produced a
// source build instead of the prebuilt binary migrate approved.
//
// An unlocked install falls back from a failed binary fetch to a
// source build, which is right for `gale install` and wrong here:
// migrate is a constrained replacement of BINARY-method directories
// (§13), and the whole machine was cleared against the hash the
// recipe declares for that binary. Committing a source artifact
// would replace bytes with something no scope was asked about.
var errMigrateNotBinary = errors.New(
	"the refetch did not produce the prebuilt binary",
)

// errMigrateHashMoved reports a refetch whose artifact is not the one
// preflight cleared with every scope.
var errMigrateHashMoved = errors.New(
	"the artifact changed between the preflight and the commit",
)

// checkMigrateCommit is everything that must still hold at the moment
// one candidate's replacement commits.
//
// Design §13's fourth qualifying property: revalidate concurrent lock
// and registry changes before EACH destructive commit, not once at
// the preflight. Every check here is re-established rather than
// carried, and projects.Scopes re-reads the registry and every lock,
// so a project registered since the preflight is seen.
//
// The artifact is pinned to the hash preflight cleared. Handing
// rep.Result.SHA256 straight to the veto would ask the machine about
// whatever just landed, so a run could clear artifact X with every
// scope and then commit artifact Y with nobody's agreement.
func checkMigrateCommit(
	galeHome, storeRoot string, t migrateTarget, rep installer.Replacement,
) error {
	name, full := t.name, t.version
	// The classification, re-established inside the commit locks. It
	// was formed before the per-package lock existed, and a concurrent
	// gale can provenance the directory in between.
	if err := stillUnprovenanced(rep.CanonicalDir, name, full); err != nil {
		return err
	}
	// The candidate must itself be attested. recordProvenance commits
	// an artifact with NO record when its closure cannot be attested,
	// which is right for an ordinary install and useless here:
	// trading one unprovenanced directory for another destroys bytes
	// and repairs nothing, while the run reports success.
	if _, err := provenance.ReadUnverified(rep.StagingDir); err != nil {
		return fmt.Errorf(
			"the refetched %s@%s is itself unprovenanced, so replacing "+
				"would destroy bytes without repairing anything (%v): %w",
			name, full, err, errCandidateUnprovenanced,
		)
	}
	b := t.recipe.BinaryForPlatform(runtime.GOOS, runtime.GOARCH)
	if b == nil {
		return fmt.Errorf(
			"%w: %s@%s: the recipe stopped declaring one for %s",
			errMigrateNotBinary, name, full, currentPlatform(),
		)
	}
	if rep.Result.Method != installer.MethodBinary {
		return fmt.Errorf(
			"%w: %s@%s built from source instead; migrate replaces only "+
				"binary-method directories, and the machine was cleared "+
				"against the declared binary", errMigrateNotBinary, name, full,
		)
	}
	if rep.Result.SHA256 != b.SHA256 {
		return fmt.Errorf(
			"%w: %s@%s was cleared at %s and the refetch produced %s",
			errMigrateHashMoved, name, full, b.SHA256, rep.Result.SHA256,
		)
	}
	scopes, err := projects.Scopes(galeHome)
	if err != nil {
		return err
	}
	if err := checkMigrateDependents(scopes, storeRoot, t); err != nil {
		return err
	}
	return checkReplaceable(replaceQuery{
		galeHome: galeHome, storeRoot: storeRoot,
		// No scope is exempt: migrate writes no lock for anyone.
		selfGaleDir: "",
		name:        name, version: full,
		targetDir: rep.CanonicalDir,
		// The cleared hash, which the check above has just proved the
		// committed artifact carries.
		wantSHA:     b.SHA256,
		platform:    currentPlatform(),
		machineWide: true,
	})
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
