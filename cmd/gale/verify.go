package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/spf13/cobra"

	"github.com/kelp/gale/internal/attestation"
	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/store"
)

var (
	verifyGlobal  bool
	verifyProject bool
)

// verifyAttestFile overrides attestation verification for tests.
// Nil means the production in-process sigstore verifier;
// production wiring never sets it.
var verifyAttestFile func(filePath, repo string) error

var (
	errVerifyNoLock = errors.New("gale verify needs a v2 lock")
	errVerifyV1     = errors.New(
		"gale verify reads a v2 lock; this lock has no tree_digest",
	)
	errVerifyAttestation = errors.New(
		"gale verify: locked attestation did not verify",
	)
	errVerifyIdentity = errors.New(
		"gale verify: locked attestation identity disagrees with policy",
	)
	errVerifyDigest     = errors.New("gale verify: tree digest mismatch")
	errVerifyNoPlatform = errors.New(
		"gale verify: no current-platform artifact",
	)
	errVerifyEmptyDigest  = errors.New("gale verify: empty tree_digest")
	errVerifyMissingStore = errors.New(
		"gale verify: fetch store missing",
	)
	errVerifyUnknownRoot = errors.New(
		"gale verify: package is not a default-target root",
	)
)

var verifyCmd = &cobra.Command{
	Use:   "verify [package]",
	Short: "Check store tree digests against the lock",
	Long: "Recompute each locked fetch tree digest and compare " +
		"it to the v2 lock. A locked attestation re-fetches the " +
		"locked URL and verifies it against the locked identity. " +
		"Does not mutate the store, lock, or current. A v1 lock " +
		"has no tree_digest.",
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateScopeFlags(verifyGlobal, verifyProject); err != nil {
			return err
		}
		c, err := newCmdContext("", verifyGlobal, verifyProject)
		if err != nil {
			return fmt.Errorf("creating context: %w", err)
		}
		var name string
		if len(args) == 1 {
			name = args[0]
		}
		return runVerify(cmd.Context(), c, name)
	},
}

func init() {
	verifyCmd.Flags().BoolVarP(&verifyGlobal, "global", "g", false,
		"Verify against the global lockfile")
	verifyCmd.Flags().BoolVarP(&verifyProject, "project", "p", false,
		"Verify against the project lockfile")
	rootCmd.AddCommand(verifyCmd)
}

func runVerify(ctx context.Context, c *cmdContext, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	lp, err := lockfilePath(c.GalePath)
	if err != nil {
		return err
	}
	lf, err := readVerifyLock(lp)
	if err != nil {
		return err
	}
	roots, err := verifyRoots(lf, name)
	if err != nil {
		return err
	}
	plat := currentPlatform()
	st := store.NewStore(c.StoreRoot)
	for _, root := range roots {
		if err := verifyOne(ctx, st, lf, root, plat); err != nil {
			return err
		}
	}
	return nil
}

func readVerifyLock(lp string) (*lockfile.V2, error) {
	lf, err := lockfile.ReadV2(lp)
	if err == nil {
		return lf, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errVerifyNoLock
	}
	if _, v1err := lockfile.ReadV1(lp); v1err == nil {
		return nil, errVerifyV1
	}
	return nil, fmt.Errorf("reading lockfile: %w", err)
}

func verifyRoots(lf *lockfile.V2, name string) ([]string, error) {
	if lf.Targets.Default == nil {
		return nil, errVerifyUnknownRoot
	}
	roots := lf.Targets.Default.Roots
	if name == "" {
		return append([]string(nil), roots...), nil
	}
	for _, root := range roots {
		got, _, err := lockfile.SplitV2Root(root)
		if err != nil {
			return nil, err
		}
		if got == name {
			return []string{root}, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", errVerifyUnknownRoot, name)
}

func verifyOne(
	ctx context.Context,
	st *store.Store,
	lf *lockfile.V2,
	root, plat string,
) error {
	name, version, err := lockfile.SplitV2Root(root)
	if err != nil {
		return err
	}
	pkg, ok := lf.Packages[root]
	if !ok {
		return fmt.Errorf("%w: %s", errVerifyUnknownRoot, root)
	}
	art, ok := pkg.Artifacts[plat]
	if !ok {
		return fmt.Errorf("%w: %s %s", errVerifyNoPlatform, root, plat)
	}
	if art.TreeDigest == "" {
		return fmt.Errorf("%w: %s", errVerifyEmptyDigest, root)
	}
	if art.Attestation != nil {
		if err := verifyLockedAttestation(ctx, name, art); err != nil {
			return err
		}
	}
	ok, err = st.FetchExists(name, version, art.SHA256)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", errVerifyMissingStore, root)
	}
	dest, err := st.FetchPath(name, version, art.SHA256)
	if err != nil {
		return err
	}
	got, err := provenance.DigestTree(ctx, dest)
	if err != nil {
		return fmt.Errorf("digesting %s: %w", dest, err)
	}
	if got != art.TreeDigest {
		return fmt.Errorf("%w: %s", errVerifyDigest, root)
	}
	return nil
}

// verifyLockedAttestation re-fetches the locked artifact and
// checks that its bytes still hash to the lock and still carry
// an attestation from the identity gale's policy names (§7d).
// The lock is the switch: the verified identity is the one
// recorded at lock time, cross-checked against the current
// policy so a stale or tampered record cannot pass. It never
// mutates.
func verifyLockedAttestation(
	ctx context.Context, name string, art lockfile.V2Artifact,
) error {
	repo, ok := attestation.PolicyFor(name)
	if !ok {
		return fmt.Errorf("%w: %s has no identity policy",
			errVerifyIdentity, name)
	}
	if art.Attestation.Repo != repo {
		return fmt.Errorf("%w: locked %q, policy %q",
			errVerifyIdentity, art.Attestation.Repo, repo)
	}
	tmp, err := os.CreateTemp("", "gale-verify-")
	if err != nil {
		return fmt.Errorf("%w: temp archive: %w",
			errVerifyAttestation, err)
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath)
	if err := download.Fetch(ctx, art.URL, tmpPath); err != nil {
		return fmt.Errorf("%w: re-fetching %s: %w",
			errVerifyAttestation, art.URL, err)
	}
	if err := download.VerifySHA256(ctx, tmpPath, art.SHA256); err != nil {
		return fmt.Errorf("%w: re-fetched bytes: %w",
			errVerifyAttestation, err)
	}
	verify := verifyAttestFile
	if verify == nil {
		verify = attestation.NewVerifier().VerifyFile
	}
	if err := verify(tmpPath, art.Attestation.Repo); err != nil {
		return fmt.Errorf("%w against %s: %w",
			errVerifyAttestation, art.Attestation.Repo, err)
	}
	return nil
}
