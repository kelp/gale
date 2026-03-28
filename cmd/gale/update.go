package main

import (
	"fmt"
	"os"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/gitutil"
	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var (
	updateLocal  bool
	updateSource string
	updateGit    bool
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

		// --source: rebuild from local source directory.
		if updateSource != "" {
			return installFromLocalSource(
				args[0], "", updateSource, out)
		}

		// --git: check remote HEAD, rebuild if changed.
		if updateGit {
			if len(args) != 1 {
				return fmt.Errorf(
					"--git requires exactly one package name")
			}
			return updateFromGit(args[0], updateLocal, out)
		}

		ctx, err := newCmdContext(updateLocal)
		if err != nil {
			return err
		}

		cfg, err := ctx.LoadConfig()
		if err != nil {
			return err
		}

		// Determine which packages to update.
		targets := cfg.Packages
		if len(args) > 0 {
			targets = make(map[string]string)
			for _, name := range args {
				ver, ok := cfg.Packages[name]
				if !ok {
					out.Warn(fmt.Sprintf(
						"%s not in gale.toml, skipping", name))
					continue
				}
				targets[name] = ver
			}
		}

		if len(targets) == 0 {
			out.Info("No packages to update.")
			return nil
		}

		var updated int
		for name, currentVer := range targets {
			r, err := ctx.Resolver(name)
			if err != nil {
				out.Warn(fmt.Sprintf(
					"Skipping %s: %v", name, err))
				continue
			}

			if r.Package.Version == currentVer {
				out.Info(fmt.Sprintf(
					"%s@%s is up to date", name, currentVer))
				continue
			}

			out.Info(fmt.Sprintf("Updating %s %s → %s...",
				name, currentVer, r.Package.Version))

			result, err := ctx.installPackage(name, out)
			if err != nil {
				out.Warn(fmt.Sprintf(
					"Failed to update %s: %v", name, err))
				continue
			}

			// Update version in gale.toml.
			if err := config.AddPackage(ctx.GalePath,
				name, r.Package.Version); err != nil {
				return fmt.Errorf("updating config: %w", err)
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
func updateFromGit(name string, local bool, out *output.Output) error {
	// Resolve recipe to get source.repo.
	ctx, err := newCmdContext(local)
	if err != nil {
		return err
	}

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
	return installFromGit(name, "", out)
}

func init() {
	updateCmd.Flags().BoolVar(&updateLocal, "local", false,
		"Resolve recipes from sibling gale-recipes directory")
	updateCmd.Flags().StringVar(&updateSource, "source", "",
		"Rebuild from a local source directory")
	updateCmd.Flags().BoolVar(&updateGit, "git", false,
		"Update from git repository HEAD")
	rootCmd.AddCommand(updateCmd)
}
