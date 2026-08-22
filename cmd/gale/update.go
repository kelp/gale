package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/index"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var (
	updateIndex   string
	updateGlobal  bool
	updateProject bool
	// allowAttestationDrop is the §7d escape: an update that
	// would drop a locked attestation refuses unless this is
	// set, and setting it warns.
	allowAttestationDrop bool
)

var errAttestationDrop = errors.New(
	"update would drop a locked attestation; the index cannot switch " +
		"verification off — pass --allow-attestation-drop to accept it",
)

var updateCmd = &cobra.Command{
	Use:   "update [package...]",
	Short: "Update packages from the index",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateScopeFlags(updateGlobal, updateProject); err != nil {
			return err
		}
		c, err := newCmdContext("", updateGlobal, updateProject)
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		return runUpdateFetch(ctx, c, args, indexSource(updateIndex),
			newCmdOutput(cmd))
	},
}

func runUpdateFetch(
	ctx context.Context, c *cmdContext, args []string, src index.Source,
	out *output.Output,
) error {
	if err := refuseSwitchHosts(c.Host, c.GalePath); err != nil {
		return err
	}
	cfg, err := c.LoadConfig()
	if err != nil {
		return err
	}
	pins, names, err := pinsForUpdate(declaredPins(cfg), args)
	if err != nil {
		return err
	}
	if len(pins) == 0 {
		return nil
	}
	if dryRun {
		out := newOutput()
		for _, name := range names {
			out.Info(fmt.Sprintf("update %s", name))
		}
		return nil
	}
	lp, err := lockfilePath(c.GalePath)
	if err != nil {
		return err
	}
	existing, err := readExistingV2(lp)
	if err != nil {
		return err
	}
	if err := refuseMixedV2(existing); err != nil {
		return err
	}
	draft, arts, err := planAdopt(ctx, src, pins)
	if err != nil {
		return err
	}
	if dropped := droppedAttestations(existing, draft, names); len(dropped) > 0 {
		joined := strings.Join(dropped, ", ")
		if !allowAttestationDrop {
			return fmt.Errorf("%w: %s", errAttestationDrop, joined)
		}
		out.Warn("allowing attestation drop: " + joined)
	}
	draft = mergeV2LockNames(existing, draft, names)
	var undos []func() error
	for _, name := range names {
		pin := pinForManifest(draft, name)
		cw, werr := c.writeConfigWitnessed(name, pin, pin)
		if werr != nil {
			return fmt.Errorf("writing config: %w", werr)
		}
		before, after := cw.Before, cw.After
		undos = append(undos, func() error {
			return config.RestoreUnderLock(c.GalePath, before, after)
		})
	}
	if err := finalizeFetch(ctx, c, fetchPublish{
		Lock:    draft,
		Arts:    arts,
		ToStore: installToStore,
	}); err != nil {
		var restore error
		for i := len(undos) - 1; i >= 0; i-- {
			restore = errors.Join(restore, undos[i]())
		}
		return errors.Join(err, restore)
	}
	return nil
}

// isGitHash returns true if s looks like a git short hash
// (7+ hex characters with no dots or non-hex characters).
// This distinguishes git hashes from semver versions like
// "1.7.1" when comparing installed vs remote versions.
func isGitHash(s string) bool {
	if len(s) < 7 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') &&
			(c < 'a' || c > 'f') &&
			(c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// droppedAttestations names the updated packages whose incoming
// lock entry loses an attestation the existing entry carried,
// per platform. A platform row that disappears entirely counts:
// the requirement is gone either way.
func droppedAttestations(
	existing, draft *lockfile.V2, names []string,
) []string {
	if existing == nil {
		return nil
	}
	var dropped []string
	for _, n := range names {
		oldPkg := v2PkgForName(existing, n)
		newPkg := v2PkgForName(draft, n)
		if oldPkg == nil || newPkg == nil {
			continue
		}
		for _, plat := range v2SortedPlatforms(oldPkg) {
			oldArt := oldPkg.Artifacts[plat]
			if oldArt.Attestation == nil {
				continue
			}
			newArt, ok := newPkg.Artifacts[plat]
			if !ok || newArt.Attestation == nil {
				dropped = append(dropped, n+" ("+plat+")")
			}
		}
	}
	slices.Sort(dropped)
	return dropped
}

func v2PkgForName(lf *lockfile.V2, name string) *lockfile.V2Package {
	if lf == nil {
		return nil
	}
	for key, pkg := range lf.Packages {
		if n, _, err := lockfile.SplitV2Root(key); err == nil && n == name {
			p := pkg
			return &p
		}
	}
	return nil
}

func v2SortedPlatforms(pkg *lockfile.V2Package) []string {
	out := make([]string, 0, len(pkg.Artifacts))
	for plat := range pkg.Artifacts {
		out = append(out, plat)
	}
	slices.Sort(out)
	return out
}

func init() {
	updateCmd.Flags().StringVar(&updateIndex, "index", "",
		"Resolve against a local index checkout")
	updateCmd.Flags().BoolVar(&allowAttestationDrop,
		"allow-attestation-drop", false,
		"Accept an update that drops a locked attestation (warns)")
	updateCmd.Flags().BoolVarP(&updateGlobal, "global", "g",
		false, "Update global packages")
	updateCmd.Flags().BoolVarP(&updateProject, "project", "p",
		false, "Update project packages")
	rootCmd.AddCommand(updateCmd)
}
