package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/kelp/gale/internal/attestation"
	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/ghcr"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/lockgraph"
	"github.com/spf13/cobra"
)

var (
	verifyGlobal  bool
	verifyProject bool
)

var verifyCmd = &cobra.Command{
	Use:   "verify <package>",
	Short: "Verify attestation for an installed package",
	Long:  "Check Sigstore attestation to confirm a package binary was built by gale-recipes CI. Verification runs in-process; no external tools required.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateScopeFlags(verifyGlobal, verifyProject); err != nil {
			return err
		}
		name := args[0]
		out := newCmdOutput(cmd)

		v := attestation.NewVerifier()

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
		lv, err := lockfile.Load(lp)
		if err != nil {
			return fmt.Errorf("reading lockfile: %w", err)
		}
		host, hErr := config.CurrentHost()
		if hErr != nil {
			return hErr
		}
		pkg, ok, err := lv.Entry(name, host, currentPlatform())
		if err != nil {
			return fmt.Errorf("reading lockfile: %w", err)
		}
		if !ok {
			return fmt.Errorf(
				"%s not found in lockfile — install it first", name,
			)
		}
		if err := checkPrebuilt(name, pkg); err != nil {
			return err
		}

		repoPath := localGHCRBase + "/" + name

		out.Step(fmt.Sprintf(
			"Verifying attestation for %s@%s...", name, pkg.Version,
		))

		if err := attestation.VerifyPrebuilt(v, attestation.PrebuiltParams{
			Repo:           attestation.DefaultRepo,
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

// checkPrebuilt rejects a lock entry that names no prebuilt artifact.
//
// Sigstore attestation covers a binary published by gale-recipes CI. A
// source artifact's recorded hash is the output of a local build, so no
// GHCR blob stands behind it and no bundle exists to fetch. Left to
// run, verification fails after a token exchange and an HTTP round trip
// with an error about a missing blob, which describes the symptom
// rather than the cause.
//
// A legacy entry records no method and cannot answer the question, so
// it keeps today's behavior rather than being guessed at.
func checkPrebuilt(name string, e lockfile.Entry) error {
	if e.Method == lockgraph.MethodSource {
		return fmt.Errorf(
			"%s@%s is locked as a source build, which has no prebuilt "+
				"attestation to verify — use 'gale audit %s' to rebuild "+
				"and compare its hash instead",
			name, e.Version, name,
		)
	}
	return nil
}

// downloadArchive fetches the raw tar.zst package blob from GHCR so
// `gale verify` can fall back to the GitHub Attestations API for
// packages published before OCI attestations were pushed as referrers.
func downloadArchive(name, sha256 string) (string, error) {
	// Scratch space first: it is a local precondition, so failing
	// on it costs no token exchange and no round trip. The error
	// is propagated, never swallowed — build.TmpDir already
	// exhausted its own fallback before returning one (gh#235).
	tmpDir, err := build.TmpDir()
	if err != nil {
		return "", fmt.Errorf("build temp dir: %w", err)
	}

	token, err := ghcr.Token(localGHCRBase + "/" + name)
	if err != nil {
		return "", fmt.Errorf("fetch ghcr token: %w", err)
	}
	blobURL := verifyBlobURL(name, sha256)

	f, err := os.CreateTemp(tmpDir, "gale-verify-archive-*.tar.zst")
	if err != nil {
		return "", fmt.Errorf("create temp archive: %w", err)
	}
	f.Close()

	if err := download.FetchWithAuth(blobURL, f.Name(), token); err != nil {
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
