package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"

	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/spf13/cobra"
)

var (
	syncRecipes string
	syncBuild   bool
	syncGlobal  bool
	syncProject bool
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Install all packages in gale.toml",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if syncGlobal && syncProject {
			return fmt.Errorf(
				"cannot use both --global and --project")
		}
		return runSync(syncRecipes, syncBuild, syncGlobal,
			syncProject, "")
	},
}

// runSync performs the sync operation: resolves recipes,
// installs missing packages, and rebuilds the generation.
// When projectDir is non-empty, sync targets that specific
// project directory regardless of cwd or scope flags.
func runSync(recipesPath string, buildOnly, global, project bool, projectDir string) error {
	out := newOutput()

	ctx, err := newCmdContext(recipesPath, false, false)
	if err != nil {
		return err
	}

	// Explicit project directory takes precedence over
	// scope flags. Used by syncIfNeeded when shell/run
	// are invoked with --project.
	if projectDir != "" {
		ctx.GalePath = filepath.Join(projectDir, "gale.toml")
		ctx.GaleDir = filepath.Join(projectDir, ".gale")
	} else if global || project {
		// Override scope when -g or -p is set.
		galePath, pathErr := resolveConfigPath(global)
		if pathErr != nil {
			return pathErr
		}
		galeDir, dirErr := galeDirForConfig(galePath)
		if dirErr != nil {
			return dirErr
		}
		ctx.GalePath = galePath
		ctx.GaleDir = galeDir
	}

	if buildOnly {
		ctx.Installer.SourceOnly = true
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
	lp, err := lockfilePath(ctx.GalePath)
	if err != nil {
		return err
	}
	lf, err := lockfile.Read(lp)
	if err != nil {
		return fmt.Errorf("reading lockfile: %w", err)
	}

	var installed, failed int
	for name, version := range cfg.Packages {
		// Track whether this iteration is a stale reinstall
		// so we can route through Reinstall (skip cache) rather
		// than Install (which would cache-hit on a bare dir).
		stale := false
		if ctx.Installer.Store.IsInstalled(name, version) {
			if storeDir, ok := ctx.Installer.Store.StorePath(name, version); ok {
				// Missing .gale-deps.toml means the install
				// predates the revision system. Flag it stale
				// without needing the recipe, so soft migration
				// still works when the installed version is no
				// longer in the registry's .versions index.
				if !installer.HasDepsMetadata(storeDir) {
					stale = true
				} else if r, err := ctx.ResolveVersionedRecipe(name, version); err == nil {
					if s, err := installer.IsStale(storeDir, r, ctx.Resolver); err == nil {
						stale = s
					}
				}
			}
			if !stale {
				if dryRun {
					out.Info(fmt.Sprintf(
						"skip %s@%s (up to date)",
						name, version))
				} else {
					out.Info(fmt.Sprintf(
						"%s@%s up to date", name, version))
				}
				continue
			}
			out.Info(fmt.Sprintf(
				"%s@%s stale — deps changed; reinstalling",
				name, version))
		}

		if dryRun {
			out.Info(fmt.Sprintf(
				"install %s@%s", name, version))
			installed++
			continue
		}

		// Not in store — fetch recipe for pinned version.
		r, err := ctx.ResolveVersionedRecipe(
			name, version)
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

		var result *installer.InstallResult
		if stale {
			result, err = ctx.Installer.Reinstall(r)
		} else {
			result, err = ctx.Installer.Install(r)
		}
		if err != nil {
			if errors.Is(err, build.ErrUnsupportedPlatform) {
				out.Warn(fmt.Sprintf(
					"%s does not support %s/%s",
					name, runtime.GOOS, runtime.GOARCH))
			} else {
				out.Warn(fmt.Sprintf(
					"Failed to install %s: %v", name, err))
			}
			failed++
			continue
		}

		// Compare against the lockfile. The install has
		// already been verified against the recipe's
		// expected SHA256, so a disagreement here only
		// means the recipe (or the built output) has
		// changed since the last install on this machine.
		// Warn, update the cache, and keep the install —
		// evicting a freshly-verified package is
		// destructive and the user doesn't gain anything
		// from it.
		locked, hasLock := lf.Packages[name]
		if hasLock && locked.SHA256 != "" &&
			result.SHA256 != "" &&
			locked.SHA256 != result.SHA256 {
			out.Warn(fmt.Sprintf(
				"%s@%s SHA256 changed since last sync "+
					"(lock: %s..., got: %s...) — "+
					"updating lockfile",
				name, version,
				locked.SHA256[:12],
				result.SHA256[:12]))
		}

		reportResult(out, result, "Installed", "built from source")

		// Update lockfile with the SHA256 from install.
		if result.SHA256 != "" {
			lp, lpErr := lockfilePath(ctx.GalePath)
			if lpErr == nil {
				_ = updateLockfile(lp, name, version, result.SHA256)
			}
		}

		installed++
	}

	if err := finishSync(dryRun, failed, ctx.RebuildGenerationLenient); err != nil {
		if failed > 0 {
			out.Warn(fmt.Sprintf(
				"Sync finished with %d error(s)", failed))
			return err
		}
		return fmt.Errorf("rebuild generation: %w", err)
	}

	out.Success(fmt.Sprintf(
		"Sync complete: %d installed, %d up to date",
		installed,
		len(cfg.Packages)-installed))
	return nil
}

// finishSync rebuilds the generation so packages that did
// install land on PATH, then surfaces any install failure.
// Issue #20: a single broken recipe used to leave the user
// with no current symlink at all. Rebuilding first keeps
// partial progress usable; the failure error still
// propagates so the exit code is non-zero.
func finishSync(dryRun bool, failed int, rebuild func() error) error {
	if dryRun {
		return nil
	}
	rebuildErr := rebuild()
	if failed > 0 {
		return fmt.Errorf("%d package(s) could not be synced", failed)
	}
	return rebuildErr
}

func init() {
	syncCmd.Flags().BoolVarP(&syncGlobal, "global", "g",
		false, "Sync global packages")
	syncCmd.Flags().BoolVarP(&syncProject, "project", "p",
		false, "Sync project packages")
	syncCmd.Flags().StringVar(&syncRecipes, "recipes", "",
		"Use local recipes directory (default: ../gale-recipes/)")
	syncCmd.Flags().Lookup("recipes").NoOptDefVal = "auto"
	syncCmd.Flags().BoolVar(&syncBuild, "build", false,
		"Build all packages from source (skip prebuilt binaries)")
	rootCmd.AddCommand(syncCmd)
}
