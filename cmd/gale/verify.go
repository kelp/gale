package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/kelp/gale/internal/attestation"
	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/ghcr"
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
		repoPath := localGHCRBase + "/" + name
		ociURI := attestation.OCIURI(
			repoPath, pkg.Version, platform, pkg.ManifestDigest,
		)

		out.Step(fmt.Sprintf(
			"Verifying attestation for %s@%s...", name, pkg.Version,
		))

		if err := attestation.VerifyPrebuilt(v, attestation.PrebuiltParams{
			Repo:           attestation.DefaultRepo,
			OCIURI:         ociURI,
			ManifestDigest: pkg.ManifestDigest,
			FetchBundle: func() ([]byte, error) {
				ctx, cancel := context.WithTimeout(
					context.Background(), 30*time.Second,
				)
				defer cancel()
				token, terr := ghcr.Token(repoPath)
				if terr != nil {
					return nil, fmt.Errorf("fetch ghcr token: %w", terr)
				}
				return ghcr.FetchReferrerBundle(
					ctx, verifyBlobURL(name, pkg.SHA256),
					pkg.ManifestDigest, token,
				)
			},
			Archive: func() (string, func(), error) {
				archivePath, dlErr := downloadArchive(name, pkg.SHA256)
				if dlErr != nil {
					return "", nil, dlErr
				}
				return archivePath, func() { os.Remove(archivePath) }, nil
			},
		}); err != nil {
			return fmt.Errorf("verification failed: %w", err)
		}

		out.Success(fmt.Sprintf(
			"%s@%s attestation verified", name, pkg.Version,
		))
		return nil
	},
}

// downloadArchive fetches the raw tar.zst package blob from GHCR so
// `gale verify` can fall back to the GitHub Attestations API for
// packages published before OCI attestations were pushed as referrers.
func downloadArchive(name, sha256 string) (string, error) {
	token, err := ghcr.Token(localGHCRBase + "/" + name)
	if err != nil {
		return "", fmt.Errorf("fetch ghcr token: %w", err)
	}
	blobURL := verifyBlobURL(name, sha256)

	tmpDir := build.TmpDir()
	if tmpDir == "" {
		return "", fmt.Errorf("build temp dir unavailable")
	}
	f, err := os.CreateTemp(tmpDir, "gale-verify-archive-*.tar.zst")
	if err != nil {
		return "", fmt.Errorf("create temp archive: %w", err)
	}
	f.Close()

	if err := download.FetchWithAuthNamed(blobURL, f.Name(), token, ""); err != nil {
		os.Remove(f.Name())
		return "", err
	}

	// Verify the downloaded bytes against the expected digest before
	// handing the file to attestation verification. A mismatch here is
	// far clearer than a downstream bundle 404.
	if err := verifyArchiveDigest(f.Name(), sha256); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// verifyBlobURL builds the GHCR blob URL for a package's archive,
// honoring the GALE_GHCR_URL override (via ghcr.BaseURL) so the
// referrer fetch and the file-fallback download both reach the same
// registry host, including a fake one in integration tests.
func verifyBlobURL(name, sha256 string) string {
	return fmt.Sprintf(
		"%s/v2/%s/%s/blobs/sha256:%s",
		ghcr.BaseURL(), localGHCRBase, name, sha256,
	)
}

// verifyArchiveDigest checks that the file at path hashes to wantSHA
// (hex-encoded SHA256), returning a localized error on mismatch.
func verifyArchiveDigest(path, wantSHA string) error {
	got, err := download.HashFile(path)
	if err != nil {
		return fmt.Errorf("hashing downloaded archive: %w", err)
	}
	if got != wantSHA {
		return fmt.Errorf(
			"downloaded archive sha256 mismatch: expected %s, got %s",
			wantSHA, got,
		)
	}
	return nil
}

func init() {
	verifyCmd.Flags().BoolVarP(&verifyGlobal, "global", "g", false,
		"Verify against the global lockfile")
	verifyCmd.Flags().BoolVarP(&verifyProject, "project", "p", false,
		"Verify against the project lockfile")
	rootCmd.AddCommand(verifyCmd)
}
