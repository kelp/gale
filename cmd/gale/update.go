package main

import (
	"fmt"
	"sort"

	"github.com/kelp/gale/internal/gitutil"
	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"
)

var (
	updateRecipes string
	updatePath    string
	updateGit     bool
	updateRecipe  string
	updateBuild   bool
)

var updateCmd = &cobra.Command{
	Use:   "update [package...]",
	Short: "Update packages to the latest version",
	RunE: func(cmd *cobra.Command, args []string) error {
		out := newCmdOutput(cmd)

		// --path requires exactly one package name.
		if updatePath != "" && len(args) != 1 {
			return fmt.Errorf(
				"--path requires exactly one package name")
		}

		// Resolve context for config path. All branches
		// use ctx.GalePath for config writes.
		ctx, err := newCmdContext(updateRecipes, false, false)
		if err != nil {
			return err
		}

		if updateBuild {
			ctx.Installer.SourceOnly = true
		}

		// --path: rebuild from local source directory.
		if updatePath != "" {
			return installFromLocalSource(ctx,
				args[0], updateRecipe, updatePath, out)
		}

		// --git: check remote HEAD, rebuild if changed.
		if updateGit {
			if len(args) != 1 {
				return fmt.Errorf(
					"--git requires exactly one package name")
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
				name, ver := parsePackageArg(arg)
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

		var updated int
		defer func() {
			if updated > 0 && !dryRun {
				if err := ctx.RebuildGeneration(); err != nil {
					out.Warn(fmt.Sprintf("rebuild generation: %v", err))
				}
			}
		}()
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
				ver, skip := updateAction(
					r.Package.Version, t.current,
					ctx.Installer.Store.IsInstalled(
						name, t.current))
				if skip {
					out.Info(fmt.Sprintf(
						"%s@%s is up to date",
						name, t.current))
					continue
				}
				newVersion = ver
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
					name, t.current, r.Package.Version))
				updated++
				continue
			}

			out.Info(fmt.Sprintf("Updating %s %s → %s...",
				name, t.current, r.Package.Version))

			result, err := ctx.Installer.Install(r)
			if err != nil {
				out.Warn(fmt.Sprintf(
					"Failed to update %s: %v", name, err))
				continue
			}

			// Update gale.toml and lockfile.
			if err := ctx.WriteConfigAndLock(
				name, r.Package.Version,
				result.SHA256); err != nil {
				return fmt.Errorf("updating %s: %w",
					name, err)
			}

			reportResult(out, result, "Updated", "built from source")
			updated++
		}

		if updated == 0 {
			out.Success("Everything is up to date.")
		} else {
			out.Success(fmt.Sprintf(
				"Updated %d package(s)", updated))
		}
		return nil
	},
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

// isNewerVersion reports whether candidate is strictly
// newer than current using semver comparison. Returns
// true if either version is not valid semver (cannot
// compare, so allow the update to proceed).
func isNewerVersion(candidate, current string) bool {
	// golang.org/x/mod/semver requires a "v" prefix.
	cv := "v" + current
	nv := "v" + candidate
	if !semver.IsValid(cv) || !semver.IsValid(nv) {
		return true
	}
	return semver.Compare(nv, cv) > 0
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
		"Use local recipes directory (default: ../gale-recipes/)")
	updateCmd.Flags().Lookup("recipes").NoOptDefVal = "auto"
	updateCmd.Flags().StringVar(&updatePath, "path", "",
		"Rebuild from a local source directory")
	updateCmd.Flags().BoolVar(&updateGit, "git", false,
		"Update from git repository HEAD")
	updateCmd.Flags().StringVar(&updateRecipe, "recipe", "",
		"Use a specific recipe TOML file")
	updateCmd.Flags().BoolVar(&updateBuild, "build", false,
		"Build from source (skip prebuilt binary)")
	rootCmd.AddCommand(updateCmd)
}
