package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/kelp/gale/internal/filelock"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/projects"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
)

// runMigrate converges the whole machine on attestable store
// directories: one pass, every scope, every unprovenanced
// binary-method directory.
//
// The order is the design (§13). Classify the entire store, clear
// every candidate with every scope, and only then replace anything,
// so a refusal costs nothing and one pass leaves the machine in one
// describable state rather than half of two.
func runMigrate(ctx *cmdContext, out *output.Output) error {
	// filepath.Dir(storeRoot) is the global gale dir at either scope,
	// since the store is shared.
	galeHome := filepath.Dir(ctx.StoreRoot)
	scan, err := migrateScan(ctx.StoreRoot,
		func(name, version string) (*recipe.Recipe, error) {
			return resolveVersionedRecipe(ctx, name, version)
		})
	if err != nil {
		return err
	}
	if len(scan.candidates) == 0 && len(scan.sourceOnly) == 0 {
		out.Success("Every store directory already attests itself.")
		return nil
	}
	if err := migratePreflight(galeHome, ctx.StoreRoot, scan); err != nil {
		return err
	}
	ordered, err := orderCandidates(scan.candidates, ctx.Resolver)
	if err != nil {
		return err
	}
	if dryRun {
		for _, t := range ordered {
			out.Info(fmt.Sprintf("migrate %s@%s (%s)", t.name, t.version, t.dir))
		}
		reportSourceOnly(ctx, galeHome, out, scan.sourceOnly)
		return nil
	}
	var relocated []migrateTarget
	for _, t := range ordered {
		moved, err := migrateOne(ctx, galeHome, t, out)
		if err != nil {
			return err
		}
		if moved {
			relocated = append(relocated, t)
		}
	}
	if err := finishRelocations(ctx, galeHome, relocated, out); err != nil {
		return err
	}
	reportSourceOnly(ctx, galeHome, out, scan.sourceOnly)
	return nil
}

// finishRelocations moves every scope off the pre-revision paths and
// then removes them, after the whole pass rather than during it.
//
// Deferred on purpose. A bare directory is reached by whatever was
// built against it, and those dependents are candidates too; one
// processed later in the pass drops its reference when it is
// refetched. Removing during the loop would refuse a directory that
// is about to be free, which for a machine-wide converge is the
// difference between finishing and stopping halfway.
func finishRelocations(
	ctx *cmdContext, galeHome string, relocated []migrateTarget,
	out *output.Output,
) error {
	if len(relocated) == 0 {
		return nil
	}
	// The farm first, once, for the whole machine. Every relocation
	// deferred its farm work (see migrateOne), so right now the shared
	// farm still points into the pre-revision directories this
	// function is about to delete. Rebuilding the scopes does not
	// repair that on its own: a scope that requires a relocated
	// identity through its LOCK alone has no generation to rebuild,
	// and its binaries would be left resolving a deleted path.
	//
	// Before the regenerations rather than after, because it is also
	// the guarded step: a claim this machine cannot satisfy refuses
	// here, while no scope's symlinks have been touched.
	//
	// The rebuild is a destructive commit of its own, though — it
	// repoints sonames the whole machine resolves through — so §13's
	// revalidation runs first, inside the lock the rebuild holds. A
	// scope whose lock moved to another hash after the install would
	// otherwise be discovered only by the later removal check: that
	// check preserves the pre-revision bytes and the run looks safe,
	// while the farm has already moved underneath the scope that
	// disagreed.
	if err := generation.RebuildFarm(ctx.StoreRoot, func() error {
		return revalidateRelocations(galeHome, ctx.StoreRoot, relocated)
	}); err != nil {
		return err
	}
	scopes, err := projects.Scopes(galeHome)
	if err != nil {
		return err
	}
	if err := regenerateScopes(scopes, ctx.StoreRoot, out); err != nil {
		return err
	}
	for _, t := range relocated {
		if err := removeRelocatedDir(
			ctx.StoreRoot, galeHome, t, out,
		); err != nil {
			return err
		}
	}
	return nil
}

// reportSourceOnly names every unprovenanced directory a refetch
// cannot reach, and what clearing it costs.
//
// Printed rather than stamped, and printed even when migrate replaced
// nothing else. §13 rejected an "unverified" marker precisely so that
// a migration cannot end by claiming a directory it never verified,
// which makes saying what is left the command's whole obligation
// toward these.
//
// Split by WHERE the directory sits, because the two halves are
// replaced by different commands. A canonical source directory is what
// `lock --refresh` was built to replace. A pre-revision one is not:
// refresh checks the canonical path alone and declines a bare
// directory by design, and migrate cannot refetch a source build. What
// converges a bare directory instead depends on what holds it, which
// is reportUnresolved's subject.
func reportSourceOnly(
	ctx *cmdContext, galeHome string, out *output.Output,
	sourceOnly []migrateTarget,
) {
	var canonical, bare []migrateTarget
	for _, t := range sourceOnly {
		if sameDir(t.dir, filepath.Join(ctx.StoreRoot, t.name, t.version)) {
			canonical = append(canonical, t)
			continue
		}
		bare = append(bare, t)
	}
	reportRebuildable(out, canonical)
	reportUnresolved(ctx, galeHome, out, bare)
}

// reportRebuildable names the source directories a refresh can clear.
func reportRebuildable(out *output.Output, targets []migrateTarget) {
	if len(targets) == 0 {
		return
	}
	out.Warn(fmt.Sprintf(
		"%d source-built %s cannot be migrated by refetching, because "+
			"the bytes came from a build on this machine and no download "+
			"reproduces them:", len(targets),
		plural(len(targets), "package", "packages"),
	))
	listTargets(out, targets)
	// NOT `gale install --build`. That path calls the installer with
	// force=false, so an occupied store directory satisfies the cache
	// check and returns MethodCached before anything is built or any
	// record is written — it cannot clear the condition reported
	// here. `lock --refresh` reinstalls with force, and §13 gives it
	// permission to replace exactly this directory.
	out.Info("Rebuild each in the scope that declares it with " +
		"`gale lock --refresh <pkg>`, which costs a full source build " +
		"per package. A directory no scope declares cannot be " +
		"rebuilt; `gale gc` clears it once nothing links it.")
}

// reportUnresolved names, for each pre-revision source directory, the
// command that converges it — and says plainly where there is none.
//
// One line used to cover all of them: "No gale command converges them
// today". That was true of one of the three states such a directory
// can be in, and it hid working escapes from the other two (gh#200).
//
//   - Nothing holds it. It is an orphan, and `gale gc` sweeps it.
//   - One scope holds it, as a declared root. Unlocked sync reports a
//     directory with no dependency metadata as stale and reinstalls it
//     into the canonical path, additively: `gale sync`, or
//     `gale sync --no-frozen` where that scope carries a lock a locked
//     sync fails closed on.
//   - Anything else — several scopes, or one that reaches it only
//     through a closure. No per-scope sequence converges that, and
//     naming one that converges nothing would be worse than naming
//     none.
//
// Nothing here mutates, and no refusal moved. Migrate still may not
// replace any of these directories; all that changed is what it says
// about them.
func reportUnresolved(
	ctx *cmdContext, galeHome string, out *output.Output,
	targets []migrateTarget,
) {
	if len(targets) == 0 {
		return
	}
	out.Warn(fmt.Sprintf(
		"%d source-built %s predate revisions, so each sits in a bare "+
			"directory migrate cannot refetch and `lock --refresh` does "+
			"not look at:", len(targets),
		plural(len(targets), "package", "packages"),
	))
	out.Info("Each stays unattested until it moves, so a locked " +
		"environment will keep refusing to activate it.")
	// Read once, after the early return: the retention set costs a
	// registry walk, and a store with no pre-revision directory in it
	// must not pay for a report with nothing to say.
	holds := readPreRevisionHolds(ctx, galeHome)
	var orphans, stuck []migrateTarget
	var roots []preRevisionRoot
	for _, t := range targets {
		state, s := holds.classify(t)
		switch state {
		case preRevisionOrphaned:
			orphans = append(orphans, t)
		case preRevisionDeclared:
			roots = append(roots, preRevisionRoot{target: t, scope: s})
		case preRevisionStuck:
			stuck = append(stuck, t)
		}
	}
	reportPreRevisionRoots(out, roots)
	reportPreRevisionOrphans(out, holds, orphans)
	reportPreRevisionStuck(out, stuck)
}

// preRevisionRoot pairs a bare directory with the one scope whose sync
// converges it, since the spelling of that sync is the scope's.
type preRevisionRoot struct {
	target migrateTarget
	scope  projects.Scope
}

// reportPreRevisionRoots names the sync that converges each declared
// root, and hands off the case where the sync lands the bytes without
// attesting them.
//
// Additive, which is why it may be named at all: `Reinstall` stages
// into a sibling and commits at the CANONICAL path, so the bare
// directory survives the operation and becomes a gc candidate by
// itself once store resolution prefers the populated sibling. No pin
// is touched and there is no window in which the bytes are gone.
func reportPreRevisionRoots(out *output.Output, roots []preRevisionRoot) {
	if len(roots) == 0 {
		return
	}
	out.Info("These are declared roots of the scope named beside each, " +
		"which a sync reinstalls into the canonical directory. That " +
		"adds a directory and deletes nothing:")
	for _, r := range roots {
		out.Info(fmt.Sprintf(
			"  %s@%s (%s): run `%s` in %s",
			r.target.name, r.target.version, r.target.dir,
			syncSpelling(r.scope), r.scope.Label,
		))
	}
	// recordProvenance is all-or-nothing: it commits the directory
	// with NO record when the closure below it cannot be attested.
	// Since a pre-revision leftover is most often a dependency, the
	// common shape is a root that lands canonically and still records
	// nothing. Unsaid, that reads as the sync having failed.
	out.Info("A reinstall whose closure cannot be attested commits " +
		"with no provenance record. That is the next step rather than " +
		"a failure: converge the closure from the bottom up, then " +
		"`gale lock --refresh <pkg>`.")
}

// reportPreRevisionOrphans names the directories `gale gc` sweeps.
//
// Hedged when gc's own retention could not be computed, because the
// claim is gc's to keep: promising a sweep gc declines to perform is
// a harmless no-op and still wrong advice.
func reportPreRevisionOrphans(
	out *output.Output, holds preRevisionHolds, orphans []migrateTarget,
) {
	if len(orphans) == 0 {
		return
	}
	if holds.retentionUncertain {
		out.Info("Nothing links or pins these, so `gale gc` clears " +
			"them — unless a config gale could not read still pins one:")
	} else {
		out.Info("Nothing links or pins these, so `gale gc` clears them:")
	}
	listTargets(out, orphans)
}

// reportPreRevisionStuck says what has no remedy, and why the obvious
// manual sequence is not one.
//
// `gale remove` plus a reinstall is refused in its unqualified form
// for three separate reasons, each of which costs something the user
// cannot get back: it deletes the pin from every section carrying it,
// the store directory may be the last copy of a version the registry
// no longer serves, and across scopes it does not even fail — the
// store entry another scope references is kept and the reinstall then
// takes the back-compat cache hit and reports success.
func reportPreRevisionStuck(out *output.Output, stuck []migrateTarget) {
	if len(stuck) == 0 {
		return
	}
	out.Info("These are reached by more than one scope, or by one scope " +
		"that holds them as a dependency rather than as a declared " +
		"root. There is no safe sequence of per-scope commands for " +
		"those, and gale names none rather than one that converges " +
		"nothing; the gap is tracked in gh#200:")
	listTargets(out, stuck)
	out.Info("Removing and reinstalling is not that sequence: it " +
		"deletes the pin from every section carrying it, losing " +
		"host-overlay placement; the directory may hold the last copy " +
		"of a version the registry no longer serves; and where another " +
		"scope still references the entry it silently does nothing, " +
		"because the store entry is kept and the reinstall then meets " +
		"the back-compat cache hit.")
}

// preRevisionState is what holds a bare source directory, and
// therefore which command converges it.
type preRevisionState int

const (
	// preRevisionOrphaned: no scope's active closure reaches the
	// directory and gc's retention set does not hold it.
	preRevisionOrphaned preRevisionState = iota
	// preRevisionDeclared: exactly one scope holds it, and holds it as
	// a root its own manifest declares.
	preRevisionDeclared
	// preRevisionStuck: several scopes hold it, or the one that does
	// reaches it only through a recorded closure.
	preRevisionStuck
)

// preRevisionHolds is the machine read once, in the terms the report
// needs: every scope, and gc's retention set.
type preRevisionHolds struct {
	storeRoot string
	scopes    []projects.Scope
	// retained is GC'S retention set, deliberately not the closure
	// walk migrate already owns. The two disagree: gc additionally
	// keeps config-derived pin keys across every project and host and
	// expands each retained package's recorded dep closure, so a
	// pinned-but-unlinked directory is retained by gc and reached by
	// nobody. Only gc's answer may be reported as gc's behaviour.
	retained map[string]bool
	// retentionUncertain records that gc's predicate could not be
	// computed. An unreadable reference source is not proof of
	// non-reference, so the report hedges rather than promising a
	// sweep — the same reading gc itself takes when it refuses.
	retentionUncertain bool
	// scopesUnknown records that the scope list itself could not be
	// read, which is a stronger failure than an unreadable retention
	// source: with no scopes there is nothing to walk, so an empty
	// holder set would read as "nothing reaches it" and every
	// directory would be reported as an orphan.
	scopesUnknown bool
}

// readPreRevisionHolds collects the facts the classification rests on.
//
// Best-effort throughout, because this is a report: migrate has
// already done its work and returned success, and a machine gale
// cannot fully read is a reason to say less, never to fail a pass that
// converged everything it could.
func readPreRevisionHolds(ctx *cmdContext, galeHome string) preRevisionHolds {
	h := preRevisionHolds{storeRoot: ctx.StoreRoot, retained: map[string]bool{}}
	scopes, err := projects.Scopes(galeHome)
	if err != nil {
		// Every directory then lands in the no-remedy bucket.
		// Unreachable in practice — migratePreflight reads the same
		// registry before anything here runs — but silence about a
		// scope must not read as that scope agreeing there is nothing
		// to do.
		h.retentionUncertain, h.scopesUnknown = true, true
		return h
	}
	h.scopes = scopes
	// gc's own entry point, with gc's own arguments. The project pass
	// is the context's scope when it is not the global one, exactly as
	// gc resolves it; every other project arrives through the registry
	// walk inside.
	projPath, projGaleDir := "", ""
	if ctx.GaleDir != "" && !sameDir(ctx.GaleDir, galeHome) {
		projPath, projGaleDir = ctx.GalePath, ctx.GaleDir
	}
	retained, _, retErr := collectGCRetention(
		galeHome, projPath, projGaleDir, store.NewStore(ctx.StoreRoot),
		ctx.Resolver, ctx.versionedRecipeResolver(),
	)
	h.retained = retained
	h.retentionUncertain = retErr != nil
	return h
}

// classify decides which of the three states one bare directory is in,
// and for a declared root which scope's sync converges it.
//
// Reachability comes from checkNothingReaches, one scope at a time.
// The walk is migrate's own and is asked the question it already
// answers; asking it per scope is what turns "something reaches this"
// into the count the classification needs. A scope whose closure
// cannot be read counts as reaching, which is the conservative
// reading: gale cannot tell, so it must not promise a sweep.
func (h preRevisionHolds) classify(
	t migrateTarget,
) (preRevisionState, projects.Scope) {
	var holders, declarers []projects.Scope
	for _, s := range h.scopes {
		declares := scopeDeclares(s, t.name)
		if declares {
			declarers = append(declarers, s)
		}
		if !declares && checkNothingReaches(
			[]projects.Scope{s}, h.storeRoot, t,
		) == nil {
			continue
		}
		holders = append(holders, s)
	}
	switch {
	// Every declarer is a holder, so one of each is the same scope:
	// the only one that holds the directory, and it holds it as a root
	// its own sync visits.
	case len(holders) == 1 && len(declarers) == 1:
		return preRevisionDeclared, holders[0]
	case len(holders) == 0 && !h.scopesUnknown && !h.retains(t):
		return preRevisionOrphaned, projects.Scope{}
	default:
		return preRevisionStuck, projects.Scope{}
	}
}

// retains reports whether gc keeps this directory.
//
// Keyed on the directory's own basename rather than on the target's
// canonical version: a pre-revision directory IS the bare spelling,
// and gc's keys come from store.List and from StorePath's bare
// fallback, both of which name it that way.
func (h preRevisionHolds) retains(t migrateTarget) bool {
	return isReferenced(t.name, filepath.Base(t.dir), h.retained)
}

// scopeDeclares reports whether the scope's manifest names the
// package, which is what makes it a root that scope's sync visits.
//
// The effective package set for the current host, because that is the
// set sync reads. A manifest that cannot be read declares nothing:
// this only ever narrows the advice, and the alternative is telling
// someone to sync a scope that will not sync.
func scopeDeclares(s projects.Scope, name string) bool {
	pkgs, err := readConfigPackages(scopeConfigPath(s))
	if err != nil {
		return false
	}
	_, ok := pkgs[name]
	return ok
}

// scopeConfigPath is the gale.toml beside a scope's lock, which is
// where it sits for both scope shapes: <project>/gale.toml next to
// <project>/gale.lock, and <gale home>/gale.toml next to
// <gale home>/gale.lock.
func scopeConfigPath(s projects.Scope) string {
	return filepath.Join(filepath.Dir(s.LockPath), "gale.toml")
}

// syncSpelling is the sync that actually runs in this scope.
//
// A legacy lock, or one that will not parse, hard-fails a locked sync
// before any package is looked at, so plain `gale sync` in such a
// scope converges nothing. `--no-frozen` skips loading the lock
// entirely rather than loading and bypassing it, which is why it works
// on a file the loader rejects.
func syncSpelling(s projects.Scope) string {
	lv, err := lockfile.Load(s.LockPath)
	if err != nil || lv.Kind == lockfile.KindLegacy {
		return "gale sync --no-frozen"
	}
	return "gale sync"
}

func listTargets(out *output.Output, targets []migrateTarget) {
	for _, t := range targets {
		out.Info(fmt.Sprintf("  %s@%s (%s)", t.name, t.version, t.dir))
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// migrateOne replaces a single candidate, refetching and verifying
// before anything is destroyed.
//
// It reports whether the candidate needs its pre-revision directory
// removed, which the caller defers to the end of the pass. Two
// shapes, decided by where the bytes actually sit. A candidate in its
// canonical directory is superseded in place, and the installer's
// ReplaceGuard stands between the fetch and the commit. A
// pre-revision candidate lives in a BARE directory the installer
// never touches: the canonical destination is absent, so no guard
// fires, and everything the guard would have said has to be said
// here.
//
// BinaryOnly is set for both. Migrate replaces binary-method
// directories and the machine was cleared against the hash the
// recipe declares, so a demotion to a source build would commit
// bytes nobody was asked about. For the canonical shape the guard
// would catch it; for the relocating shape nothing would, because
// the commit into an absent directory is never guarded.
//
// DeferFarm is set for the RELOCATING shape alone, and it is what
// makes that shape possible at all. Migrate runs machine-wide, so
// every registered scope is an external farm claimant, and a scope
// loading a versioned library out of the bare directory claims that
// soname AT the bare path. The canonical copy proposes the same
// soname somewhere else, which design §4 tells GuardPopulate to
// refuse — the guard would veto the one operation that ends the
// disagreement it is reporting. Deferring costs nothing in the
// direction that matters: the commit adds a directory and touches
// neither the pre-revision bytes nor a single farm link, so a
// failure here leaves the machine exactly as it was, and
// finishRelocations puts the farm right for every scope at once.
//
// The canonical shape keeps the per-commit guard. It replaces bytes
// in the directory the farm already points at, so its claim and the
// claimants' agree about the path, and the ordinary rule applies.
func migrateOne(
	ctx *cmdContext, galeHome string, t migrateTarget, out *output.Output,
) (bool, error) {
	name, full := t.name, t.version
	// Decided by the scan, which had the store layout in hand. Asking
	// again here would be a second derivation of one fact, and the
	// two could disagree.
	relocating := t.bare

	if relocating && canonicalAttests(ctx.StoreRoot, t) == nil {
		// Resume. An earlier pass installed and verified the canonical
		// artifact and stopped before the bare directory went away.
		// Reinstalling over it would meet its own record and fail
		// stillUnprovenanced, so without this branch one interrupted
		// relocation would be unrecoverable by the command that
		// created it.
		out.Info(fmt.Sprintf(
			"%s@%s is already migrated; finishing the move from %s...",
			name, full, t.dir,
		))
		return true, nil
	}
	if relocating {
		out.Info(fmt.Sprintf(
			"Migrating %s@%s from %s, which predates revisions...",
			name, full, t.dir,
		))
	} else {
		out.Info(fmt.Sprintf("Migrating unprovenanced %s@%s...", name, full))
	}

	prevGuard, prevBinary, prevFarm := ctx.Installer.ReplaceGuard,
		ctx.Installer.BinaryOnly, ctx.Installer.DeferFarm
	ctx.Installer.ReplaceGuard = func(rep installer.Replacement) error {
		return checkMigrateCommit(galeHome, ctx.StoreRoot, t, rep)
	}
	ctx.Installer.BinaryOnly = true
	ctx.Installer.DeferFarm = relocating
	defer func() {
		ctx.Installer.ReplaceGuard = prevGuard
		ctx.Installer.BinaryOnly = prevBinary
		ctx.Installer.DeferFarm = prevFarm
	}()

	if _, err := ctx.Installer.Reinstall(t.recipe); err != nil {
		return false, fmt.Errorf("migrating %s@%s: %w", name, full, err)
	}
	if !relocating {
		return false, nil
	}
	// The commit landed unguarded, so it is checked now. A refusal
	// here destroys nothing: the bare directory is untouched and the
	// next run resumes from the canonical record.
	if err := canonicalAttests(ctx.StoreRoot, t); err != nil {
		return false, err
	}
	return true, nil
}

// canonicalAttests reports whether the canonical directory now holds
// the artifact every scope was cleared against.
//
// Read from the RECORD rather than from an InstallResult, so the same
// question can be asked again later. Design §13 requires
// revalidation before each destructive commit, and a relocation's
// destructive step happens after generations have been rebuilt, long
// after the install returned; carrying the result across that gap
// would prove something about a moment that has passed.
//
// It is also the resume predicate. An interrupted relocation leaves
// exactly this state, and a run that could not recognise it would
// reinstall over its own record and fail.
//
// VerifyShallow, which is neither of the obvious two readers.
// ReadUnverified is too weak: this predicate authorizes destroying
// the pre-revision bytes, so a record somebody copied into the
// directory must not satisfy it. VerifyAgainstStore is too strong,
// and the reason reaches one level deeper than it appears. A binary
// artifact records runtime edges only, so its own record survives a
// collected build dependency — but a runtime dependency may itself
// be source-method and carry build edges, and the deep reader
// recurses into those. Design §12 permits a build dependency to
// disappear once the bytes it produced are committed, so the deep
// reader would refuse a graph that is perfectly sound, and
// dependency-first ordering does not help: the source dependency is
// already provenanced and the collected tool is not a candidate.
func canonicalAttests(storeRoot string, t migrateTarget) error {
	name, full := t.name, t.version
	rec, err := provenance.VerifyShallow(
		storeRoot, name, full, currentPlatform(),
	)
	if err != nil {
		return fmt.Errorf(
			"the migrated %s@%s does not attest itself, so removing %s "+
				"would destroy bytes without repairing anything (%v): %w",
			name, full, t.dir, err, errCandidateUnprovenanced,
		)
	}
	b := t.recipe.BinaryForPlatform(runtime.GOOS, runtime.GOARCH)
	if b == nil || rec.Method != lockgraph.MethodBinary {
		return fmt.Errorf(
			"%w: %s@%s did not come from the declared binary, and the "+
				"machine was cleared against that artifact alone",
			errMigrateNotBinary, name, full,
		)
	}
	if rec.SHA256 != b.SHA256 {
		return fmt.Errorf(
			"%w: %s@%s was cleared at %s and the store now records %s",
			errMigrateHashMoved, name, full, b.SHA256, rec.SHA256,
		)
	}
	return nil
}

// revalidateRelocations re-establishes every relocation's predicate
// against the machine as it is right now.
//
// Read fresh, never carried: projects.Scopes re-reads the registry
// and every lock, so a scope registered or re-locked since the
// install is seen. The same predicate runs again before each
// individual removal, which is not redundant — these are two
// destructive commits, and §13 asks for revalidation before each.
func revalidateRelocations(
	galeHome, storeRoot string, relocated []migrateTarget,
) error {
	scopes, err := projects.Scopes(galeHome)
	if err != nil {
		return err
	}
	for _, t := range relocated {
		if err := stillUnprovenanced(t.dir, t.name, t.version); err != nil {
			return err
		}
		if err := checkRelocateCommit(
			galeHome, storeRoot, t, scopes,
		); err != nil {
			return err
		}
	}
	return nil
}

// regenerateScopes moves every scope off any pre-revision path,
// once, after the whole pass.
//
// Once rather than per candidate: a rebuild is idempotent and reads
// each scope's whole active set, so doing it per relocation would
// repeat identical work and multiply every scope's generation
// history by the number of pre-revision directories on the machine.
func regenerateScopes(
	scopes []projects.Scope, storeRoot string, out *output.Output,
) error {
	for _, s := range scopes {
		if err := regenerateScope(s, storeRoot, out); err != nil {
			return err
		}
	}
	return nil
}

// regenerateScope rebuilds one scope's generation from the package
// set it is already running, so its symlinks follow the identity into
// the canonical directory.
//
// The scope's ACTIVE package set, never its manifest. Reading
// gale.toml would apply unrelated pending edits to a scope whose
// owner did not ask for them, turning a relocation into a silent sync
// of somebody else's project. CurrentVersions reports the version
// directory each link actually names, so a pre-revision root comes
// back as a bare "1.0" and store resolution carries it to the
// canonical "1.0-1" that now exists beside it.
//
// A scope that has never synced links nothing and is skipped: there
// is no generation to move and Build would manufacture one.
func regenerateScope(
	s projects.Scope, storeRoot string, out *output.Output,
) error {
	pkgs, err := generation.CurrentVersions(s.GaleDir, storeRoot)
	if err != nil {
		return fmt.Errorf(
			"reading the active generation of %s: %w", s.Label, err,
		)
	}
	if len(pkgs) == 0 {
		return nil
	}
	// [bin] alone is read from the scope's manifest — never its
	// packages, per the paragraph above. A generation built before
	// gh#190 may hold a shadowed executable, and the rebuild below
	// refuses one. Without the overrides the scope could not be
	// migrated at all: the collision lives in the active package set,
	// so declaring the winner would fix every other command and leave
	// this one stuck.
	if err := generation.BuildWithOptions(
		pkgs, s.GaleDir, storeRoot,
		generation.Options{BinOverrides: scopeBinOverrides(s.GaleDir)},
	); err != nil {
		return fmt.Errorf("regenerating %s: %w", s.Label, err)
	}
	out.Info(fmt.Sprintf("  regenerated %s", s.Label))
	return nil
}

// scopeBinOverrides returns the [bin] table of the gale.toml that
// owns galeDir, or nil when there is none to read.
//
// nil on any failure, deliberately: this only ever widens what a
// rebuild accepts, so an unreadable manifest costs the scope its
// overrides and nothing else. The rebuild still refuses the
// collision, with the message that names the fix.
func scopeBinOverrides(galeDir string) map[string]string {
	globalDir, err := galeConfigDir()
	if err != nil {
		return nil
	}
	configPath := filepath.Join(filepath.Dir(galeDir), "gale.toml")
	if sameDir(galeDir, globalDir) {
		configPath = filepath.Join(globalDir, "gale.toml")
	}
	cfg, err := loadEffectiveConfig(configPath)
	if err != nil {
		return nil
	}
	return cfg.Bin
}

// errBareDirStillReferenced reports a pre-revision directory some
// scope still reaches after every generation was rebuilt.
var errBareDirStillReferenced = errors.New(
	"a scope still reaches the pre-revision directory",
)

// removeRelocatedDir deletes the pre-revision directory, re-proving
// the whole relocation immediately before it does.
//
// Under the generation lock, and everything is re-established inside
// it. Design §13's fourth qualifying property is revalidation before
// each destructive commit, and this is that commit: the scope list,
// the locks, the dependent records and the canonical artifact are all
// read again, because generations were rebuilt since the install
// returned and a concurrent gale had that whole window to change any
// of them. The lock is the one generation.Build takes, so no scope
// can acquire a reference between the walk that finds none and the
// removal that rests on it.
//
// A directory something still reaches is an error, not a warning.
// Rebuilding a generation moves the ROOTS that resolved to the bare
// directory; a transitive dependency is reached through a recorded
// closure instead, so a reference can survive every rebuild. The
// bytes are preserved either way, but the pass has not converged and
// reporting success would tell the user the opposite of what
// happened.
func removeRelocatedDir(
	storeRoot, galeHome string, t migrateTarget, out *output.Output,
) error {
	lockPath := filepath.Join(filepath.Dir(storeRoot), "generation.lock")
	return filelock.With(lockPath, func() error {
		scopes, err := projects.Scopes(galeHome)
		if err != nil {
			return err
		}
		// The bare directory must still be the unprovenanced thing
		// this pass decided about. One that gained a record in the
		// meantime is somebody else's, and deleting it would destroy
		// the very record §13 exists to protect.
		if err := stillUnprovenanced(t.dir, t.name, t.version); err != nil {
			return err
		}
		if err := checkRelocateCommit(
			galeHome, storeRoot, t, scopes,
		); err != nil {
			return err
		}
		if err := checkNothingReaches(scopes, storeRoot, t); err != nil {
			return err
		}
		if err := os.RemoveAll(t.dir); err != nil {
			return fmt.Errorf("removing %s: %w", t.dir, err)
		}
		out.Success(fmt.Sprintf("  removed %s", t.dir))
		return nil
	})
}

// checkRelocateCommit re-establishes, for a relocation, everything
// checkMigrateCommit establishes inside the ReplaceGuard.
//
// The guard cannot run here: it fires only when a staged artifact
// supersedes an occupied CANONICAL directory, and a pre-revision
// candidate's canonical directory is absent. Without this the one
// case §13 hands to migrate alone would be the one case with no
// revalidation at all.
func checkRelocateCommit(
	galeHome, storeRoot string, t migrateTarget, scopes []projects.Scope,
) error {
	if err := canonicalAttests(storeRoot, t); err != nil {
		return err
	}
	if err := checkMigrateDependents(scopes, storeRoot, t); err != nil {
		return err
	}
	b := t.recipe.BinaryForPlatform(runtime.GOOS, runtime.GOARCH)
	return checkReplaceable(replaceQuery{
		galeHome: galeHome, storeRoot: storeRoot,
		selfGaleDir: "",
		name:        t.name, version: t.version,
		// The bare directory, which is what this operation destroys.
		targetDir:   t.dir,
		wantSHA:     b.SHA256,
		platform:    currentPlatform(),
		machineWide: true,
	})
}

// checkNothingReaches refuses the removal while any scope's active
// closure still contains the directory.
func checkNothingReaches(
	scopes []projects.Scope, storeRoot string, t migrateTarget,
) error {
	target := canonicalStoreDir(t.dir)
	for _, s := range scopes {
		roots, err := generation.AuthoritativeGenerationDirs(
			s.GaleDir, storeRoot,
		)
		if err != nil {
			return fmt.Errorf(
				"reading the active generation of %s before removing %s: %w",
				s.Label, t.dir, err,
			)
		}
		dirs, complete := generation.AuthoritativeClosure(roots, storeRoot)
		if !complete {
			return fmt.Errorf(
				"%w in %s, so gale cannot tell whether it reaches %s; "+
					"reinstall the package whose dependency metadata "+
					"cannot be read, then run gale migrate again",
				errScopeClosureUnreadable, s.Label, t.dir,
			)
		}
		if dirs[target] {
			return fmt.Errorf(
				"%w: %s still reaches %s, so it was left in place; the "+
					"canonical directory is installed and attested, so "+
					"re-running gale migrate after that scope no longer "+
					"loads it will finish the move",
				errBareDirStillReferenced, s.Label, t.dir,
			)
		}
	}
	return nil
}
