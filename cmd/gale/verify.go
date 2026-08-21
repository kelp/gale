package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/spf13/cobra"

	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/store"
)

var (
	verifyGlobal  bool
	verifyProject bool
)

var (
	errVerifyNoLock = errors.New("gale verify needs a v2 lock")
	errVerifyV1     = errors.New(
		"gale verify reads a v2 lock; this lock has no tree_digest",
	)
	errVerifyAttestation = errors.New(
		"gale verify: locked attestation is not checkable",
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
		"it to the v2 lock. Does not talk to GHCR. Does not " +
		"mutate the store, lock, or current. A v1 lock has no " +
		"tree_digest.",
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
		return fmt.Errorf("%w: %s", errVerifyAttestation, root)
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
