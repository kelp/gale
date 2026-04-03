package main

import (
	"fmt"
	"os"

	"github.com/kelp/gale/internal/gitutil"
	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var (
	updateRecipes string
	updateSource  string
	updateGit     bool
	updateRecipe  string
)

var updateCmd = &cobra.Command{
	Use:   "update [package...]",
	Short: "Update packages to the latest version",
	RunE: func(cmd *cobra.Command, args []string) error {
		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

		// --source requires exactly one package name.
		if updateSource != "" && len(args) != 1 {
			return fmt.Errorf(
				"--source requires exactly one package name")
		}

		// Resolve context for config path. All branches
		// use ctx.GalePath for config writes.
		ctx, err := newCmdContext(updateRecipes)
		if err != nil {
			return err
		}

		// --source: rebuild from local source directory.
		if updateSource != "" {
			return installFromLocalSource(
				args[0], updateRecipe, updateSource,
				ctx.GalePath, ctx.GaleDir, ctx.StoreRoot, out)
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

		var updated int
		for name, t := range targets {
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
				if r.Package.Version == t.current {
					out.Info(fmt.Sprintf(
						"%s@%s is up to date",
						name, t.current))
					continue
				}
				newVersion = r.Package.Version
			}

			// Fetch the recipe for the target version.
			r, err := resolveVersionedRecipe(
				ctx, name, newVersion)
			if err != nil {
				out.Warn(fmt.Sprintf(
					"Skipping %s: %v", name, err))
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
			if err := writeConfigAndLock(ctx.GalePath,
				name, r.Package.Version,
				result.SHA256); err != nil {
				return fmt.Errorf("updating %s: %w",
					name, err)
			}

			reportResult(out, result, "Updated", "built from source")
			updated++
		}

		if err := rebuildGeneration(ctx.GaleDir,
			ctx.StoreRoot, ctx.GalePath); err != nil {
			return fmt.Errorf("rebuild generation: %w", err)
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

	if cfg.Packages[name] == remoteHash {
		out.Success(fmt.Sprintf(
			"%s@%s is up to date", name, remoteHash))
		return nil
	}

	out.Info(fmt.Sprintf("Updating %s to %s...",
		name, remoteHash))
	return installFromGit(name, updateRecipe,
		ctx.GalePath, ctx.GaleDir, ctx.StoreRoot,
		updateRecipes, out)
}

func init() {
	updateCmd.Flags().StringVar(&updateRecipes, "recipes", "",
		"Use local recipes directory (default: ../gale-recipes/)")
	updateCmd.Flags().Lookup("recipes").NoOptDefVal = "auto"
	updateCmd.Flags().StringVar(&updateSource, "source", "",
		"Rebuild from a local source directory")
	updateCmd.Flags().BoolVar(&updateGit, "git", false,
		"Update from git repository HEAD")
	updateCmd.Flags().StringVar(&updateRecipe, "recipe", "",
		"Use a specific recipe TOML file")
	rootCmd.AddCommand(updateCmd)
}
