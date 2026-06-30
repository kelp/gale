package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/store"
	"github.com/spf13/cobra"
)

var (
	removeGlobal  bool
	removeProject bool
	removeHost    string
)

var removeCmd = &cobra.Command{
	Use:   "remove <package>",
	Short: "Remove a package",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if removeGlobal && removeProject {
			return fmt.Errorf(
				"cannot use both --global and --project",
			)
		}

		name := args[0]

		// gale manages itself via the bootstrap, not
		// through its own `remove` command. Letting the
		// user nuke the active binary from its own store
		// is a footgun with no non-trivial recovery: the
		// PATH entry disappears, direnv can't reload the
		// hook, and the only way back is a fresh bootstrap.
		if name == "gale" {
			return fmt.Errorf(
				"refusing to remove gale itself — " +
					"use the bootstrap script " +
					"(`just upgrade` from the umbrella) to " +
					"manage the active install",
			)
		}

		out := newCmdOutput(cmd)

		ctx, err := newCmdContext("", removeGlobal, removeProject)
		if err != nil {
			return err
		}

		st := store.NewStore(ctx.StoreRoot)

		// Look up the package. With --host, membership and
		// the pinned version come from the targeted host's
		// section of the raw file — the effective config
		// flattens to the *current* host's view, which hides
		// foreign-host entries and reports the wrong version
		// when the pins differ (gh#75).
		host := resolveHostFlag(removeHost)
		var version string
		if host != "" {
			raw, err := rawGaleConfig(ctx.GalePath)
			if err != nil {
				return err
			}
			v, ok := raw.Hosts[host].Packages[name]
			if !ok {
				return fmt.Errorf(
					"%s is not in [hosts.%s.packages] in %s",
					name, host, ctx.GalePath,
				)
			}
			version = v
		} else {
			cfg, err := ctx.LoadConfig()
			if err != nil {
				return err
			}
			v, ok := cfg.Packages[name]
			if !ok {
				return fmt.Errorf(
					"%s is not in %s", name, ctx.GalePath,
				)
			}
			version = v
		}

		if dryRun {
			out.Info(fmt.Sprintf(
				"remove %s@%s", name, version,
			))
			return nil
		}

		// Update config first so a failed write does not
		// leave the store missing but config still listing
		// the package. With --host, only that section is
		// touched. Without --host, sweep every section that
		// lists the package (shared + any host overlays) —
		// otherwise a host overlay can survive the remove,
		// leaving gale.toml referencing a package whose
		// store dir we're about to delete.
		var sections []string
		if host != "" {
			sections = []string{host}
		} else {
			sections = locatePackageSections(
				ctx.GalePath, name, config.CurrentHost(),
			)
		}
		for _, section := range sections {
			if err := config.RemovePackage(
				ctx.GalePath, section, name,
			); err != nil {
				return fmt.Errorf("removing from config: %w",
					err)
			}
		}
		out.Info(fmt.Sprintf(
			"Removed %s from %s (%s)",
			name, ctx.GalePath,
			formatSections(sections),
		))

		// Remove from lockfile. Warn on error but continue
		// since the main operation (config + store) succeeded.
		if err := ctx.RemoveLockEntry(name); err != nil {
			out.Warn(fmt.Sprintf(
				"Failed to update lockfile: %v", err,
			))
		}

		// Rebuild the generation for this scope. Do this
		// before removing from the store so the generation
		// is updated with the new config (package removed)
		// before we delete the package from the store.
		if err := ctx.RebuildGeneration(); err != nil {
			return fmt.Errorf("rebuild generation: %w", err)
		}

		// Remove the declared version from the store —
		// unless another scope's gale.toml still references
		// it. The store is shared: deleting an entry the
		// global config (or an enclosing project's) still
		// lists would leave that scope's generation symlinks
		// dangling without warning (gh#67).
		if st.IsInstalled(name, version) {
			// Resolve the canonical on-disk dir (<v>-<rev>)
			// once. st.Remove resolves internally, but
			// farm.Depopulate prefix-matches symlink targets
			// against this path, so the bare config version
			// made it a guaranteed no-op that leaked farm
			// entries (gh#74).
			storeDir := st.ResolveDir(name, version)
			if otherScopeReferences(
				st, name, storeDir, ctx.versionedRecipeResolver(), out,
			) {
				out.Info(fmt.Sprintf(
					"%s@%s still referenced by another "+
						"gale.toml — keeping store entry",
					name, version,
				))
			} else {
				if err := st.Remove(name, version); err != nil {
					return fmt.Errorf("removing from store: %w",
						err)
				}
				// Clean up farm symlinks pointing into the
				// removed store dir. Best-effort; a failure
				// here leaves stale symlinks that `gale
				// inspect` would surface.
				if farmDir := farm.DirFromStoreDir(storeDir); farmDir != "" {
					if err := farm.Depopulate(storeDir, farmDir); err != nil {
						out.Warn(fmt.Sprintf(
							"farm depopulate: %v", err,
						))
					}
				}
				out.Info(fmt.Sprintf("Removed %s@%s from store",
					name, version))
			}
		} else {
			out.Warn(fmt.Sprintf(
				"%s@%s not found in store", name, version,
			))
		}

		out.Success(fmt.Sprintf("Removed %s", name))
		return nil
	},
}

func init() {
	removeCmd.Flags().BoolVarP(&removeGlobal, "global", "g",
		false, "Remove from global config")
	removeCmd.Flags().BoolVarP(&removeProject, "project", "p",
		false, "Remove from project config")
	removeCmd.Flags().StringVar(&removeHost, "host", "",
		"Remove from [hosts.<host>.packages] "+
			"(use 'current' for this machine)")
	rootCmd.AddCommand(removeCmd)
}

// locatePackageSections returns every section in the
// gale.toml at configPath that lists name. Sections are
// encoded as "" for shared [packages] and "<hostname>" for
// each matching [hosts.<hostname>.packages]. Returns nil if
// the package is absent (caller has already verified via
// the effective config, so this can only happen on a parse
// failure). Order is deterministic: shared first, then host
// names sorted alphabetically.
//
// `gale remove` calls this so a single invocation cleans
// both shared and host-overlay entries. Removing only one
// leaves the package in the effective config while we
// delete its store dir — see TestRemoveCleansHostOverlayAndShared.
// `current` is accepted for future preference rules but is
// not used: every match must be cleared regardless.
func locatePackageSections(configPath, name, _ string) []string {
	cfg, err := rawGaleConfig(configPath)
	if err != nil {
		return nil
	}

	var sections []string
	if _, inShared := cfg.Packages[name]; inShared {
		sections = append(sections, "")
	}
	hostNames := make([]string, 0, len(cfg.Hosts))
	for host := range cfg.Hosts {
		hostNames = append(hostNames, host)
	}
	sort.Strings(hostNames)
	for _, host := range hostNames {
		if _, has := cfg.Hosts[host].Packages[name]; has {
			sections = append(sections, host)
		}
	}
	return sections
}

// rawGaleConfig parses the gale.toml at path without
// applying host overlays, so per-host sections stay visible.
// LoadConfig flattens to the current host's view via
// ApplyHost, which hides entries declared for other hosts.
func rawGaleConfig(path string) (*config.GaleConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return cfg, nil
}

// otherScopeReferences reports whether any gale.toml still
// references the store dir about to be deleted. The current
// scope's entry is already removed from disk by the time
// this runs, so any hit comes from the other scope (global
// vs project) or a surviving section. Uses the host-union
// collector (collectReferencedPackagesAllHosts) so pins
// under any [hosts.*.packages] overlay count as references
// — flattening to the current host's view would hide them
// and delete a store entry another host still needs. Bare
// config versions resolve to the same canonical <v>-<rev>
// key as storeDir via storeRetentionKey (recipe-canonical when
// a resolver is available).
func otherScopeReferences(
	st *store.Store, name, storeDir string,
	pinResolve versionedRecipeResolver,
	out *output.Output,
) bool {
	globalDir, err := galeConfigDir()
	if err != nil {
		globalDir = ""
	}
	var projPath string
	if cwd, err := os.Getwd(); err == nil {
		projPath, _ = config.FindGaleConfig(cwd)
	}
	referenced := collectReferencedPackagesAllHosts(
		globalDir, projPath, st, pinResolve, out,
	)
	return referenced[name+"@"+filepath.Base(storeDir)]
}

// formatSections renders the section list from
// locatePackageSections for user-facing output.
func formatSections(sections []string) string {
	if len(sections) == 0 {
		return "no sections"
	}
	parts := make([]string, 0, len(sections))
	for _, s := range sections {
		if s == "" {
			parts = append(parts, "shared")
		} else {
			parts = append(parts, "hosts."+s)
		}
	}
	return strings.Join(parts, ", ")
}
