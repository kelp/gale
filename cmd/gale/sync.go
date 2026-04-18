package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"

	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/store"
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
		// Check store first — pinned version present?
		if ctx.Installer.Store.IsInstalled(name, version) {
			// Installed. Check if stale relative to the
			// current recipes of its declared deps. A
			// stale install means one of its deps had a
			// recipe bump; we fall through to reinstall.
			stale := false
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

		result, err := ctx.Installer.Install(r)
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

		// Verify SHA256 against lockfile if present.
		locked, hasLock := lf.Packages[name]
		if hasLock && locked.SHA256 != "" &&
			result.SHA256 != "" &&
			evictOnSHA256Mismatch(
				ctx.Installer.Store, result, locked.SHA256, out) {
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

		// Update lockfile with the SHA256 from install.
		if result.SHA256 != "" {
			lp, lpErr := lockfilePath(ctx.GalePath)
			if lpErr == nil {
				_ = updateLockfile(lp, name, version, result.SHA256)
			}
		}

		installed++
	}

	if err := finishSync(dryRun, failed, ctx.RebuildGeneration); err != nil {
		if failed > 0 {
			out.Warn(fmt.Sprintf(
				"Sync finished with %d error(s)", failed))
		}
		if failed > 0 {
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

// evictOnSHA256Mismatch removes a package from the store
// when the installed SHA256 does not match the locked hash.
// Returns true if a mismatch was detected and the package
// was evicted.
func finishSync(dryRun bool, failed int, rebuild func() error) error {
	if dryRun {
		return nil
	}
	if failed > 0 {
		return fmt.Errorf("%d package(s) could not be synced", failed)
	}
	return rebuild()
}

func evictOnSHA256Mismatch(s *store.Store, result *installer.InstallResult, lockedSHA string, out *output.Output) bool {
	if lockedSHA == result.SHA256 {
		return false
	}
	if err := s.Remove(result.Name, result.Version); err != nil {
		// Log but continue — mismatch detected regardless.
		out.Warn(fmt.Sprintf("removing %s@%s from store: %v",
			result.Name, result.Version, err))
	}
	return true
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
