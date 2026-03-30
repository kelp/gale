package main

import (
	"fmt"
	"os"

	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var syncLocal bool

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Install all packages in gale.toml",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSync(syncLocal)
	},
}

// runSync performs the sync operation: resolves recipes,
// installs missing packages, and rebuilds the generation.
func runSync(local bool) error {
	out := output.New(os.Stderr, !noColor)

	ctx, err := newCmdContext(local)
	if err != nil {
		return err
	}

	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}

	if len(cfg.Packages) == 0 {
		out.Info("No packages to sync.")
		return nil
	}

	// Read lockfile for SHA256 verification.
	lf, err := lockfile.Read(lockfilePath(ctx.GalePath))
	if err != nil {
		return fmt.Errorf("reading lockfile: %w", err)
	}

	var installed, failed int
	for name, version := range cfg.Packages {
		// Check store first — pinned version present?
		if ctx.Installer.Store.IsInstalled(name, version) {
			out.Info(fmt.Sprintf(
				"%s@%s up to date", name, version))
			continue
		}

		// Not in store — fetch recipe for pinned version.
		r, err := resolveVersionedRecipe(
			ctx, name, version)
		if err != nil {
			out.Warn(fmt.Sprintf(
				"%s@%s: %v. "+
					"Run 'gale update %s' to install latest.",
				name, version, err, name))
			failed++
			continue
		}

		// Versions match — install.
		out.Info(fmt.Sprintf("Installing %s@%s...",
			name, version))

		result, err := ctx.Installer.Install(r)
		if err != nil {
			out.Warn(fmt.Sprintf(
				"Failed to install %s: %v", name, err))
			failed++
			continue
		}

		// Verify SHA256 against lockfile if present.
		locked, hasLock := lf.Packages[name]
		if hasLock && locked.SHA256 != "" &&
			result.SHA256 != "" &&
			locked.SHA256 != result.SHA256 {
			out.Warn(fmt.Sprintf(
				"%s@%s SHA256 mismatch "+
					"(lock: %s..., got: %s...)",
				name, version,
				locked.SHA256[:12],
				result.SHA256[:12]))
			failed++
			continue
		}

		reportResult(out, result, "Installed", "built from source")
		installed++
	}

	if err := rebuildGeneration(ctx.GaleDir,
		ctx.StoreRoot, ctx.GalePath); err != nil {
		return fmt.Errorf("rebuild generation: %w", err)
	}

	if failed > 0 {
		out.Warn(fmt.Sprintf(
			"Sync finished with %d error(s)", failed))
		return fmt.Errorf(
			"%d package(s) could not be synced", failed)
	}

	out.Success(fmt.Sprintf(
		"Sync complete: %d installed, %d up to date",
		installed,
		len(cfg.Packages)-installed))
	return nil
}

func init() {
	syncCmd.Flags().BoolVar(&syncLocal, "local", false,
		"Resolve recipes from sibling gale-recipes directory")
	rootCmd.AddCommand(syncCmd)
}
