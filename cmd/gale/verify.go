package main

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/kelp/gale/internal/attestation"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/spf13/cobra"
)

var (
	verifyGlobal  bool
	verifyProject bool
)

var verifyCmd = &cobra.Command{
	Use:   "verify <package>",
	Short: "Verify attestation for an installed package",
	Long:  "Check Sigstore attestation to confirm a package binary was built by gale-recipes CI. Requires the gh CLI.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateScopeFlags(verifyGlobal, verifyProject); err != nil {
			return err
		}
		name := args[0]
		out := newCmdOutput(cmd)

		v := attestation.NewVerifier()
		if !v.Available() {
			return fmt.Errorf(
				"attestation verification unavailable: %s",
				v.UnavailableReason(),
			)
		}

		// Resolve context first so lockfile uses the same
		// config path the installer would use.
		ctx, err := newCmdContext("", verifyGlobal, verifyProject)
		if err != nil {
			return fmt.Errorf("creating context: %w", err)
		}

		// Find the lockfile to get the version.
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
				"%s not found in lockfile — install it first", name,
			)
		}

		platform := runtime.GOOS + "-" + runtime.GOARCH
		// GHCR manifests are tagged with the bare version
		// ("<version>-<platform>"), not the canonical lockfile
		// form ("<version>-<revision>-<platform>"). Strip the
		// trailing "-<revision>" suffix when present so the
		// constructed tag matches what gale-recipes CI pushes.
		tag := bareVersion(pkg.Version) + "-" + platform
		ociURI := fmt.Sprintf(
			"oci://ghcr.io/%s/%s:%s",
			localGHCRBase, name, tag,
		)

		out.Step(fmt.Sprintf(
			"Verifying attestation for %s@%s...", name, pkg.Version,
		))

		if err := attestation.VerifyOCI(
			ociURI, attestation.DefaultRepo,
		); err != nil {
			return fmt.Errorf("verification failed: %w", err)
		}

		out.Success(fmt.Sprintf(
			"%s@%s attestation verified", name, pkg.Version,
		))
		return nil
	},
}

// verifyOCIURI constructs the OCI URI to verify. When digest is
// non-empty, it pins the manifest by digest
// ("oci://ghcr.io/<base>/<name>@<digest>"); otherwise it falls
// back to the tag form
// ("oci://ghcr.io/<base>/<name>:<bareVersion>-<platform>").
//
// Stub: returns "" until implemented.
func verifyOCIURI(base, name, version, platform, digest string) string {
	_ = base
	_ = name
	_ = version
	_ = platform
	_ = digest
	return ""
}

// bareVersion strips a Debian-style numeric revision suffix from v.
// A trailing "-<N>" where N is a positive integer is removed; any
// other suffix (e.g. "-rc1", "-dev.2") is left in place. This
// mirrors the semantics of internal/version.splitRevision so the
// two agree on what counts as a revision.
//
// Examples:
//
//	"1.8.1-4"  → "1.8.1"
//	"1.8.1"    → "1.8.1"
//	"0.10.0-2" → "0.10.0"
//	"1.0-rc1"  → "1.0-rc1"
//	"1.2-0"    → "1.2-0"
func bareVersion(v string) string {
	dash := strings.LastIndexByte(v, '-')
	if dash < 0 {
		return v
	}
	suffix := v[dash+1:]
	if suffix == "" {
		return v
	}
	n, err := strconv.Atoi(suffix)
	if err != nil || n <= 0 {
		return v
	}
	return v[:dash]
}

func init() {
	verifyCmd.Flags().BoolVarP(&verifyGlobal, "global", "g", false,
		"Verify against the global lockfile")
	verifyCmd.Flags().BoolVarP(&verifyProject, "project", "p", false,
		"Verify against the project lockfile")
	rootCmd.AddCommand(verifyCmd)
}
