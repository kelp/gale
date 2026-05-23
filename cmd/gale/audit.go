package main

import (
	"fmt"
	"os"

	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/spf13/cobra"
)

var (
	auditGlobal  bool
	auditProject bool
)

var auditCmd = &cobra.Command{
	Use:   "audit <package>",
	Short: "Rebuild a package and compare its hash",
	Long: `Rebuild a package from source and compare the SHA256 against the
installed binary. Most builds are not yet deterministic — mismatches
are expected due to timestamps, embedded paths, and build IDs. A match
confirms the build is reproducible. A mismatch does not indicate
tampering.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateScopeFlags(auditGlobal, auditProject); err != nil {
			return err
		}
		name := args[0]
		out := newCmdOutput(cmd)

		// Resolve context first so lockfile uses the same
		// config path as the installer.
		ctx, err := newCmdContext("", auditGlobal, auditProject)
		if err != nil {
			return fmt.Errorf("creating context: %w", err)
		}

		// Get installed version and hash from lockfile.
		lp, lpErr := lockfilePath(ctx.GalePath)
		if lpErr != nil {
			return lpErr
		}
		lf, err := lockfile.Read(lp)
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
		r, err := ctx.ResolveVersionedRecipe(name, pkg.Version)
		if err != nil {
			return fmt.Errorf("resolving recipe: %w", err)
		}

		// Install dependencies (build, runtime, and implicit
		// system deps).
		deps, err := ctx.Installer.InstallBuildDeps(r)
		if err != nil {
			return fmt.Errorf("installing build deps: %w", err)
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

		// Compare hashes. Human-friendly banner + detail go to
		// stderr via the output helper so quiet/color modes are
		// honoured. The hash(es) themselves go to stdout, one
		// per line with no prefix, so scripts can pipe:
		//   gale audit foo | head -1
		stdout := cmd.OutOrStdout()
		if result.SHA256 == pkg.SHA256 {
			out.Success(fmt.Sprintf(
				"%s@%s: build is reproducible", name, pkg.Version))
			out.Step(fmt.Sprintf("sha256: %s", pkg.SHA256))
			fmt.Fprintln(stdout, pkg.SHA256)
		} else {
			out.Error(fmt.Sprintf(
				"%s@%s: build differs from installed binary",
				name, pkg.Version))
			out.Step(fmt.Sprintf("installed: %s", pkg.SHA256))
			out.Step(fmt.Sprintf("rebuilt:   %s", result.SHA256))
			fmt.Fprintln(stdout, pkg.SHA256)
			fmt.Fprintln(stdout, result.SHA256)
			return fmt.Errorf("audit failed: hashes do not match")
		}

		return nil
	},
}

func init() {
	auditCmd.Flags().BoolVarP(&auditGlobal, "global", "g", false,
		"Audit against the global lockfile")
	auditCmd.Flags().BoolVarP(&auditProject, "project", "p", false,
		"Audit against the project lockfile")
	rootCmd.AddCommand(auditCmd)
}
