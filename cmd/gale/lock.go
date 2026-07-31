package main

import (
	"errors"
	"fmt"
	"maps"
	"runtime"
	"slices"
	"strings"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/lockwrite"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
	"github.com/spf13/cobra"
)

var (
	lockGlobal  bool
	lockProject bool
	lockRecipes string
	lockHost    string
)

var lockCmd = &cobra.Command{
	Use:   "lock",
	Short: "Regenerate gale.lock for the packages gale.toml declares",
	Long: "Resolve every package one gale.toml section declares and " +
		"record the verified closure in gale.lock.\n\n" +
		"Plain `gale lock` regenerates [targets.default] from the " +
		"shared [packages] section; --host <selector> regenerates that " +
		"host's target from [hosts.<selector>.packages]. Every other " +
		"target is carried forward, and gale.toml is never written.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateScopeFlags(lockGlobal, lockProject); err != nil {
			return err
		}
		ctx, err := newCmdContext(lockRecipes, lockGlobal, lockProject)
		if err != nil {
			return err
		}
		// resolveHostFlag expands `current` before the target is
		// chosen, so the lock is keyed on the concrete hostname the
		// manifest overlay uses. A target keyed "current" would match
		// no machine and every reader would plan without it.
		return runLock(ctx, resolveHostFlag(lockHost), newCmdOutput(cmd))
	},
}

func init() {
	lockCmd.Flags().BoolVarP(&lockGlobal, "global", "g",
		false, "Lock the global config")
	lockCmd.Flags().BoolVarP(&lockProject, "project", "p",
		false, "Lock the project config")
	lockCmd.Flags().StringVar(&lockRecipes, "recipes", "",
		"Resolve recipes from a local directory instead of the registry")
	lockCmd.Flags().StringVar(&lockHost, "host", "",
		"Lock [hosts.<host>.packages] instead of the shared section "+
			"(use 'current' for this machine)")
	rootCmd.AddCommand(lockCmd)
}

// errNoDeclarations reports a lock target whose manifest section
// declares no packages.
//
// It lives here rather than in internal/lockwrite because it is about
// the request, not the document: the user named a section that is
// empty. The writer's own answer to an empty section is to drop the
// target, which is right for `remove` and wrong for an explicit
// `gale lock` that would then have locked nothing and said so
// nowhere.
var errNoDeclarations = errors.New("no packages declared")

// errUnprovenancedStoreDir reports a canonical store directory that
// is occupied but carries no usable provenance.
//
// It is separate from the absent case because the two admit opposite
// actions: `gale lock` populates an absent directory and must not
// adopt an occupied one, since adopting would assert provenance for
// bytes nothing here verified (design §11, §13).
var errUnprovenancedStoreDir = errors.New("no usable provenance")

// errRecipeDisagrees reports installed provenance the recipe does not
// back: same identity, different bytes.
//
// The canonical directory is occupied and validly provenanced here,
// so §11 allows no replacement and the remedy is a new
// version-revision. Reusing the record regardless would lock bytes
// under an identity the recipe says holds others, which is exactly
// the substitution the lock exists to detect.
var errRecipeDisagrees = errors.New("recipe disagrees with installed provenance")

// runLock regenerates one lock target from the manifest section it
// mirrors, and writes gale.toml never.
//
// The section is read raw, exactly as WriteLock reads it: each target
// roots its own section alone, so a host-merged view would lock the
// shared packages into every overlay target as well.
func runLock(ctx *cmdContext, target string, out *output.Output) error {
	cfg, err := rawGaleConfig(ctx.GalePath)
	if err != nil {
		return err
	}
	declared := declaredForTarget(cfg, target)
	if len(declared) == 0 {
		return noDeclarations(cfg, target, ctx.GalePath)
	}
	roots := make([]*recipe.Recipe, 0, len(declared))
	for _, name := range slices.Sorted(maps.Keys(declared)) {
		r, err := ctx.ResolveVersionedRecipe(name, declared[name])
		if err != nil {
			return fmt.Errorf(
				"resolving a recipe for %s@%s: %w", name, declared[name], err,
			)
		}
		if dryRun {
			out.Info(fmt.Sprintf("lock %s@%s", name, r.Package.Full()))
			continue
		}
		out.Info(fmt.Sprintf("Locking %s@%s...", name, r.Package.Full()))
		if err := lockRoot(ctx, r); err != nil {
			return err
		}
		ctx.noteLockRoot(target, name, r.Package.Full())
		roots = append(roots, r)
	}
	// Returned explicitly rather than left to WriteLock's empty-target
	// no-op: a dry run must not depend on a later function happening to
	// have nothing to do.
	if dryRun {
		return nil
	}
	// `gale lock` is the only writer that mints (§11), so the mints are
	// attached here rather than inside WriteLock, which every writer
	// shares.
	ctx.lockMints, ctx.mintSkips = mintOtherPlatforms(ctx.Resolver, roots)
	if err := ctx.WriteLock(); err != nil {
		return err
	}
	// After the write, for the same reason warnUnlocked is: what these
	// describe is what the new lockfile leaves out, and a failed write
	// leaves the previous one intact, omitting nothing.
	warnSkippedPlatforms(out, ctx.mintSkips)
	return nil
}

// lockRoot makes one declared package lockable: it decides whether
// the installed closure can back the root, and refuses the one state
// that looks close enough to adopt and is not.
//
// The identity is the recipe's canonical version-revision, never a
// store-dir basename: the basename is whatever resolution found,
// while Full() is what a reinstall would write and therefore what the
// lock must name.
func lockRoot(ctx *cmdContext, r *recipe.Recipe) error {
	name, full := r.Package.Name, r.Package.Full()
	if err := store.CheckIdentity(name, full); err != nil {
		return err
	}
	dir, occupied := ctx.Installer.Store.StorePath(name, full)
	if !occupied {
		// Absent, so there is nothing to adopt and nothing to
		// contradict: fetching or building is how this run obtains the
		// provenance the lock is written from. Without it `gale lock`
		// could only describe what happened to be installed, and a
		// package `gale add` just declared could never be locked at all.
		if _, err := ctx.Installer.Install(r); err != nil {
			return fmt.Errorf("installing %s@%s to lock it: %w", name, full, err)
		}
		return nil
	}
	rec, err := provenance.ReadUnverified(dir)
	switch {
	case errors.Is(err, provenance.ErrAbsent):
		// The only state §13's exception covers, and so the only one
		// entitled to be offered a replacement.
		return unprovenanced(dir, name, full, err)
	case errors.Is(err, provenance.ErrInvalid):
		// A record that exists and does not validate is an integrity
		// failure rather than a migration candidate: something wrote
		// bytes that disagree with the format gale produces, and
		// replacing the directory would destroy the evidence of it.
		return fmt.Errorf("locking %s@%s: %w", name, full, err)
	case err != nil:
		// Anything else — a permission problem, an I/O error — is
		// reported as itself. Reading it as unprovenanced would
		// recommend replacing a store directory because a file could
		// not be opened.
		return fmt.Errorf("reading provenance for %s@%s: %w", name, full, err)
	}
	return checkRecipeBacks(r, rec)
}

// checkRecipeBacks holds an installed binary record to what the
// recipe declares for this platform.
//
// Source provenance is reused as-is. A source artifact's hash is the
// output this machine built, which no recipe field describes and
// which §10 says legitimately differs across machines; there is
// nothing to compare it against.
//
// The manifest digest is compared only where the recipe carries one,
// per §3's "declared SHA and manifest digest where the recipe carries
// them". An index published before the field existed declares none,
// and calling that a disagreement would make every such package
// unlockable.
func checkRecipeBacks(r *recipe.Recipe, rec provenance.Record) error {
	if rec.Method != lockgraph.MethodBinary {
		return nil
	}
	name, full := r.Package.Name, r.Package.Full()
	b := r.BinaryForPlatform(runtime.GOOS, runtime.GOARCH)
	if b == nil {
		// Nothing declared is not the same as nothing to check: the
		// installed bytes came from somewhere this recipe no longer
		// names, so the recipe cannot back them either.
		return recipeDisagrees(name, full, "sha256", rec.SHA256, "none")
	}
	if b.SHA256 != rec.SHA256 {
		return recipeDisagrees(name, full, "sha256", rec.SHA256, b.SHA256)
	}
	if b.ManifestDigest != "" && b.ManifestDigest != rec.ManifestDigest {
		return recipeDisagrees(
			name, full, "manifest_digest", rec.ManifestDigest, b.ManifestDigest,
		)
	}
	return nil
}

// recipeDisagrees names both values, because which one is wrong is
// the user's call: an upstream artifact may have been replaced, or
// the recipe re-pinned without a revision bump.
//
// It offers no `--refresh`. That flag replaces a canonical directory
// only where the directory is absent, unreferenced, or unprovenanced,
// and this one is none of those, so naming it would send the user to
// a command that must refuse (§11).
func recipeDisagrees(name, version, field, installed, declared string) error {
	return fmt.Errorf(
		"%s@%s: %w: the installed artifact records %s %q and the recipe "+
			"declares %q; the store directory is provenanced, so the remedy "+
			"is a new version-revision rather than replacing it",
		name, version, errRecipeDisagrees, field, installed, declared,
	)
}

// unprovenanced reports an occupied canonical directory with NO
// provenance record, and names the two commands that may replace it.
//
// Absent only. A record that exists and does not validate is an
// integrity failure and never reaches here: §13's exception covers
// the migration case alone, so widening this helper again would
// offer to destroy the evidence of a malformed record.
//
// `gale lock` deliberately is not one of them. Writing a record
// beside bytes it never fetched would attest a directory on the
// strength of it being in the right place, which is exactly the
// unverified marker §13 rejected; replacement under §11 and §13 is an
// explicit user action.
func unprovenanced(dir, name, version string, cause error) error {
	return fmt.Errorf(
		"%s@%s: %s is occupied but has %w (%w); gale lock records only "+
			"what it verified, so replacing those bytes takes "+
			"`gale lock --refresh %s` or `gale migrate`",
		name, version, dir, errUnprovenancedStoreDir, cause, name,
	)
}

// noDeclarations reports a lock target whose section declares
// nothing, naming the sections that do declare packages.
//
// The list is the whole point of the error. --host takes the
// selector verbatim, so a user whose packages all live under
// overlays needs to see which strings are spelled in the file rather
// than guess at their own hostname.
func noDeclarations(cfg *config.GaleConfig, target, path string) error {
	sections := declaredRemedies(cfg)
	if len(sections) == 0 {
		return fmt.Errorf(
			"%w in %s: %s declares no packages in any section",
			errNoDeclarations, lockwrite.ManifestSection(target), path,
		)
	}
	return fmt.Errorf(
		"%w in %s: %s declares packages in other sections — run: %s",
		errNoDeclarations, lockwrite.ManifestSection(target), path,
		strings.Join(sections, ", "),
	)
}

// declaredRemedies names the command that locks each manifest section
// that declares at least one package, in a stable order.
//
// Commands rather than section names, because the section a user must
// pass and the flag they must pass it with are not the same string:
// shared [packages] is locked by plain `gale lock`, and offering
// --host for it sends the user back into the failure they just hit.
func declaredRemedies(cfg *config.GaleConfig) []string {
	var out []string
	if len(cfg.Packages) > 0 {
		out = append(out, "gale lock")
	}
	for _, host := range slices.Sorted(maps.Keys(cfg.Hosts)) {
		if len(cfg.Hosts[host].Packages) == 0 {
			continue
		}
		flag, ok := hostFlagArg(host)
		if !ok {
			// Unspellable in one line, so the section is named and the
			// user supplies the selector themselves.
			out = append(out, fmt.Sprintf(
				"gale lock --host, with the selector spelled as in %s",
				lockwrite.ManifestSection(host),
			))
			continue
		}
		out = append(out, "gale lock "+flag)
	}
	return out
}
