package main

import (
	"fmt"
	"os"

	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var auditCmd = &cobra.Command{
	Use:   "audit <package>",
	Short: "Verify a package builds reproducibly",
	Long: `Rebuild a package from source and compare the SHA256 against the
installed binary. Most builds are not yet deterministic — mismatches
are expected due to timestamps, embedded paths, and build IDs. A match
confirms the build is reproducible. A mismatch does not indicate
tampering.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

		// Get installed version and hash from lockfile.
		configPath, err := resolveConfigPath(false)
		if err != nil {
			return fmt.Errorf("finding config: %w", err)
		}
		lf, err := lockfile.Read(lockfilePath(configPath))
		if err != nil {
			return fmt.Errorf("reading lockfile: %w", err)
		}
		pkg, ok := lf.Packages[name]
		if !ok {
			return fmt.Errorf(
				"%s not found in lockfile — install it first", name)
		}
		if pkg.SHA256 == "" {
			return fmt.Errorf(
				"%s has no SHA256 in lockfile — reinstall it", name)
		}

		// Resolve recipe at the pinned version.
		ctx, err := newCmdContext("")
		if err != nil {
			return fmt.Errorf("creating context: %w", err)
		}
		r, err := resolveVersionedRecipe(ctx, name, pkg.Version)
		if err != nil {
			return fmt.Errorf("resolving recipe: %w", err)
		}

		// Install dependencies (build, runtime, and implicit
		// system deps).
		depPaths, err := ctx.Installer.InstallBuildDeps(r)
		if err != nil {
			return fmt.Errorf("install build deps: %w", err)
		}
		var deps *build.BuildDeps
		if len(depPaths.BinDirs) > 0 || len(depPaths.StoreDirs) > 0 {
			deps = &build.BuildDeps{
				BinDirs:   depPaths.BinDirs,
				StoreDirs: depPaths.StoreDirs,
				NamedDirs: depPaths.NamedDirs,
			}
		}

		// Rebuild from source.
		out.Info(fmt.Sprintf(
			"Rebuilding %s@%s from source...", name, pkg.Version))
		tmpDir := build.TmpDir()
		result, err := build.Build(r, tmpDir, r.Build.Debug, deps)
		if err != nil {
			return fmt.Errorf("build failed: %w", err)
		}
		defer os.Remove(result.Archive)

		// Compare hashes.
		if result.SHA256 == pkg.SHA256 {
			out.Success(fmt.Sprintf(
				"%s@%s: build is reproducible", name, pkg.Version))
			fmt.Fprintf(os.Stderr, "    sha256: %s\n", pkg.SHA256)
		} else {
			out.Error(fmt.Sprintf(
				"%s@%s: build differs from installed binary",
				name, pkg.Version))
			fmt.Fprintf(os.Stderr, "    installed: %s\n", pkg.SHA256)
			fmt.Fprintf(os.Stderr, "    rebuilt:   %s\n", result.SHA256)
			return fmt.Errorf("audit failed: hashes do not match")
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(auditCmd)
}
