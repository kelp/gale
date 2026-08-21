package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
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
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateScopeFlags(lockGlobal, lockProject); err != nil {
			return err
		}
		if err := checkLockArgs(args); err != nil {
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
		host, err := resolveHostFlag(lockHost)
		if err != nil {
			return err
		}
		return runLock(ctx, host, newCmdOutput(cmd))
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

// errLockTakesNoPackages reports package names given to gale lock.
// Unprovenanced refetch is gale fetch-adopt, not a lock argument.
var errLockTakesNoPackages = errors.New("gale lock takes no package names")

// checkLockArgs refuses positional package names. Those named the
// deleted --refresh subset; unprovenanced refetch is fetch-adopt.
func checkLockArgs(args []string) error {
	if len(args) == 0 {
		return nil
	}
	return fmt.Errorf(
		"gale lock %s: %w; unprovenanced refetch is gale fetch-adopt",
		strings.Join(args, " "), errLockTakesNoPackages,
	)
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
	roots, err := resolveRoots(ctx, declared)
	if err != nil {
		return err
	}
	if dryRun {
		for _, r := range roots {
			out.Info(fmt.Sprintf(
				"lock %s@%s", r.Package.Name, r.Package.Full(),
			))
		}
		// Returned explicitly rather than left to WriteLock's
		// empty-target no-op: a dry run must not depend on a later
		// function happening to have nothing to do.
		return nil
	}
	for _, r := range roots {
		name := r.Package.Name
		out.Info(fmt.Sprintf("Locking %s@%s...", name, r.Package.Full()))
		if err := lockRoot(ctx, r); err != nil {
			return err
		}
		ctx.noteLockRoot(target, name, r.Package.Full())
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

// resolveRoots resolves every declared package and returns the
// recipes in the order they must be processed: a root that another
// declared root depends on comes first.
//
// Every root is resolved before any is processed, which is what makes
// the order knowable at all, and it also means a misspelled pin costs
// nothing: resolution fails before the first store directory is
// touched.
func resolveRoots(
	ctx *cmdContext, declared map[string]string,
) ([]*recipe.Recipe, error) {
	resolved := make([]*recipe.Recipe, 0, len(declared))
	for _, name := range slices.Sorted(maps.Keys(declared)) {
		r, err := ctx.ResolveVersionedRecipe(name, declared[name])
		if err != nil {
			return nil, fmt.Errorf(
				"resolving a recipe for %s@%s: %w", name, declared[name], err,
			)
		}
		resolved = append(resolved, r)
	}
	return orderRoots(resolved), nil
}

// orderRoots puts a declared root that another declared root depends
// on ahead of the root above it.
//
// Design §5 and §7 between them make the order load-bearing rather
// than cosmetic. Provenance is all-or-nothing, so an artifact whose
// dependency carries no record commits with no record of its own; a
// dependent processed first is therefore rebuilt against an
// unattested dependency and cannot be locked. Alphabetical order
// would decide whether a scope can converge at all. Processing
// bottom-up is also what §5's reverse-dependent rule rests on: a
// dependent must see the dependency's new bytes rather than a digest
// that went stale behind it.
//
// Only declared roots are ordered. A dependency that is not itself
// declared is installed by the root above it, in the installer's own
// topological order, and never appears here.
//
// A cycle among recipes cannot be ordered, so the order it was given
// is returned unchanged and the run proceeds to whichever check
// names the cycle properly. Reordering part of it would be worse
// than not reordering: depth-first traversal emits a cycle's members
// in an order that satisfies nothing, and the whole reason this
// function exists is that the order carries meaning.
func orderRoots(resolved []*recipe.Recipe) []*recipe.Recipe {
	byName := make(map[string]*recipe.Recipe, len(resolved))
	for _, r := range resolved {
		byName[r.Package.Name] = r
	}
	const (
		visiting = 1
		done     = 2
	)
	state := make(map[string]int, len(resolved))
	ordered := make([]*recipe.Recipe, 0, len(resolved))
	cycled := false
	var visit func(r *recipe.Recipe)
	visit = func(r *recipe.Recipe) {
		name := r.Package.Name
		if state[name] == visiting {
			// Reached from inside its own subtree. Distinguished from
			// done because the two mean opposite things: a finished
			// node is already placed, while this one cannot be.
			cycled = true
			return
		}
		if state[name] == done {
			return
		}
		state[name] = visiting
		for _, dep := range declaredDeps(r) {
			if d, ok := byName[dep]; ok {
				visit(d)
			}
		}
		state[name] = done
		ordered = append(ordered, r)
	}
	// resolved arrives alphabetical, so ties break alphabetically and
	// two runs over one manifest process the same order.
	for _, r := range resolved {
		visit(r)
	}
	if cycled {
		return resolved
	}
	return ordered
}

// declaredDeps names every dependency of one recipe that provenance
// can require a record for, in a stable order.
//
// Build dependencies count alongside runtime ones because a source
// artifact's record serializes both (design §5), so a build tool
// without provenance leaves its dependent unattestable exactly as a
// runtime library does. Including them for a binary package orders a
// pair that did not need ordering, which costs nothing.
func declaredDeps(r *recipe.Recipe) []string {
	deps := r.DependenciesForPlatform(runtime.GOOS, runtime.GOARCH)
	all := slices.Concat(deps.Runtime, deps.Build)
	slices.Sort(all)
	return slices.Compact(all)
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
		if _, err := ctx.Installer.Install(context.Background(), r); err != nil {
			return fmt.Errorf("installing %s@%s to lock it: %w", name, full, err)
		}
		return nil
	}
	rec, err := provenance.ReadUnverified(dir)
	switch {
	case errors.Is(err, provenance.ErrAbsent):
		// The only state §13's exception covers, and so the only one
		// entitled to be offered a replacement. Which replacement,
		// though, depends on WHERE the bytes are: a pre-revision
		// install resolves to a bare dir that no per-scope command
		// may relocate.
		return unprovenanced(unprovenancedDir{
			dir:       dir,
			canonical: filepath.Join(ctx.StoreRoot, name, full),
			name:      name,
			version:   full,
			cause:     err,
		})
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
// It offers no replacement command. A provenanced disagreement is
// not the unprovenanced refetch case, so naming fetch-adopt would
// send the user to a command that must refuse.
func recipeDisagrees(name, version, field, installed, declared string) error {
	return fmt.Errorf(
		"%s@%s: %w: the installed artifact records %s %q and the recipe "+
			"declares %q; the store directory is provenanced, so the remedy "+
			"is a new version-revision rather than replacing it",
		name, version, errRecipeDisagrees, field, installed, declared,
	)
}

// unprovenancedDir is one occupied directory with no provenance,
// and where it sits. dir is what the identity resolves to today;
// canonical is where a reinstall would write. They differ exactly
// when the install predates revisions.
type unprovenancedDir struct {
	dir, canonical string
	name, version  string
	cause          error
}

// unprovenanced reports an occupied directory with NO provenance
// record, and names the commands that may replace it.
//
// Absent only. A record that exists and does not validate is an
// integrity failure and never reaches here: §13's exception covers
// the migration case alone, so widening this helper again would
// offer to destroy the evidence of a malformed record.
//
// `gale lock` deliberately is not one of them. Writing a record
// beside bytes it never fetched would attest a directory on the
// strength of it being in the right place, which is exactly the
// unverified marker §13 rejected; replacement is an explicit user
// action (`gale fetch-adopt`, or machine-wide `gale migrate` for a
// pre-revision bare directory).
func unprovenanced(u unprovenancedDir) error {
	remedy := fmt.Sprintf("`gale fetch-adopt` for %s, or `gale migrate`", u.name)
	if u.dir != u.canonical {
		remedy = fmt.Sprintf(
			"`gale migrate`, since %s predates revisions and moving it "+
				"to %s is machine-wide work that one scope cannot do "+
				"safely", u.dir, u.canonical,
		)
	}
	return fmt.Errorf(
		"%s@%s: %s is occupied but has %w (%w); gale lock records only "+
			"what it verified, so replacing those bytes takes %s",
		u.name, u.version, u.dir, errUnprovenancedStoreDir, u.cause, remedy,
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
