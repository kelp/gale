package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/kelp/gale/internal/attestation"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var verifyCmd = &cobra.Command{
	Use:   "verify <package>",
	Short: "Verify attestation for an installed package",
	Long:  "Check Sigstore attestation to confirm a package binary was built by gale-recipes CI. Requires the gh CLI.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

		v := attestation.NewVerifier()
		if !v.Available() {
			return fmt.Errorf(
				"gh CLI is required for attestation verification\n" +
					"  Install: https://cli.github.com")
		}

		// Resolve context first so lockfile uses the same
		// config path the installer would use.
		ctx, err := newCmdContext("")
		if err != nil {
			return fmt.Errorf("creating context: %w", err)
		}

		// Find the lockfile to get the version.
		lf, err := lockfile.Read(lockfilePath(ctx.GalePath))
		if err != nil {
			return fmt.Errorf("reading lockfile: %w", err)
		}
		pkg, ok := lf.Packages[name]
		if !ok {
			return fmt.Errorf(
				"%s not found in lockfile — install it first", name)
		}

		platform := runtime.GOOS + "-" + runtime.GOARCH
		tag := pkg.Version + "-" + platform
		ociURI := fmt.Sprintf(
			"oci://ghcr.io/%s/%s:%s",
			localGHCRBase, name, tag)

		out.Step(fmt.Sprintf(
			"Verifying attestation for %s@%s...", name, pkg.Version))

		if err := attestation.VerifyOCI(
			ociURI, attestation.DefaultRepo); err != nil {
			return fmt.Errorf("verification failed: %w", err)
		}

		out.Success(fmt.Sprintf(
			"%s@%s attestation verified", name, pkg.Version))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(verifyCmd)
}
