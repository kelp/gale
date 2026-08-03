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
	ordered := orderCandidates(scan.candidates)
	if dryRun {
		for _, t := range ordered {
			out.Info(fmt.Sprintf("migrate %s@%s (%s)", t.name, t.version, t.dir))
		}
		reportSourceOnly(out, scan.sourceOnly)
		return nil
	}
	for _, t := range ordered {
		if err := migrateOne(ctx, galeHome, t, out); err != nil {
			return err
		}
	}
	reportSourceOnly(out, scan.sourceOnly)
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
	out.Info("Rebuild each with `gale install --build <pkg>`, which " +
		"costs a full source build per package, then run gale migrate " +
		"again.")
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
// Two shapes, decided by where the bytes actually sit. A candidate in
// its canonical directory is superseded in place, and the installer's
// ReplaceGuard is what stands between the fetch and the commit. A
// pre-revision candidate lives in a BARE directory the installer
// never touches: the canonical destination is absent, so the install
// is an ordinary one, no guard fires, and the old directory must be
// relocated afterwards by this command.
func migrateOne(
	ctx *cmdContext, galeHome string, t migrateTarget, out *output.Output,
) error {
	name, full := t.name, t.version
	canonical := filepath.Join(ctx.StoreRoot, name, full)
	relocating := !sameDir(t.dir, canonical)
	if relocating {
		out.Info(fmt.Sprintf(
			"Migrating %s@%s from %s, which predates revisions...",
			name, full, t.dir,
		))
	} else {
		out.Info(fmt.Sprintf("Migrating unprovenanced %s@%s...", name, full))
	}

	prev := ctx.Installer.ReplaceGuard
	ctx.Installer.ReplaceGuard = func(rep installer.Replacement) error {
		return checkMigrateCommit(galeHome, ctx.StoreRoot, t, rep)
	}
	defer func() { ctx.Installer.ReplaceGuard = prev }()

	res, err := ctx.Installer.Reinstall(t.recipe)
	if err != nil {
		return fmt.Errorf("migrating %s@%s: %w", name, full, err)
	}
	if !relocating {
		return nil
	}
	return relocateBareDir(ctx, galeHome, t, res, out)
}

// relocateBareDir moves a pre-revision install into the canonical
// layout, after the canonical artifact has been installed and
// verified.
//
// This is the half no per-scope command may perform, and §13 says
// why: other scopes' generations link the bare path, so moving the
// identity without repairing their symlinks would break scopes that
// never ran anything. Machine-wide is the unit precisely because the
// repair has to cover every scope at once.
//
// Three steps in an order chosen so a failure destroys nothing. The
// commit checks the installer's guard never got to run come first,
// against the committed directory. Then every scope is moved off the
// bare path. The bare directory is removed last, and only after the
// closure walk proves nobody still reaches it.
func relocateBareDir(
	ctx *cmdContext, galeHome string, t migrateTarget,
	res *installer.InstallResult, out *output.Output,
) error {
	if err := checkRelocateCommit(galeHome, ctx.StoreRoot, t, res); err != nil {
		return err
	}
	scopes, err := projects.Scopes(galeHome)
	if err != nil {
		return err
	}
	for _, s := range scopes {
		if err := regenerateScope(s, ctx.StoreRoot, out); err != nil {
			return err
		}
	}
	return removeRelocatedDir(scopes, ctx.StoreRoot, t, out)
}

// checkRelocateCommit re-establishes, for a relocation, everything
// checkMigrateCommit establishes inside the ReplaceGuard.
//
// The guard cannot run here: it fires only when a staged artifact
// supersedes an occupied CANONICAL directory, and a pre-revision
// candidate's canonical directory is absent. Without this the one
// case §13 hands to migrate alone would be the one case with no
// revalidation at all.
//
// The committed directory stands in for the staging directory. By
// this point the install has already landed, so what must be proved
// is that the bytes now at the canonical path are the ones every
// scope was asked about, before the bare directory is destroyed.
func checkRelocateCommit(
	galeHome, storeRoot string, t migrateTarget, res *installer.InstallResult,
) error {
	name, full := t.name, t.version
	canonical := filepath.Join(storeRoot, name, full)
	if _, err := provenance.ReadUnverified(canonical); err != nil {
		return fmt.Errorf(
			"the migrated %s@%s is itself unprovenanced, so removing %s "+
				"would destroy bytes without repairing anything (%v): %w",
			name, full, t.dir, err, errCandidateUnprovenanced,
		)
	}
	b := t.recipe.BinaryForPlatform(runtime.GOOS, runtime.GOARCH)
	if b == nil || res.Method != installer.MethodBinary {
		return fmt.Errorf(
			"%w: %s@%s did not come from the declared binary, and the "+
				"machine was cleared against that artifact alone",
			errMigrateNotBinary, name, full,
		)
	}
	if res.SHA256 != b.SHA256 {
		return fmt.Errorf(
			"%w: %s@%s was cleared at %s and the refetch produced %s",
			errMigrateHashMoved, name, full, b.SHA256, res.SHA256,
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
		selfGaleDir: "",
		name:        name, version: full,
		// The bare directory, which is what this operation destroys.
		targetDir:   t.dir,
		wantSHA:     b.SHA256,
		platform:    currentPlatform(),
		machineWide: true,
	})
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

// removeRelocatedDir deletes the pre-revision directory, but only
// after proving no scope reaches it.
//
// Under the generation lock, and the proof is taken inside it. That
// lock is what generation.Build takes, so holding it means no scope
// can acquire a reference between the walk that found none and the
// removal that relies on it.
//
// Rebuilding a generation moves the ROOTS that resolved to the bare
// directory. It does not move a transitive dependency, which a
// dependent reaches through its own recorded closure rather than
// through any symlink, so the walk can still find the directory
// referenced after every rebuild succeeded. That is not a failure of
// the relocation: the canonical directory is installed and every
// scope's roots point at it. It means one directory outlives the
// pass, and saying so is better than deleting bytes a dependent
// resolves at runtime.
func removeRelocatedDir(
	scopes []projects.Scope, storeRoot string, t migrateTarget,
	out *output.Output,
) error {
	lockPath := filepath.Join(filepath.Dir(storeRoot), "generation.lock")
	return filelock.With(lockPath, func() error {
		target := canonicalStoreDir(t.dir)
		for _, s := range scopes {
			roots, err := generation.AuthoritativeGenerationDirs(
				s.GaleDir, storeRoot,
			)
			if err != nil {
				return fmt.Errorf(
					"reading the active generation of %s before removing "+
						"%s: %w", s.Label, t.dir, err,
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
				out.Warn(fmt.Sprintf(
					"%v: %s still reaches %s, so it was left in place",
					errBareDirStillReferenced, s.Label, t.dir,
				))
				return nil
			}
		}
		if err := os.RemoveAll(t.dir); err != nil {
			return fmt.Errorf("removing %s: %w", t.dir, err)
		}
		out.Success(fmt.Sprintf("  removed %s", t.dir))
		return nil
	})
}
