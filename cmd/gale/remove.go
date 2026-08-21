package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kelp/gale/internal/atomicfile"
	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/filelock"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/store"
	"github.com/spf13/cobra"
)

var (
	removeGlobal  bool
	removeProject bool
)

var removeCmd = &cobra.Command{
	Use:   "remove <package>",
	Short: "Remove a package",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateScopeFlags(removeGlobal, removeProject); err != nil {
			return err
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

		c, err := newCmdContext("", removeGlobal, removeProject)
		if err != nil {
			return err
		}
		bg := cmd.Context()
		if bg == nil {
			bg = context.Background()
		}
		return runRemoveFetch(bg, c, name, out)
	},
}

func runRemoveFetch(
	ctx context.Context, c *cmdContext, name string, out *output.Output,
) error {
	if err := refuseSwitchHosts(c.Host, c.GalePath); err != nil {
		return err
	}
	cfg, err := c.LoadConfig()
	if err != nil {
		return err
	}
	version, ok := cfg.Packages[name]
	if !ok {
		return fmt.Errorf("%s is not in %s", name, c.GalePath)
	}
	if dryRun {
		out.Info(fmt.Sprintf("remove %s@%s", name, version))
		return nil
	}
	lp, err := lockfilePath(c.GalePath)
	if err != nil {
		return err
	}
	view, err := lockfile.Load(lp)
	if err != nil {
		return err
	}
	prior, wrote, err := config.RemovePackageSections(
		c.GalePath, locatePackageSections(c.GalePath, name), name,
	)
	if err != nil {
		return fmt.Errorf("removing from config: %w", err)
	}
	w := removeWork{c: c, name: name, version: version, out: out, prior: prior, wrote: wrote}
	switch view.Kind {
	case lockfile.KindAbsent:
		if err := w.unlocked(ctx); err != nil {
			return err
		}
	case lockfile.KindLegacy, lockfile.KindV1:
		return errors.Join(
			errSwitchV1,
			config.RestoreUnderLock(c.GalePath, prior, wrote),
		)
	case lockfile.KindV2:
		if err := w.v2(view.V2); err != nil {
			return err
		}
	default:
		return errors.Join(
			fmt.Errorf("unhandled lockfile kind %s", view.Kind),
			config.RestoreUnderLock(c.GalePath, prior, wrote),
		)
	}
	out.Info(fmt.Sprintf("Removed %s from %s", name, c.GalePath))
	_ = ctx
	return nil
}

type removeWork struct {
	c             *cmdContext
	name, version string
	out           *output.Output
	prior, wrote  config.FileState
}

func (w removeWork) undo(err error) error {
	return errors.Join(err, config.RestoreUnderLock(w.c.GalePath, w.prior, w.wrote))
}

func (w removeWork) unlocked(ctx context.Context) error {
	st := store.NewStore(w.c.StoreRoot)
	var dropStoreDir string
	if st.IsInstalled(w.name, w.version) {
		var planErr error
		dropStoreDir, planErr = storeRemovalPlan(ctx, w.c, st, w.name, w.version, w.out)
		if planErr != nil {
			return w.undo(planErr)
		}
	} else {
		w.out.Warn(fmt.Sprintf("%s@%s not found in store", w.name, w.version))
	}
	if err := rebuildGeneration(w.c.GaleDir, w.c.StoreRoot, w.c.GalePath, nil); err != nil {
		return w.undo(fmt.Errorf("rebuild generation: %w", err))
	}
	if dropStoreDir == "" {
		return nil
	}
	if err := dropFromStore(w.c, w.name, w.version, w.out); err != nil {
		return errors.Join(w.undo(err), w.c.RebuildGeneration())
	}
	return nil
}

func (w removeWork) v2(lf *lockfile.V2) error {
	if len(lf.Targets.Host) > 0 {
		return w.undo(errSwitchHosts)
	}
	if err := refuseMixedV2(lf); err != nil {
		return w.undo(err)
	}
	draft := dropV2Root(lf, w.name)
	if err := writeV2Only(w.c, draft); err != nil {
		return w.undo(fmt.Errorf("writing lock: %w", err))
	}
	if err := rebuildFromV2(w.c, draft); err != nil {
		return fmt.Errorf("rebuild generation: %w", err)
	}
	return nil
}

func init() {
	removeCmd.Flags().BoolVarP(&removeGlobal, "global", "g",
		false, "Remove from global config")
	removeCmd.Flags().BoolVarP(&removeProject, "project", "p",
		false, "Remove from project config")
	rootCmd.AddCommand(removeCmd)
}

// guardDepopulate runs the cross-project farm claimant guard for a
// store dir about to lose its farm entries AND be deleted. It goes
// through GuardStoreRemoval rather than GuardDepopulate because
// those are two mutations: the generation rebuild at line 185 can
// already have wiped the farm link by the time this runs, and a
// guard that only reads the live farm would then approve deleting a
// directory another scope still claims.
//
// Claimants are the other scopes plus the initiating scope's own
// resulting closure. Without the second, removing a package another
// package in THIS scope still links deletes the farm entry that
// dependent resolves through: the reference scan only reads
// declared pins, so a package that is nobody's root and somebody's
// dependency reads as unreferenced. Its OLD closure is never
// claimed, or the scope would veto its own remove.
// dropFromStore performs the store deletion inside ONE hold of
// the generation lock, nested inside the version lock.
//
// The earlier call in storeRemovalPlan cannot serve on its own. It
// runs before the generation rebuild, which takes and releases this
// same lock, so between that check and this depopulate another
// scope can establish a claim on the soname and have it silently
// deleted. A check is only worth what it is atomic with, so the
// authoritative one lives here beside the farm mutation; the early
// call remains because a refusal must land before the generation
// swap, not after it.
//
// The two locks are taken in the installer's order, version lock
// then generation lock, and that ordering is load-bearing. The
// version lock is the same file lockPackage holds for the whole of
// an install, and an install takes the generation lock underneath
// it at commitStaged. Taking them the other way round closes an
// AB-BA cycle on two blocking flock calls: `gale install foo` waits
// for the generation lock while holding the package lock, `gale
// remove foo` waits for the package lock while holding the
// generation lock, and neither process ever wakes.
//
// Holding the version lock across the whole section is what makes
// the deletion safe, not merely deadlock-free. Released early, this
// interleaves: remove guards and depopulates, an installer commits
// a fresh copy of that exact version and repopulates the farm, and
// remove then deletes the directory the install just committed,
// leaving farm links pointing into nothing. The generation lock
// alone cannot prevent that, because the installer's commit is
// itself the thing holding the version lock.
func dropFromStore(
	ctx *cmdContext, name, version string, out *output.Output,
) error {
	st := store.NewStore(ctx.StoreRoot)
	err := st.RemoveWithin(name, version,
		func(dir string, drop func() error) error {
			return filelock.With(genLockPath(ctx.StoreRoot), func() error {
				if beforeGuardedRemoval != nil {
					beforeGuardedRemoval()
				}
				return drop()
			})
		})
	if err != nil {
		return fmt.Errorf("removing from store: %w", err)
	}
	return nil
}

// beforeGuardedRemoval runs inside dropFromStore's critical section,
// before the authoritative guard. Test seam only (nil in production,
// like renameDir in the installer): it lets a test establish a
// claimant in the window the early check cannot cover, which is the
// race the second check exists to close, and observe that both
// locks are held together there.
var beforeGuardedRemoval func()

// storeRemovalPlan decides whether the store entry for an
// installed package will be removed, and when it will, runs the
// cross-project farm guard over the depopulation that removal
// implies. Returns the resolved store dir to remove, or "" when
// another scope's gale.toml still references it — the store is
// shared, and deleting an entry the global config (or an enclosing
// project's) still lists would leave that scope's generation
// symlinks dangling without warning (gh#67).
//
// The dir is resolved to the canonical on-disk form (<v>-<rev>)
// once. st.Remove resolves internally, but farm.Depopulate
// prefix-matches symlink targets against this path, so the bare
// config version made it a guaranteed no-op that leaked farm
// entries (gh#74).
//
// This is the early guard call: it decides, and it runs before the
// generation swap so a refusal lands before anything moves. The
// authoritative one is dropFromStore's, beside the deletion.
func storeRemovalPlan(
	parent context.Context, ctx *cmdContext, st *store.Store, name, version string,
	out *output.Output,
) (string, error) {
	storeDir := st.ResolveDir(name, version)
	if otherScopeReferences(
		st, name, storeDir, versionedRecipeResolverWith(ctx, parent), out,
	) {
		out.Info(fmt.Sprintf(
			"%s@%s still referenced by another "+
				"gale.toml — keeping store entry",
			name, version,
		))
		return "", nil
	}
	return storeDir, nil
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
func locatePackageSections(configPath, name string) []string {
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
	referenced, err := collectReferencedPackagesAllHosts(
		globalDir, projPath, st, pinResolve,
	)
	if err != nil {
		// Fail closed. An unreadable config hides pins; it does
		// not prove there are none, and the deletion this guards
		// is irreversible (gh#188).
		out.Warn(fmt.Sprintf(
			"keeping %s: %v", storeDir, err,
		))
		return true
	}
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

// restoreLock puts the lockfile back to what it was before this
// command wrote it, but only while the file is still exactly what
// this command left there.
//
// Both tokens come from inside WriteLock's critical section
// (LockWitness), so "still ours" is a real claim rather than an
// optimistic one. When it does not hold, another command owns the
// file now: undoing our write by discarding theirs trades one
// inconsistency for a worse one, because theirs is the state the
// machine is actually in. The restore then stands down and reports
// which file it deliberately left alone.
func restoreLock(w LockWitness) error {
	if w.Path == "" {
		return nil
	}
	return filelock.With(w.Path+".lock", func() error {
		current, err := readFileSnapshot(w.Path)
		if err != nil {
			return err
		}
		if !current.Same(w.After) {
			return fmt.Errorf(
				"%s changed since this command wrote it, so it was "+
					"left as it is", w.Path,
			)
		}
		return applySnapshot(w.Path, w.Before)
	})
}

// applySnapshot writes a snapshot back, restoring absence by
// removing rather than by writing an empty file. The two are
// different states: an empty gale.lock names no roots, which is the
// stale-lock state against a manifest that still declares packages,
// so a compensation that invented one would leave the project worse
// than the operation it is undoing.
func applySnapshot(path string, snap FileSnapshot) error {
	if !snap.Exists {
		if err := os.Remove(path); err != nil &&
			!errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("restoring absent %s: %w", path, err)
		}
		return nil
	}
	if err := atomicfile.Write(path, snap.Bytes); err != nil {
		return fmt.Errorf("restoring %s: %w", path, err)
	}
	return nil
}
