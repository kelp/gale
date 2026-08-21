package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/kelp/gale/internal/fetch"
	"github.com/kelp/gale/internal/filelock"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/index"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/store"
)

// fetchArt is one current-platform artifact to stage.
type fetchArt struct {
	Name    string
	Version string
	Art     index.Artifact
}

// fetchPublish is the unused Phase 1 publisher input. It is not
// the live FinalizeInstall path.
type fetchPublish struct {
	Name    string
	Version string
	Art     index.Artifact
	Arts    []fetchArt
	Lock    *lockfile.V2
	ToStore func(context.Context, *store.Store, string, string, index.Artifact) (string, error)

	afterLock     func() error
	afterStage    func() error
	afterRegister func() error
	afterWrite    func() error
	beforeSwap    func() error
}

// mutateLockPath is the per-scope publication lock.
func mutateLockPath(galeDir string) string {
	return filepath.Join(galeDir, "mutate.lock")
}

// finalizeFetch stages a fetch tree, registers the project,
// writes a v2 lock, and swaps current last. Source install
// stays the only installer.
func finalizeFetch(ctx context.Context, c *cmdContext, p fetchPublish) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	toStore := p.ToStore
	if toStore == nil {
		toStore = fetch.ToStore
	}
	return filelock.With(mutateLockPath(c.GaleDir), func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := runPublishHook(p.afterLock); err != nil {
			return err
		}
		pkgs, err := pkgsFromV2Lock(p.Lock)
		if err != nil {
			return err
		}
		arts := p.Arts
		if len(arts) == 0 {
			arts = []fetchArt{{Name: p.Name, Version: p.Version, Art: p.Art}}
		}
		prev := installToStore
		if toStore != nil {
			installToStore = toStore
		}
		stageErr := landFetchArts(ctx, c.StoreRoot, arts)
		installToStore = prev
		if stageErr != nil {
			return stageErr
		}
		if err := runPublishHook(p.afterStage); err != nil {
			return err
		}
		if err := registerProject(c.GalePath, c.GaleDir); err != nil {
			return fmt.Errorf("registering project: %w", err)
		}
		if err := runPublishHook(p.afterRegister); err != nil {
			return err
		}
		lp, err := lockfilePath(c.GalePath)
		if err != nil {
			return err
		}
		if err := lockfile.WriteV2(lp, p.Lock); err != nil {
			return fmt.Errorf("writing lock: %w", err)
		}
		if err := runPublishHook(p.afterWrite); err != nil {
			return err
		}
		if err := runPublishHook(p.beforeSwap); err != nil {
			return err
		}
		opts := fetchBuildOpts(c, p.Lock)
		if err := generation.BuildWithOptions(
			pkgs, c.GaleDir, c.StoreRoot, opts,
		); err != nil {
			return fmt.Errorf("swapping current: %w", err)
		}
		autoPruneGenerations(c.GaleDir, c.StoreRoot)
		return nil
	})
}

func fetchBuildOpts(c *cmdContext, lf *lockfile.V2) generation.Options {
	opts := generation.Options{
		Fetch: fetchSHAMap(lf),
	}
	if c != nil && c.GalePath != "" {
		if cfg, err := loadEffectiveConfig(c.GalePath); err == nil {
			opts.BinOverrides = cfg.Bin
		}
	}
	return opts
}

func fetchSHAMap(lf *lockfile.V2) map[string]string {
	out := map[string]string{}
	if lf == nil {
		return out
	}
	plat := currentPlatform()
	for key, pkg := range lf.Packages {
		name, _, err := lockfile.SplitV2Root(key)
		if err != nil {
			continue
		}
		art, ok := pkg.Artifacts[plat]
		if !ok || art.SHA256 == "" {
			continue
		}
		out[name] = art.SHA256
	}
	return out
}

func pkgsFromV2Lock(lf *lockfile.V2) (map[string]string, error) {
	pkgs := make(map[string]string)
	if lf == nil || lf.Targets.Default == nil {
		return pkgs, nil
	}
	for _, root := range lf.Targets.Default.Roots {
		name, version, err := lockfile.SplitV2Root(root)
		if err != nil {
			return nil, fmt.Errorf("lock root: %w", err)
		}
		if other, ok := pkgs[name]; ok && other != version {
			return nil, fmt.Errorf(
				"%w: one target roots both %s@%s and %s@%s",
				lockfile.ErrVersionConflict, name, other, name, version,
			)
		}
		pkgs[name] = version
	}
	return pkgs, nil
}

func runPublishHook(fn func() error) error {
	if fn == nil {
		return nil
	}
	if err := fn(); err != nil {
		return fmt.Errorf("publishing: %w", err)
	}
	return nil
}
