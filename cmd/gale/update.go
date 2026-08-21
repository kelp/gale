package main

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/gitutil"
	"github.com/kelp/gale/internal/index"
	"github.com/kelp/gale/internal/output"
	ver "github.com/kelp/gale/internal/version"
	"github.com/spf13/cobra"
)

var (
	updateRecipes   string
	updatePath      string
	updateGit       bool
	updateRecipe    string
	updateBuild     bool
	updateNoRefresh bool
	updateNoInstall bool
	updateIndex     string
	updateGlobal    bool
	updateProject   bool
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
		return runUpdateFetch(ctx, c, args, indexSource(updateIndex))
	},
}

func runUpdateFetch(
	ctx context.Context, c *cmdContext, args []string, src index.Source,
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

func leftoverUpdateHelpers() {
	// Source-era helpers stay until Delete the long tail.
	_ = updateRecipes
	_ = updatePath
	_ = updateGit
	_ = updateRecipe
	_ = updateBuild
	_ = updateNoRefresh
	_ = updateNoInstall
}

func finishUpdate(dryRun bool, failed int, updated int, rebuild func() error) error {
	if dryRun {
		return nil
	}
	if updated == 0 && failed == 0 {
		return nil // nothing changed — skip rebuild
	}
	rebuildErr := rebuild()
	if failed > 0 {
		if rebuildErr != nil {
			return fmt.Errorf("%d package(s) could not be updated; rebuild: %w",
				failed, rebuildErr)
		}
		return fmt.Errorf("%d package(s) could not be updated", failed)
	}
	return rebuildErr
}

// updateFromGit checks if the remote HEAD changed, and
// rebuilds from git if so.
func updateFromGit(name string, ctx *cmdContext, out *output.Output) error {
	r, err := ctx.Resolver(context.Background(), name)
	if err != nil {
		return fmt.Errorf("fetching recipe: %w", err)
	}
	if r.Source.Repo == "" {
		return fmt.Errorf(
			"recipe for %s has no source.repo", name,
		)
	}

	// Check remote HEAD.
	remoteHash, err := gitutil.RemoteHead(
		r.Source.Repo, r.Source.Branch,
	)
	if err != nil {
		return fmt.Errorf("checking remote: %w", err)
	}

	// Compare to installed version.
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}

	installed := cfg.Packages[name]
	if isGitHash(installed) && installed == remoteHash {
		out.Success(fmt.Sprintf(
			"%s@%s is up to date", name, remoteHash,
		))
		return nil
	}

	out.Info(fmt.Sprintf("Updating %s to %s...",
		name, remoteHash))
	return installFromGit(ctx, name, updateRecipe, out)
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

// updateAction returns the version to install and whether
// the update should be skipped. When the registry version
// matches the current version AND the package exists in the
// store, skip is true. When the store entry is missing,
// skip is false and version is the current version
// (reinstall). When the registry is newer, skip is false
// and version is the new version.
func updateAction(
	candidate, current string,
	inStore bool,
) (version string, skip bool) {
	newer := ver.IsNewer(candidate, current)
	if !newer && inStore {
		return current, true
	}
	if newer {
		return candidate, false
	}
	return current, false
}

// noInstallPin returns the version string to write to
// gale.toml under --no-install. Pins stay bare by
// convention, but for a revision-only bump (same upstream
// version, higher recipe revision) the bare pin is
// byte-identical to the current one — the promised
// follow-up `gale sync` would resolve it to the
// already-installed revision and silently skip the new
// one (#66). Writing the canonical `<version>-<revision>`
// makes the drift detectable; sync already handles
// versioned pins. The strict-newer guard keeps the
// reinstall-current path (store entry missing, same
// version) writing the bare pin unchanged.
func noInstallPin(bare, full, current string) string {
	if stripNumericRevision(current) == bare &&
		ver.IsNewer(full, current) {
		return full
	}
	return bare
}

// sortedTargetKeys returns a sorted copy of the input
// slice. Used to ensure deterministic iteration order
// over update targets.
func sortedTargetKeys(keys []string) []string {
	sorted := make([]string, len(keys))
	copy(sorted, keys)
	sort.Strings(sorted)
	return sorted
}

func init() {
	updateCmd.Flags().StringVar(&updateIndex, "index", "",
		"Resolve against a local index checkout")
	updateCmd.Flags().BoolVarP(&updateGlobal, "global", "g",
		false, "Update global packages")
	updateCmd.Flags().BoolVarP(&updateProject, "project", "p",
		false, "Update project packages")
	rootCmd.AddCommand(updateCmd)
}
