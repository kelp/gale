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
	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/projects"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/recipe"
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
		reportSourceOnly(out, scan.sourceOnly)
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
	reportSourceOnly(out, scan.sourceOnly)
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
func reportSourceOnly(out *output.Output, sourceOnly []migrateTarget) {
	if len(sourceOnly) == 0 {
		return
	}
	out.Warn(fmt.Sprintf(
		"%d source-built %s cannot be migrated by refetching, because "+
			"the bytes came from a build on this machine and no download "+
			"reproduces them:", len(sourceOnly),
		plural(len(sourceOnly), "package", "packages"),
	))
	for _, t := range sourceOnly {
		out.Info(fmt.Sprintf("  %s@%s (%s)", t.name, t.version, t.dir))
	}
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
func migrateOne(
	ctx *cmdContext, galeHome string, t migrateTarget, out *output.Output,
) (bool, error) {
	name, full := t.name, t.version
	canonical := filepath.Join(ctx.StoreRoot, name, full)
	relocating := !sameDir(t.dir, canonical)

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

	prevGuard, prevBinary := ctx.Installer.ReplaceGuard, ctx.Installer.BinaryOnly
	ctx.Installer.ReplaceGuard = func(rep installer.Replacement) error {
		return checkMigrateCommit(galeHome, ctx.StoreRoot, t, rep)
	}
	ctx.Installer.BinaryOnly = true
	defer func() {
		ctx.Installer.ReplaceGuard = prevGuard
		ctx.Installer.BinaryOnly = prevBinary
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
func canonicalAttests(storeRoot string, t migrateTarget) error {
	name, full := t.name, t.version
	canonical := filepath.Join(storeRoot, name, full)
	rec, err := provenance.ReadUnverified(canonical)
	if err != nil {
		return fmt.Errorf(
			"the migrated %s@%s is itself unprovenanced, so removing %s "+
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
	if err := generation.Build(pkgs, s.GaleDir, storeRoot); err != nil {
		return fmt.Errorf("regenerating %s: %w", s.Label, err)
	}
	out.Info(fmt.Sprintf("  regenerated %s", s.Label))
	return nil
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
