package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
)

// runLockRefresh is `gale lock --refresh [pkg...]`: the same
// regeneration as plain `gale lock`, with permission to replace a
// store directory that carries no provenance at all.
//
// It shares runLock's body rather than duplicating it. The only
// difference between the two is what they do with one classification —
// an occupied, unprovenanced canonical directory — and everything else
// about resolving, verifying, minting and writing is identical.
//
// Named packages narrow the permission, never the lock: every
// declared root is still resolved and written, and only the named
// ones may be replaced. Refreshing is destructive, so a user who
// names one package must not have the rest replaced silently.
func runLockRefresh(
	ctx *cmdContext, target string, only []string, out *output.Output,
) error {
	ctx.refresh = true
	if len(only) > 0 {
		ctx.refreshOnly = make(map[string]bool, len(only))
		for _, name := range only {
			ctx.refreshOnly[name] = true
		}
	}
	return runLock(ctx, target, out)
}

// errUndeclaredRefresh reports `--refresh <pkg>` for a package the
// target does not declare. Its own sentinel because the state it
// describes is a typed command line, not a store or lock condition,
// and because every other refusal on this path also names packages.
var errUndeclaredRefresh = errors.New("package not declared here")

// errLockArgsNeedRefresh reports package names given to plain
// `gale lock`, which regenerates every declared root and has
// nothing to do with a subset.
var errLockArgsNeedRefresh = errors.New(
	"package names mean something only with --refresh",
)

// checkLockArgs is the argument rule cobra's Args hook cannot
// express, since that hook does not see flags. Separate from the
// RunE body so the rule is reachable by a test that does not have
// to drive the whole command.
func checkLockArgs(args []string, refresh bool) error {
	if len(args) == 0 || refresh {
		return nil
	}
	return fmt.Errorf(
		"gale lock %s: %w; plain `gale lock` regenerates every "+
			"declared root",
		strings.Join(args, " "), errLockArgsNeedRefresh,
	)
}

// errRaceLostToProvenance reports a directory that stopped being
// replaceable between the decision and the lock, because another
// gale provenanced it. Retrying is the whole remedy, which is why it
// is distinct from a conflict: nothing is wrong with the store.
var errRaceLostToProvenance = errors.New(
	"the directory was provenanced by another run",
)

// errCandidateUnprovenanced reports a rebuilt artifact that carries
// no record of its own, so replacing with it would trade one
// unattested directory for another.
var errCandidateUnprovenanced = errors.New(
	"the rebuilt artifact carries no provenance",
)

// stillUnprovenanced re-establishes, inside the commit locks, the
// classification refreshable made outside them.
//
// The four answers are four different situations and must not
// collapse into one. Only a VALID record means another run got
// there first, which is a race the user retries. A record that
// exists and does not validate is an integrity failure exactly as
// it is in lockRoot, and reporting it as a lost race would tell the
// user to retry into the same corrupt state forever. Anything else,
// a permission problem or an I/O error, is returned as itself,
// because reading it as either would decide the fate of a directory
// on the strength of a file that could not be opened.
func stillUnprovenanced(dir, name, full string) error {
	_, err := provenance.ReadUnverified(dir)
	switch {
	case errors.Is(err, provenance.ErrAbsent):
		return nil
	case err == nil:
		return fmt.Errorf(
			"%s gained provenance while %s@%s was being rebuilt, so it is "+
				"no longer the unprovenanced directory --refresh may "+
				"replace: %w",
			dir, name, full, errRaceLostToProvenance,
		)
	case errors.Is(err, provenance.ErrInvalid):
		return fmt.Errorf("refreshing %s@%s: %w", name, full, err)
	default:
		return fmt.Errorf(
			"reading provenance for %s@%s: %w", name, full, err,
		)
	}
}

// checkRefreshNames refuses a named package the target does not
// declare, before anything is resolved or replaced.
//
// The list of declared names is the whole point of the error: the
// realistic mistake is a misspelling or the wrong scope, and both
// look identical to a user who is told only that the name is
// unknown.
func checkRefreshNames(ctx *cmdContext, declared map[string]string) error {
	unknown := make([]string, 0, len(ctx.refreshOnly))
	for name := range ctx.refreshOnly {
		if _, ok := declared[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	slices.Sort(unknown)
	return fmt.Errorf(
		"gale lock --refresh %s: %w; this target declares %s",
		strings.Join(unknown, ", "), errUndeclaredRefresh,
		strings.Join(slices.Sorted(maps.Keys(declared)), ", "),
	)
}

// checkDependents refuses a replacement that would invalidate a
// record the initiating scope actively loads, BEFORE any bytes are
// destroyed.
//
// Only the initiating scope is walked. Every other scope's reference
// is checkReplaceable's business, and it refuses there for the wider
// reason: it cannot tell which bytes that scope needs at all. Migrate
// drops that veto and therefore walks every scope instead; the shared
// mechanism is checkDependentsIn.
//
// The target is the canonical directory. `--refresh` replaces nothing
// else: refreshable declines a pre-revision bare directory, whose
// relocation is machine-wide migrate's business.
func checkDependents(ctx *cmdContext, name, full string) error {
	return checkDependentsIn(dependentQuery{
		galeDir:   ctx.GaleDir,
		label:     "this scope",
		storeRoot: ctx.StoreRoot,
		id:        lockgraph.Key(name, full),
		targetDir: filepath.Join(ctx.StoreRoot, name, full),
		remedy:    postMigrate,
	})
}

// replaceUnprovenanced fetches the locked identity again and puts the
// newly verified result where the unprovenanced directory was.
//
// Refetching is the whole point, and design §13 is explicit about why
// a cheaper approach is not honest: proving the newly fetched artifact
// says nothing about whether the bytes already on disk came from it.
// Writing provenance beside an untouched directory would be the
// rejected unverified marker under another name, so the directory is
// replaced or nothing happens.
//
// Reinstall, never remove-then-install. Reinstall builds into a
// sibling staging dir under the per-package lock and swaps it in
// under the store-gen lock, so the previous directory survives a
// failed fetch, a refused veto, and an interrupted run. Deleting
// first would destroy bytes on the strength of a prediction about
// what would replace them, and would leave the identity absent when
// the prediction was wrong.
//
// The veto rides on the installer's ReplaceGuard rather than running
// here, because here it could only be told what the recipe declares.
// An unlocked install falls back from a failed binary fetch to a
// source build, so the artifact that lands may be one this function
// never named. The guard sees what is actually about to be
// committed, inside the same lock that commits it.
func replaceUnprovenanced(
	ctx *cmdContext, r *recipe.Recipe, out *output.Output,
) error {
	name, full := r.Package.Name, r.Package.Full()
	// Before the fetch as well as inside the commit lock. The answer
	// CAN change in between, which is exactly why the locked recheck
	// exists; this call is the cheap one, so a conflict already
	// visible costs the user no build.
	if err := checkDependents(ctx, name, full); err != nil {
		return err
	}
	out.Info(fmt.Sprintf("Refreshing unprovenanced %s@%s...", name, full))

	prev := ctx.Installer.ReplaceGuard
	ctx.Installer.ReplaceGuard = func(rep installer.Replacement) error {
		// Re-established here, not carried from refreshable's answer.
		// That answer was formed before the per-package lock existed,
		// and a concurrent gale can provenance the directory in
		// between; acting on the stale answer would destroy the very
		// record §13 says must survive.
		if err := stillUnprovenanced(rep.CanonicalDir, name, full); err != nil {
			return err
		}
		// And so is the dependent scan, for the same reason and one
		// more. The initiating scope is exempt from checkReplaceable's
		// cross-scope veto, so nothing else watches it: a generation
		// rebuild or a concurrent install in this very scope can make a
		// dependent active, or provenance one that already was, while
		// the candidate is being rebuilt.
		if err := checkDependents(ctx, name, full); err != nil {
			return err
		}
		// The candidate must itself be attested. recordProvenance
		// commits an artifact with NO record when its closure cannot
		// be attested, which is right for an ordinary install and
		// useless here: trading an unprovenanced directory for
		// another unprovenanced directory destroys bytes and repairs
		// nothing, while the run goes on to report success.
		if _, err := provenance.ReadUnverified(rep.StagingDir); err != nil {
			return fmt.Errorf(
				"the rebuilt %s@%s is itself unprovenanced, so replacing "+
					"would destroy bytes without repairing anything "+
					"(%v): %w",
				name, full, err, errCandidateUnprovenanced,
			)
		}
		return checkReplaceable(replaceQuery{
			// filepath.Dir(storeRoot) is the global gale dir at either
			// scope, since the store is shared (see genLockPath).
			galeHome:    filepath.Dir(ctx.StoreRoot),
			storeRoot:   ctx.StoreRoot,
			selfGaleDir: ctx.GaleDir,
			name:        name,
			version:     full,
			targetDir:   rep.CanonicalDir,
			wantSHA:     rep.Result.SHA256,
			platform:    currentPlatform(),
		})
	}
	defer func() { ctx.Installer.ReplaceGuard = prev }()

	if _, err := ctx.Installer.Reinstall(context.Background(), r); err != nil {
		return fmt.Errorf("refreshing %s@%s: %w", name, full, err)
	}
	return nil
}

// refreshable reports whether the CANONICAL store directory is
// occupied and carries no provenance at all, which is the one state
// design §13 permits one scope to replace.
//
// Canonical exactly, never the store's resolution of the identity.
// Resolution falls a "<v>-1" request back to a pre-revision bare
// "<v>" directory, and that directory is not this command's to
// touch: other scopes' generations link it by that path, so moving
// the identity into the canonical directory and deleting the bare
// one would leave their symlinks dangling, with no per-scope command
// able to repair them. Relocating a pre-revision install is
// machine-wide `gale migrate`'s business, where every scope is
// enumerated before anything moves.
//
// Every other state is left to lockRoot's ordinary classification: an
// absent directory installs, a provenanced one is compared
// against the recipe, and a record that exists and does not validate
// is an integrity failure whose evidence replacement would destroy.
//
// The directory is deliberately not returned. Nothing acts on the
// path this decision was made from: the replacement resolves it
// again inside the lock that commits it, so a caller holding a stale
// path cannot aim the destructive step at it.
func refreshable(ctx *cmdContext, r *recipe.Recipe) bool {
	name, full := r.Package.Name, r.Package.Full()
	if ctx.refreshOnly != nil && !ctx.refreshOnly[name] {
		return false
	}
	if err := store.CheckIdentity(name, full); err != nil {
		return false
	}
	dir := filepath.Join(ctx.StoreRoot, name, full)
	// Occupied first. ReadUnverified reports ErrAbsent for a
	// directory that does not exist at all, and that case is an
	// ordinary install rather than a replacement: routing it here
	// would force a rebuild of something lockRoot installs once.
	if _, err := os.Lstat(dir); err != nil {
		return false
	}
	_, err := provenance.ReadUnverified(dir)
	return errors.Is(err, provenance.ErrAbsent)
}
