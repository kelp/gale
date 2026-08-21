package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/recipe"
	"github.com/spf13/cobra"
)

var (
	installGlobal  bool
	installProject bool
	installRecipes string
	installRecipe  string
	installPath    string
	installGit     bool
	installBuild   bool
	installHost    string
)

var installCmd = &cobra.Command{
	Use:   "install <package>[@version]",
	Short: "Install a package",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateScopeFlags(installGlobal, installProject); err != nil {
			return err
		}

		name, version, err := parsePackageArg(args[0])
		if err != nil {
			return err
		}
		out := newCmdOutput(cmd)

		// --recipe (singular) names a specific file; an @version
		// pin would be silently ignored if we let it through.
		// --recipes (plural) is a local registry directory — it
		// MUST accept @version (resolved by ResolveVersionedRecipe
		// against the local recipes below). See finding F-5.3.
		if installRecipe != "" && version != "" {
			return fmt.Errorf("cannot specify @version with --recipe; " +
				"the file already pins the version — omit @version, " +
				"or use --recipes (plural) for a recipes directory")
		}

		// Resolve scope and paths via cmdContext.
		ctx, err := newCmdContext(installRecipes, installGlobal, installProject)
		if err != nil {
			return err
		}
		ctx.Host, err = resolveHostFlag(installHost)
		if err != nil {
			return err
		}
		// A typo'd --host would silently create a new
		// [hosts.<typo>] section at finalize; make that
		// visible up front (gh#108).
		noticeNewHostSection(out, ctx.GalePath, ctx.Host)

		// If --path flag is provided, build from local source.
		if installPath != "" {
			if dryRun {
				out.Info(fmt.Sprintf(
					"install %s (from source)", name,
				))
				return nil
			}
			return installFromLocalSource(ctx, name, installRecipe,
				installPath, out)
		}

		// If --git flag is provided, clone and build from git.
		if installGit {
			if dryRun {
				out.Info(fmt.Sprintf(
					"install %s (from git)", name,
				))
				return nil
			}
			return installFromGit(ctx, name, installRecipe, out)
		}

		// If --recipe flag is provided, install from recipe file.
		if installRecipe != "" {
			if dryRun {
				out.Info(fmt.Sprintf(
					"install %s (from recipe)", name,
				))
				return nil
			}
			return installFromRecipeFile(ctx, installRecipe, out)
		}

		out.Info(fmt.Sprintf("Fetching recipe for %s...", name))

		var r *recipe.Recipe
		switch {
		case version != "" && version != "latest":
			// Specific version requested — resolve through the
			// same chain as every other version-aware command
			// (gh#70): configured taps first, then the versioned
			// registry index. In --recipes mode the registry is
			// nil and the version resolves against the local
			// recipes directory. ResolveVersionedRecipe compares
			// against both bare Version and Full() so explicit
			// revisions like "1.0-1" work too.
			r, err = ctx.ResolveVersionedRecipe(name, version)
			if err != nil {
				return fmt.Errorf("fetching %s@%s: %w",
					name, version, err)
			}
		default:
			r, err = ctx.Resolver(name)
			if err != nil {
				return fmt.Errorf("fetching recipe: %w", err)
			}
		}

		if dryRun {
			out.Info(fmt.Sprintf("install %s@%s",
				r.Package.Name, r.Package.Version))
			return nil
		}

		if installBuild {
			ctx.Installer.SourceOnly = true
		}

		out.Info(fmt.Sprintf("Installing %s@%s...",
			r.Package.Name, r.Package.Version))

		result, err := ctx.Installer.InstallWithFinalize(r, false,
			func(_ *installer.InstallResult) error {
				return ctx.FinalizeRecipeInstall(r)
			})
		if err != nil {
			if errors.Is(err, build.ErrUnsupportedPlatform) {
				out.Warn(fmt.Sprintf("%s does not support %s/%s",
					r.Package.Name, runtime.GOOS, runtime.GOARCH))
				return fmt.Errorf("install failed: %w", err)
			}
			return fmt.Errorf("install failed: %w", err)
		}

		reportResult(out, result, "Installed", "built from source")

		return nil
	},
}

func init() {
	installCmd.Flags().BoolVarP(&installGlobal, "global", "g",
		false, "Install to global config")
	installCmd.Flags().BoolVarP(&installProject, "project", "p",
		false, "Install to project config")
	installCmd.Flags().StringVar(&installRecipes, "recipes", "",
		"Resolve recipes from a local directory instead of the registry")
	installCmd.Flags().StringVar(&installRecipe, "recipe", "",
		"Install from a recipe TOML file")
	installCmd.Flags().StringVar(&installPath, "path", "",
		"Build from a local source directory")
	installCmd.Flags().BoolVar(&installGit, "git", false,
		"Clone and build from git repository")
	installCmd.Flags().BoolVar(&installBuild, "build", false,
		"Build from source (skip prebuilt binary)")
	installCmd.Flags().StringVar(&installHost, "host", "",
		"Write under [hosts.<host>.packages] "+
			"(use 'current' for this machine)")
	rootCmd.AddCommand(installCmd)
}

// resolveScope determines whether to use global config.
// Returns true for global, false for project. When no
// flag is set, defaults to project if project config
// exists in the directory tree, otherwise global.
func resolveScope(global, project bool, cwd string) bool {
	if global {
		return true
	}
	if project {
		return false
	}
	// Auto-detect: project config exists → project scope.
	_, err := projectConfigPath(cwd)
	if err != nil {
		return true // no project config → global
	}
	return false // project config found → project scope
}

func installFromGit(ctx *cmdContext, name, recipePath string, out *output.Output) error {
	// Shallow-copy ctx.Installer, then override Resolver when
	// --recipe is set.
	inst := *ctx.Installer
	if recipePath != "" {
		resolver, err := resolverForRecipe(recipePath)
		if err != nil {
			return err
		}
		inst.Resolver = resolver
	}

	// Resolve recipe.
	var r *recipe.Recipe
	if recipePath != "" {
		parsed, err := loadRecipeFile(recipePath, true)
		if err != nil {
			return err
		}
		r = parsed
	} else {
		fetched, err := inst.Resolver(name)
		if err != nil {
			return fmt.Errorf("fetching recipe: %w", err)
		}
		r = fetched
	}

	if r.Source.Repo == "" {
		return fmt.Errorf(
			"recipe for %s has no source.repo — cannot build from git", name,
		)
	}

	out.Info(fmt.Sprintf("Installing %s from git (%s)...",
		r.Package.Name, r.Source.Repo))

	result, err := (&inst).InstallGitWithFinalize(r, func(res *installer.InstallResult) error {
		// Git installs produce a dev version derived from the
		// repo state; sync it onto the recipe so Full() emits
		// the matching <version>-<revision> string.
		r.Package.Version = res.Version
		return ctx.FinalizeRecipeInstall(r)
	})
	if err != nil {
		return fmt.Errorf("install failed: %w", err)
	}

	reportResult(out, result, "Installed", "built from git")

	return nil
}

func installFromLocalSource(ctx *cmdContext, name, recipePath, sourceDir string, out *output.Output) error {
	// Resolve source directory to absolute path.
	absSource, err := filepath.Abs(sourceDir)
	if err != nil {
		return fmt.Errorf("resolving source path: %w", err)
	}

	// Resolve the recipe file.
	resolvedRecipe, err := resolveRecipePath(name, recipePath, absSource)
	if err != nil {
		return err
	}

	r, err := loadRecipeFile(resolvedRecipe, true)
	if err != nil {
		return err
	}

	// Override version with semver dev version from git.
	version, err := gitDevVersion(absSource)
	if err != nil {
		return fmt.Errorf("detecting version: %w", err)
	}
	r.Package.Version = version

	// Shallow-copy ctx.Installer, then override Resolver for
	// local recipe resolution.
	inst := *ctx.Installer
	resolver, err := resolverForRecipe(resolvedRecipe)
	if err != nil {
		return err
	}
	inst.Resolver = resolver
	// On the copy, not on ctx.Installer: the guard is this command's
	// alone, and a copy needs no set-and-restore that a later error
	// path could skip. The version above is derived from the source
	// content, so an occupied canonical dir is already skipped as a
	// cache hit; this catches the case where that identity failed to
	// tell two builds apart and a rebuild is about to land on bytes
	// a generation still reaches (gh#183).
	inst.ReplaceGuard = localSourceReplaceGuard(ctx, r)

	out.Info(fmt.Sprintf("Installing %s@%s from local source...",
		r.Package.Name, r.Package.Version))

	result, err := (&inst).InstallLocalWithFinalize(r, absSource,
		func(_ *installer.InstallResult) error {
			return ctx.FinalizeRecipeInstall(r)
		})
	if err != nil {
		return fmt.Errorf("install failed: %w", err)
	}

	reportResult(out, result, "Installed", "built from local source")

	return nil
}

// errGenerationReferenced reports a store directory an existing
// generation still resolves through.
var errGenerationReferenced = errors.New(
	"a generation still references this store directory",
)

// localSourceReplaceGuard refuses to overwrite a store directory
// that any generation links, so design.md's promise — a committed
// store entry is byte-stable for as long as a generation references
// it — is enforced rather than merely documented (gh#183).
//
// EVERY generation, not just the active one. `gale rollback` selects
// an old generation by number and expects the environment that
// generation described; rewriting the bytes under it turns a
// rollback into a silent execution of a tree nobody asked for.
// AuthoritativeGenerationDirs would see only `current`.
//
// Wired on the local-source path alone. A default guard on
// newCmdContext would refuse an ordinary stale reinstall — a
// revision bump reinstalls the same identity on purpose — and sync
// would stop converging.
func localSourceReplaceGuard(
	ctx *cmdContext, r *recipe.Recipe,
) func(installer.Replacement) error {
	name, full := r.Package.Name, r.Package.Full()
	target := filepath.Join(ctx.StoreRoot, name, full)
	return func(rep installer.Replacement) error {
		// Dependencies installed underneath this build share the
		// installer and so share the guard. This answers for the
		// package the command named, and for nothing else.
		if rep.CanonicalDir != target {
			return nil
		}
		gens, err := generation.List(ctx.GaleDir, ctx.StoreRoot)
		if err != nil {
			return fmt.Errorf("listing generations: %w", err)
		}
		for _, g := range gens {
			if g.Packages[name] != full {
				continue
			}
			return fmt.Errorf(
				"%s@%s is linked by generation %d: %w",
				name, full, g.Number, errGenerationReferenced,
			)
		}
		return nil
	}
}

// gitDevVersion returns a semver-compliant version string
// for the given git directory. Uses git describe to find the
// nearest tag and formats as:
//   - "0.2.0" when exactly on tag v0.2.0
//   - "0.2.0-dev.7+5395b8f" when 7 commits ahead
//   - "0.0.0-dev+5395b8f" when no tags exist
//
// A dirty working tree adds a digest of the uncommitted content, so
// two builds of two different trees do not claim one store
// directory (gh#183).
func gitDevVersion(dir string) (string, error) {
	cmd := exec.Command("git", "describe",
		"--tags", "--always")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git describe in %s: %w", dir, err)
	}
	dirt, err := dirtDigest(dir)
	if err != nil {
		return "", err
	}
	return devVersionWithDirt(strings.TrimSpace(string(out)), dirt), nil
}

// dirtLen is how much of the digest the version carries. Twelve hex
// characters read like the abbreviated hashes beside them, and the
// digest names one working tree on one machine rather than
// addressing content globally.
const dirtLen = 12

// dirtDigest fingerprints everything in dir that HEAD does not
// describe. A clean tree returns "".
//
// Two sources, because git reports uncommitted work in two places:
// the diff against HEAD covers tracked files (staged and unstaged
// alike, and --binary so a changed binary file is more than the
// words "Binary files differ"), and ls-files covers untracked ones,
// whose content the diff never mentions.
//
// --exclude-standard is load-bearing. Without it every in-tree build
// output — target/, node_modules/, a compiled binary — feeds the
// digest, and rebuilding one project twice mints two store
// directories because the first build changed the tree it was built
// from. With it, gitignored output is invisible and an unchanged
// tree keeps one identity.
//
// Reads only: no index refresh, no writes, no network.
func dirtDigest(dir string) (string, error) {
	status, err := gitOutput(dir, "status", "--porcelain=v1", "-z",
		"--untracked-files=all")
	if err != nil {
		return "", err
	}
	if len(status) == 0 {
		return "", nil
	}

	h := sha256.New()
	// The status itself, so a rename or a deletion registers even
	// when neither side contributes bytes below.
	h.Write(status)

	diff, err := gitOutput(dir, "diff", "--binary", "--no-ext-diff", "HEAD")
	if err != nil {
		return "", err
	}
	h.Write(diff)

	untracked, err := gitOutput(dir, "ls-files", "-o",
		"--exclude-standard", "-z")
	if err != nil {
		return "", err
	}
	// git sorts its own output, so the walk order — and the digest —
	// is a function of the tree rather than of the filesystem.
	for _, rel := range strings.Split(string(untracked), "\x00") {
		if rel == "" {
			continue
		}
		h.Write([]byte(rel))
		if err := hashPathContent(h, filepath.Join(dir, rel)); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:dirtLen], nil
}

// hashPathContent folds one untracked entry's content into h. A
// symlink contributes its target text rather than the bytes it
// points at, which is both what git records and what keeps a
// dangling link from failing the digest. Anything that is neither a
// regular file nor a symlink contributes nothing but its path.
func hashPathContent(h io.Writer, path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Raced with a build or an editor between the listing and
			// the read. The path is already in the digest.
			return nil
		}
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return fmt.Errorf("read link %s: %w", path, err)
		}
		fmt.Fprint(h, target)
		return nil
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}

// gitOutput runs one read-only git command in dir and returns its
// stdout. Stderr is folded into the error so a failure names its own
// cause instead of an exit status.
func gitOutput(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		return nil, fmt.Errorf("git %s in %s: %w: %s",
			strings.Join(args, " "), dir, err, msg)
	}
	return nil, fmt.Errorf("git %s in %s: %w",
		strings.Join(args, " "), dir, err)
}

// devVersionWithDirt spells a local build's version, carrying the
// working tree's digest when there is one. An empty dirt reproduces
// formatDevVersion exactly, so a clean checkout keeps the identity
// it has always had and tagged builds do not churn the store.
//
// The digest rides in the semver BUILD-METADATA segment, never in a
// "-<hex>" suffix. lockfile.VersionMatches, version.SplitRevision
// and store.HasNumericRevisionSuffix all read a trailing "-<digits>"
// as a Debian revision, so an all-digit digest after a dash would
// parse as revision 12345678. "+" is already legal in store paths —
// "0.0.0-dev+5395b8f" ships today.
//
//	("v0.2.0", "")             → "0.2.0"
//	("v0.2.0", "abc…")         → "0.2.0+dirty.abc…"
//	("v0.2.0-7-g5395b8f", "…") → "0.2.0-dev.7+5395b8f.dirty.…"
func devVersionWithDirt(describe, dirt string) string {
	v := formatDevVersion(describe)
	if dirt == "" {
		return v
	}
	if strings.Contains(v, "+") {
		return v + ".dirty." + dirt
	}
	return v + "+dirty." + dirt
}

// formatDevVersion converts git describe output to semver.
//
//	"v0.2.0"                → "0.2.0"
//	"v0.2.0-7-g5395b8f"     → "0.2.0-dev.7+5395b8f"
//	"v1.0.0-rc1"            → "1.0.0-rc1"
//	"v1.0.0-rc1-3-gabcdef0" → "1.0.0-rc1-dev.3+abcdef0"
//	"5395b8f"               → "0.0.0-dev+5395b8f"
func formatDevVersion(describe string) string {
	// No tags: bare hash.
	if !strings.Contains(describe, ".") {
		return "0.0.0-dev+" + describe
	}

	// Strip leading "v".
	describe = strings.TrimPrefix(describe, "v")

	// When ahead of a tag, git describe appends -<N>-g<hex>.
	// Parse from the right to handle pre-release tags like
	// "1.0.0-rc1-3-gabcdef0".
	lastDash := strings.LastIndex(describe, "-")
	if lastDash < 0 {
		// Exactly on a tag: "0.2.0".
		return describe
	}

	suffix := describe[lastDash+1:]
	if !strings.HasPrefix(suffix, "g") {
		// No -g<hash> suffix — on a pre-release tag like
		// "1.0.0-rc1".
		return describe
	}

	// Find the commit count before the hash.
	rest := describe[:lastDash]
	countDash := strings.LastIndex(rest, "-")
	if countDash < 0 {
		// Malformed — treat as tag.
		return describe
	}

	tag := rest[:countDash]
	count := rest[countDash+1:]
	hash := strings.TrimPrefix(suffix, "g")
	return tag + "-dev." + count + "+" + hash
}

// resolveRecipePath finds the recipe TOML file. If recipePath
// is provided, uses it directly. Otherwise, checks for a
// sibling gale-recipes directory next to sourceDir.
func resolveRecipePath(name, recipePath, sourceDir string) (string, error) {
	if recipePath != "" {
		return recipePath, nil
	}

	letter := string(name[0])
	sibling := filepath.Join(filepath.Dir(sourceDir), "gale-recipes")
	candidate := filepath.Join(sibling, "recipes", letter, name+".toml")
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}

	return "", fmt.Errorf(
		"no recipe found for %q — use --recipe to specify a recipe file", name,
	)
}

// resolverForRecipe returns a RecipeResolver for the given
// recipe file path. If the recipe is inside a letter-bucketed
// recipes repo, uses recipeFileResolver for local dep
// resolution. Otherwise falls back to the registry.
func resolverForRecipe(recipePath string) (installer.RecipeResolver, error) {
	if detectRecipesRepo(recipePath) != "" {
		return recipeFileResolver(recipePath), nil
	}
	reg, err := newRegistry()
	if err != nil {
		return nil, err
	}
	return reg.FetchRecipe, nil
}

func installFromRecipeFile(ctx *cmdContext, recipePath string, out *output.Output) error {
	r, err := loadRecipeFile(recipePath, false)
	if err != nil {
		return err
	}

	// Shallow-copy ctx.Installer, then override Resolver for
	// local recipe resolution.
	inst := *ctx.Installer
	resolver, err := resolverForRecipe(recipePath)
	if err != nil {
		return err
	}
	inst.Resolver = resolver

	out.Info(fmt.Sprintf("Installing %s@%s...",
		r.Package.Name, r.Package.Version))

	// InstallWithFinalize holds the per-package lock across
	// finalize so a concurrent `gale gc` cannot reap the
	// just-installed package before it lands in gale.toml
	// (gh#69 — the --recipe path missed the race-0004 fix the
	// registry, --path, and --git paths already received).
	result, err := (&inst).InstallWithFinalize(r, false,
		func(_ *installer.InstallResult) error {
			return ctx.FinalizeRecipeInstall(r)
		})
	if err != nil {
		return fmt.Errorf("install failed: %w", err)
	}

	reportResult(out, result, "Installed", "built from source")

	return nil
}

// parsePackageArg splits "name@version" into name and version.
//
// Strict-parse rules (see finding F-1):
//   - No "@" in arg or "@" at index 0 → (arg, "", nil). Name
//     validation happens elsewhere via registry.ValidName.
//   - Multiple "@" characters → error. Recipe names never
//     contain "@", so a stray second one is always user error.
//   - Empty version segment ("name@", "name@@") → error.
//     Previously fell through to the "latest" branch and
//     silently ignored the user's pin.
//   - Whitespace, control characters, or shell metacharacters
//     in the version segment → error. Tightening here means a
//     malformed pin gets caught before it reaches the registry
//     URL or the store path.
//
// Allowed version characters: letters, digits, ".", "-", "_",
// "+". This matches every existing recipe version we ship
// (semver-ish with optional Debian "-N" revision suffix).
func parsePackageArg(arg string) (string, string, error) {
	i := strings.LastIndex(arg, "@")
	if i <= 0 {
		// Either no "@" (bare name) or arg starts with "@"
		// (caller's name validator handles that case).
		return arg, "", nil
	}
	name := arg[:i]
	version := arg[i+1:]
	if strings.Contains(name, "@") {
		return "", "", fmt.Errorf(
			"invalid argument %q: multiple '@' separators", arg,
		)
	}
	if version == "" {
		return "", "", fmt.Errorf(
			"invalid argument %q: empty version after '@'", arg,
		)
	}
	for _, r := range version {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			return "", "", fmt.Errorf(
				"invalid argument %q: whitespace in version", arg,
			)
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_' || r == '+':
		default:
			return "", "", fmt.Errorf(
				"invalid argument %q: invalid character %q in version",
				arg, r,
			)
		}
	}
	return name, version, nil
}

// resolveConfigPath returns the gale.toml path to write to.
func resolveConfigPath(global bool) (string, error) {
	if global {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("finding home dir: %w", err)
		}
		return filepath.Join(home, ".gale", "gale.toml"), nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting working dir: %w", err)
	}

	path, err := projectConfigPath(cwd)
	if err == nil {
		return path, nil
	}

	return filepath.Join(cwd, "gale.toml"), nil
}
