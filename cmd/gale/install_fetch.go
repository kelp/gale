package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/index"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/store"
)

var errSwitchHosts = errors.New("gale refuses host overlays")

// installToStore is a test hook. Production stays nil.
var installToStore func(context.Context, *store.Store, string, string, index.Artifact) (string, error)

func refuseSwitchHosts(host string, galePath string) error {
	if host != "" {
		return errSwitchHosts
	}
	cfg, err := rawGaleConfig(galePath)
	if err != nil {
		return err
	}
	if err := refuseHostOverlays(cfg); err != nil {
		return errSwitchHosts
	}
	return nil
}

func runInstallFetch(
	ctx context.Context, c *cmdContext, name, pin string, src index.Source,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := refuseSwitchHosts(c.Host, c.GalePath); err != nil {
		return err
	}
	lp, err := lockfilePath(c.GalePath)
	if err != nil {
		return err
	}
	existing, err := readExistingV2(lp)
	if err != nil {
		return err
	}
	if pin == "" {
		pin = "latest"
	}
	if dryRun {
		out := newOutput()
		out.Info(fmt.Sprintf("install %s", name))
		return nil
	}
	draft, arts, err := planAdopt(ctx, src, map[string]string{name: pin})
	if err != nil {
		return err
	}
	if err := refuseMixedV2(existing); err != nil {
		return err
	}
	draft = mergeV2LockNames(existing, draft, []string{name})
	cw, err := c.writeConfigWitnessed(name, pinForManifest(draft, name), pinForManifest(draft, name))
	if err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	if err := finalizeFetch(ctx, c, fetchPublish{
		Lock:    draft,
		Arts:    arts,
		ToStore: installToStore,
	}); err != nil {
		return errors.Join(err, config.RestoreUnderLock(c.GalePath, cw.Before, cw.After))
	}
	newOutput().Success(fmt.Sprintf("Installed %s", name))
	return nil
}

func readExistingV2(lp string) (*lockfile.V2, error) {
	lf, err := lockfile.ReadV2(lp)
	if err == nil {
		if len(lf.Targets.Host) > 0 {
			return nil, errSwitchHosts
		}
		return lf, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return &lockfile.V2{Version: lockfile.SchemaV2}, nil
	}
	if errors.Is(err, lockfile.ErrUnknownVersion) ||
		errors.Is(err, lockfile.ErrLegacySchema) {
		return nil, fmt.Errorf("%w: run gale fetch-adopt", errVerifyV1)
	}
	// v1 ReadV2 reports unknown version
	if v, lerr := lockfile.Load(lp); lerr == nil {
		switch v.Kind {
		case lockfile.KindAbsent:
			return &lockfile.V2{Version: lockfile.SchemaV2}, nil
		case lockfile.KindV2:
			return v.V2, nil
		case lockfile.KindV1, lockfile.KindLegacy:
			return nil, fmt.Errorf("%w: run gale fetch-adopt", errVerifyV1)
		}
	}
	return nil, err
}

func mergeV2Lock(existing, incoming *lockfile.V2, name string) *lockfile.V2 {
	return mergeV2LockNames(existing, incoming, []string{name})
}

func pinForManifest(lf *lockfile.V2, name string) string {
	if lf == nil || lf.Targets.Default == nil {
		return ""
	}
	for _, root := range lf.Targets.Default.Roots {
		n, ver, err := lockfile.SplitV2Root(root)
		if err == nil && n == name {
			return ver
		}
	}
	return ""
}
