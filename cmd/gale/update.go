package main

import (
	"fmt"
	"sort"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/gitutil"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/output"
	ver "github.com/kelp/gale/internal/version"
	"github.com/spf13/cobra"
)

var (
	updateRecipes   string
	updatePath      string
	updateGit       bool
	updateRecipe    string
	updateBuild     bool
	updateNoRefresh bool
	updateNoInstall bool
	updateGlobal    bool
	updateProject   bool
)

var updateCmd = &cobra.Command{
	Use:   "update [package...]",
	Short: "Update packages to the latest version",
	RunE: func(cmd *cobra.Command, args []string) error {
		out := newCmdOutput(cmd)

		if updateGlobal && updateProject {
			return fmt.Errorf("cannot use both --global and --project")
		}

		// --path requires exactly one package name.
		if updatePath != "" && len(args) != 1 {
			return fmt.Errorf(
				"--path requires exactly one package name")
		}

		// --no-install only makes sense for the
		// pin-resolution path. --path and --git both imply
		// building, so combining them is a user error.
		if updateNoInstall && (updatePath != "" || updateGit) {
			return fmt.Errorf(
				"--no-install cannot be combined with --path or --git")
		}

		// Auto-refresh configured taps so a stale local clone
		// doesn't mask an upstream version bump. Skip when
		// --recipes is set (resolver bypasses taps anyway),
		// when --no-refresh is passed, or when GALE_OFFLINE=1.
		if updateRecipes == "" && !tapsOfflineMode(updateNoRefresh) {
			if err := refreshConfiguredTapsDefault(out); err != nil {
				out.Warn(fmt.Sprintf("tap refresh: %v", err))
			}
		}

		// Resolve context for config path. All branches
		// use ctx.GalePath for config writes.
		ctx, err := newCmdContext(updateRecipes, updateGlobal, updateProject)
		if err != nil {
			return err
		}

		if updateBuild {
			ctx.Installer.SourceOnly = true
		}

		// --path: rebuild from local source directory.
		if updatePath != "" {
			if dryRun {
				out.Info(fmt.Sprintf(
					"update %s (from local source)", args[0]))
				return nil
			}
			// Check membership in gale.toml — consistent with normal update path.
			cfg, cfgErr := ctx.LoadConfig()
			if cfgErr != nil {
				return cfgErr
			}
			if _, ok := cfg.Packages[args[0]]; !ok {
				return fmt.Errorf("%s not in gale.toml — use 'gale install --path %s %s' to add it",
					args[0], updatePath, args[0])
			}
			return installFromLocalSource(ctx,
				args[0], updateRecipe, updatePath, out)
		}

		// --git: check remote HEAD, rebuild if changed.
		if updateGit {
			if len(args) != 1 {
				return fmt.Errorf(
					"--git requires exactly one package name")
			}
			if dryRun {
				out.Info(fmt.Sprintf(
					"update %s (from git HEAD)", args[0]))
				return nil
			}
			// Check membership in gale.toml.
			cfg, cfgErr := ctx.LoadConfig()
			if cfgErr != nil {
				return cfgErr
			}
			if _, ok := cfg.Packages[args[0]]; !ok {
				return fmt.Errorf("%s not in gale.toml — use 'gale install --git %s' to add it",
					args[0], args[0])
			}
			return updateFromGit(args[0], ctx, out)
		}

		cfg, err := ctx.LoadConfig()
		if err != nil {
			return err
		}

		// Determine which packages to update.
		// Parse @version from args if present.
		type target struct {
			current string // version in gale.toml
			pinned  string // explicit @version (empty = latest)
		}
		targets := make(map[string]target)

		if len(args) > 0 {
			for _, arg := range args {
				name, ver, err := parsePackageArg(arg)
				if err != nil {
					return err
				}
				current, ok := cfg.Packages[name]
				if !ok {
					out.Warn(fmt.Sprintf(
						"%s not in gale.toml, skipping", name))
					continue
				}
				if cfg.Pinned[name] {
					out.Info(fmt.Sprintf(
						"skipping %s (pinned)", name))
					continue
				}
				targets[name] = target{current, ver}
			}
		} else {
			for name, ver := range cfg.Packages {
				if cfg.Pinned[name] {
					out.Info(fmt.Sprintf(
						"skipping %s (pinned)", name))
					continue
				}
				targets[name] = target{ver, ""}
			}
		}

		if len(targets) == 0 {
			out.Info("No packages to update.")
			return nil
		}

		// Sort target names for deterministic order.
		targetNames := make([]string, 0, len(targets))
		for name := range targets {
			targetNames = append(targetNames, name)
		}
		targetNames = sortedTargetKeys(targetNames)

		var updated, failed int
		for _, name := range targetNames {
			t := targets[name]
			var newVersion string

			if t.pinned != "" {
				// Explicit @version — fetch that version.
				newVersion = t.pinned
			} else {
				// No @version — check latest from registry.
				r, err := ctx.Resolver(name)
				if err != nil {
					out.Warn(fmt.Sprintf(
						"Skipping %s: %v", name, err))
					continue
				}
				// Compare via Package.Full() so a revision bump
				// (e.g. recipe revision 1 → 2 at an unchanged
				// upstream version) surfaces as outdated. Raw
				// Package.Version drops the revision entirely
				// and makes `update` disagree with `outdated`.
				candidate := r.Package.Full()
				target, skip := updateAction(
					candidate, t.current,
					ctx.Installer.Store.IsInstalled(
						name, t.current))
				if skip {
					out.Info(fmt.Sprintf(
						"%s@%s is up to date",
						name, t.current))
					continue
				}
				newVersion = target
			}

			// Fetch the recipe for the target version.
			r, err := ctx.ResolveVersionedRecipe(
				name, newVersion)
			if err != nil {
				out.Warn(fmt.Sprintf(
					"Skipping %s: %v", name, err))
				continue
			}

			if dryRun {
				out.Info(fmt.Sprintf("update %s %s → %s",
					name, t.current, r.Package.Full()))
				updated++
				continue
			}

			// --no-install: bump the gale.toml pin and stop.
			// The user runs `gale sync` to actually build and
			// install the new version; the lockfile stays
			// untouched because it tracks installed artifacts.
			if updateNoInstall {
				if err := config.UpsertPackage(
					ctx.GalePath, config.CurrentHost(),
					name, r.Package.Version); err != nil {
					return fmt.Errorf("updating %s pin: %w",
						name, err)
				}
				out.Success(fmt.Sprintf(
					"Bumped %s %s → %s (run 'gale sync' to install)",
					name, t.current, r.Package.Full()))
				updated++
				continue
			}

			out.Info(fmt.Sprintf("Updating %s %s → %s...",
				name, t.current, r.Package.Full()))

			result, err := ctx.Installer.InstallWithFinalize(r, false,
				func(res *installer.InstallResult) error {
					return ctx.WriteConfigAndLockForRecipe(r, res.SHA256)
				})
			if err != nil {
				out.Warn(fmt.Sprintf(
					"Failed to update %s: %v", name, err))
				failed++
				continue
			}

			reportResult(out, result, "Updated", "built from source")
			updated++
		}

		// Skip the generation rebuild under --no-install: the
		// new pin points at a store dir that does not exist
		// yet, which would cause rebuild to fail. The
		// follow-up `gale sync` installs and rebuilds.
		if err := finishUpdate(dryRun || updateNoInstall, failed, updated, ctx.RebuildGeneration); err != nil {
			return err
		}
		if updated == 0 {
			out.Success("Everything is up to date.")
		} else if updateNoInstall {
			out.Success(fmt.Sprintf(
				"Bumped %d pin(s) — run 'gale sync' to install",
				updated))
		} else {
			out.Success(fmt.Sprintf(
				"Updated %d package(s)", updated))
		}
		return nil
	},
}

func finishUpdate(dryRun bool, failed int, updated int, rebuild func() error) error {
	if dryRun {
		return nil
	}
	if updated == 0 && failed == 0 {
		return nil // nothing changed — skip rebuild
	}
	rebuildErr := rebuild()
	if failed > 0 {
		if rebuildErr != nil {
			return fmt.Errorf("%d package(s) could not be updated; rebuild: %w",
				failed, rebuildErr)
		}
		return fmt.Errorf("%d package(s) could not be updated", failed)
	}
	return rebuildErr
}

// updateFromGit checks if the remote HEAD changed, and
// rebuilds from git if so.
func updateFromGit(name string, ctx *cmdContext, out *output.Output) error {
	r, err := ctx.Resolver(name)
	if err != nil {
		return fmt.Errorf("fetching recipe: %w", err)
	}
	if r.Source.Repo == "" {
		return fmt.Errorf(
			"recipe for %s has no source.repo", name)
	}

	// Check remote HEAD.
	remoteHash, err := gitutil.RemoteHead(
		r.Source.Repo, r.Source.Branch)
	if err != nil {
		return fmt.Errorf("checking remote: %w", err)
	}

	// Compare to installed version.
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}

	installed := cfg.Packages[name]
	if isGitHash(installed) && installed == remoteHash {
		out.Success(fmt.Sprintf(
			"%s@%s is up to date", name, remoteHash))
		return nil
	}

	out.Info(fmt.Sprintf("Updating %s to %s...",
		name, remoteHash))
	return installFromGit(ctx, name, updateRecipe, out)
}

// isGitHash returns true if s looks like a git short hash
// (7+ hex characters with no dots or non-hex characters).
// This distinguishes git hashes from semver versions like
// "1.7.1" when comparing installed vs remote versions.
func isGitHash(s string) bool {
	if len(s) < 7 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') &&
			(c < 'a' || c > 'f') &&
			(c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// isNewerVersion reports whether candidate is strictly newer
// than current. Delegates to internal/version so update and
// outdated share one ordering — including gale revision
// semantics (numeric `-<N>` is newer than bare).
func isNewerVersion(candidate, current string) bool {
	return ver.IsNewer(candidate, current)
}

// updateAction returns the version to install and whether
// the update should be skipped. When the registry version
// matches the current version AND the package exists in the
// store, skip is true. When the store entry is missing,
// skip is false and version is the current version
// (reinstall). When the registry is newer, skip is false
// and version is the new version.
func updateAction(
	candidate, current string,
	inStore bool,
) (version string, skip bool) {
	newer := isNewerVersion(candidate, current)
	if !newer && inStore {
		return current, true
	}
	if newer {
		return candidate, false
	}
	return current, false
}

// sortedTargetKeys returns a sorted copy of the input
// slice. Used to ensure deterministic iteration order
// over update targets.
func sortedTargetKeys(keys []string) []string {
	sorted := make([]string, len(keys))
	copy(sorted, keys)
	sort.Strings(sorted)
	return sorted
}

func init() {
	updateCmd.Flags().StringVar(&updateRecipes, "recipes", "",
		"Resolve recipes from a local directory instead of the registry "+
			"(bare --recipes uses ../gale-recipes/)")
	updateCmd.Flags().Lookup("recipes").NoOptDefVal = "auto"
	updateCmd.Flags().StringVar(&updatePath, "path", "",
		"Build from a local source directory")
	updateCmd.Flags().BoolVar(&updateGit, "git", false,
		"Update from git repository HEAD")
	updateCmd.Flags().StringVar(&updateRecipe, "recipe", "",
		"Use a specific recipe TOML file")
	updateCmd.Flags().BoolVar(&updateBuild, "build", false,
		"Build from source (skip prebuilt binary)")
	updateCmd.Flags().BoolVar(&updateNoRefresh, "no-refresh", false,
		"Skip refreshing configured recipe taps before resolving")
	updateCmd.Flags().BoolVar(&updateNoInstall, "no-install", false,
		"Write new version pins to gale.toml without installing "+
			"(run 'gale sync' to install)")
	updateCmd.Flags().BoolVarP(&updateGlobal, "global", "g",
		false, "Update global packages")
	updateCmd.Flags().BoolVarP(&updateProject, "project", "p",
		false, "Update project packages")
	rootCmd.AddCommand(updateCmd)
}
