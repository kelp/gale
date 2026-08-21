package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/fetch"
	"github.com/kelp/gale/internal/index"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/store"
)

var (
	errSwitchMixed  = errors.New("mixed source/fetch lock")
	errSwitchNoLock = errors.New(
		"gale sync requires a v2 lock; run gale lock, gale install, or gale fetch-adopt",
	)
	errSwitchV1       = errors.New("this lock is v1; run gale fetch-adopt")
	errSwitchOccupied = errors.New(
		"occupied fetch directory disagrees with the lock",
	)
)

func liveToStore() func(context.Context, *store.Store, string, string, index.Artifact) (string, error) {
	if installToStore != nil {
		return installToStore
	}
	return fetch.ToStore
}

func refuseMixedV2(lf *lockfile.V2) error {
	if lf == nil {
		return nil
	}
	for _, key := range slices.Sorted(maps.Keys(lf.Packages)) {
		pkg := lf.Packages[key]
		for _, plat := range slices.Sorted(maps.Keys(pkg.Artifacts)) {
			art := pkg.Artifacts[plat]
			if art.Method != "" && art.Method != provenance.MethodFetch {
				return fmt.Errorf("%w: %s %s method %q",
					errSwitchMixed, key, plat, art.Method)
			}
		}
	}
	return nil
}

func requireLiveV2(lp string) (*lockfile.V2, error) {
	v, err := lockfile.Load(lp)
	if err != nil {
		return nil, err
	}
	switch v.Kind {
	case lockfile.KindAbsent:
		return nil, errSwitchNoLock
	case lockfile.KindLegacy, lockfile.KindV1:
		return nil, errSwitchV1
	case lockfile.KindV2:
		if v.V2 == nil {
			return nil, errSwitchNoLock
		}
		if len(v.V2.Targets.Host) > 0 {
			return nil, errSwitchHosts
		}
		if err := refuseMixedV2(v.V2); err != nil {
			return nil, err
		}
		return v.V2, nil
	default:
		return nil, fmt.Errorf("unhandled lockfile kind %s", v.Kind)
	}
}

func checkV2Declared(lf *lockfile.V2, declared map[string]string) error {
	roots := v2RootVersions(lf)
	var unlocked, orphaned, repinned []string
	for name, want := range declared {
		got, ok := roots[name]
		if !ok {
			unlocked = append(unlocked, name)
			continue
		}
		if !v2PinMatches(got, want) {
			repinned = append(repinned, fmt.Sprintf(
				"%s is declared %s but locked at %s", name, want, got,
			))
		}
	}
	for name := range roots {
		if _, ok := declared[name]; !ok {
			orphaned = append(orphaned, name)
		}
	}
	slices.Sort(unlocked)
	slices.Sort(orphaned)
	slices.Sort(repinned)
	var parts []string
	if len(unlocked) > 0 {
		parts = append(parts, "unlocked "+strings.Join(unlocked, ", "))
	}
	if len(orphaned) > 0 {
		parts = append(parts, "orphaned "+strings.Join(orphaned, ", "))
	}
	parts = append(parts, repinned...)
	if len(parts) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s; run gale lock",
		lockfile.ErrStaleLock, strings.Join(parts, "; "))
}

func v2RootVersions(lf *lockfile.V2) map[string]string {
	out := map[string]string{}
	if lf == nil || lf.Targets.Default == nil {
		return out
	}
	for _, root := range lf.Targets.Default.Roots {
		name, ver, err := lockfile.SplitV2Root(root)
		if err != nil {
			continue
		}
		out[name] = ver
	}
	return out
}

func v2PinMatches(locked, declared string) bool {
	if locked == declared {
		return true
	}
	return stripNumericRevision(declared) == locked
}

func artsFromV2(lf *lockfile.V2) ([]fetchArt, error) {
	if lf == nil {
		return nil, nil
	}
	plat := currentPlatform()
	var arts []fetchArt
	for _, key := range slices.Sorted(maps.Keys(lf.Packages)) {
		name, ver, err := lockfile.SplitV2Root(key)
		if err != nil {
			return nil, err
		}
		art, ok := lf.Packages[key].Artifacts[plat]
		if !ok {
			return nil, fmt.Errorf("%w: %s %s", errVerifyNoPlatform, key, plat)
		}
		if art.Method != "" && art.Method != provenance.MethodFetch {
			return nil, fmt.Errorf("%w: %s %s method %q",
				errSwitchMixed, key, plat, art.Method)
		}
		arts = append(arts, fetchArt{
			Name:    name,
			Version: ver,
			Art:     v2ToIndexArt(art),
		})
	}
	return arts, nil
}

func v2ToIndexArt(a lockfile.V2Artifact) index.Artifact {
	files := make([]index.FileEntry, 0, len(a.Files))
	for _, f := range a.Files {
		files = append(files, index.FileEntry{
			Src: f.Src, Dest: f.Dest, Mode: f.Mode,
		})
	}
	out := index.Artifact{
		URL:        a.URL,
		Format:     a.Format,
		SHA256:     a.SHA256,
		TreeDigest: a.TreeDigest,
		HashSource: a.HashSource,
		Strip:      a.Strip,
		Files:      files,
	}
	if a.Attestation != nil {
		t := true
		out.Attestation = &t
	}
	return out
}

func landFetchArt(
	ctx context.Context, st *store.Store, a fetchArt,
	toStore func(context.Context, *store.Store, string, string, index.Artifact) (string, error),
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dest, err := st.FetchPath(a.Name, a.Version, a.Art.SHA256)
	if err != nil {
		return err
	}
	ok, err := st.FetchExists(a.Name, a.Version, a.Art.SHA256)
	if err != nil {
		return err
	}
	if ok {
		if toStore != nil {
			return nil
		}
		got, derr := provenance.DigestTree(ctx, dest)
		if derr != nil {
			return fmt.Errorf("%w: %s: %w", errSwitchOccupied, dest, derr)
		}
		if got != a.Art.TreeDigest {
			return fmt.Errorf("%w: %s tree digest is %s, want %s",
				errSwitchOccupied, dest, got, a.Art.TreeDigest)
		}
		return nil
	}
	fn := toStore
	if fn == nil {
		fn = liveToStore()
	}
	if _, err := fn(ctx, st, a.Name, a.Version, a.Art); err != nil {
		return fmt.Errorf("staging store: %w", err)
	}
	return nil
}

func landFetchArts(
	ctx context.Context, storeRoot string, arts []fetchArt,
	toStore func(context.Context, *store.Store, string, string, index.Artifact) (string, error),
) error {
	st := store.NewStore(storeRoot)
	for _, a := range arts {
		if err := landFetchArt(ctx, st, a, toStore); err != nil {
			return err
		}
	}
	return nil
}

func mergeV2LockNames(existing, incoming *lockfile.V2, names []string) *lockfile.V2 {
	drop := make(map[string]bool, len(names))
	for _, n := range names {
		drop[n] = true
	}
	if existing == nil || len(existing.Packages) == 0 {
		return incoming
	}
	out := &lockfile.V2{
		Version:  lockfile.SchemaV2,
		Targets:  lockfile.Targets{Default: &lockfile.Target{}},
		Packages: maps.Clone(existing.Packages),
	}
	if out.Packages == nil {
		out.Packages = map[string]lockfile.V2Package{}
	}
	for key := range out.Packages {
		n, _, err := lockfile.SplitV2Root(key)
		if err == nil && drop[n] {
			delete(out.Packages, key)
		}
	}
	kept := make([]string, 0)
	if existing.Targets.Default != nil {
		for _, root := range existing.Targets.Default.Roots {
			n, _, err := lockfile.SplitV2Root(root)
			if err == nil && !drop[n] {
				kept = append(kept, root)
			}
		}
	}
	if incoming != nil && incoming.Targets.Default != nil {
		kept = append(kept, incoming.Targets.Default.Roots...)
		maps.Copy(out.Packages, incoming.Packages)
	}
	out.Targets.Default.Roots = kept
	return out
}

func dropV2Root(lf *lockfile.V2, name string) *lockfile.V2 {
	empty := &lockfile.V2{
		Version: lockfile.SchemaV2,
		Targets: lockfile.Targets{Default: &lockfile.Target{}},
	}
	return mergeV2LockNames(lf, empty, []string{name})
}

func rebuildFromV2(c *cmdContext, lf *lockfile.V2) error {
	pkgs, err := pkgsFromV2Lock(lf)
	if err != nil {
		return err
	}
	return rebuildGenerationWith(genRebuild{
		galeDir:    c.GaleDir,
		storeRoot:  c.StoreRoot,
		configPath: c.GalePath,
		pkgs:       pkgs,
		fetch:      fetchSHAMap(lf),
	})
}

func writeV2Only(c *cmdContext, lf *lockfile.V2) error {
	lp, err := lockfilePath(c.GalePath)
	if err != nil {
		return err
	}
	return lockfile.WriteV2(lp, lf)
}

func declaredPins(cfg *config.GaleConfig) map[string]string {
	if cfg == nil || cfg.Packages == nil {
		return map[string]string{}
	}
	return maps.Clone(cfg.Packages)
}

func runLockLive(ctx context.Context, c *cmdContext, src index.Source) error {
	if err := refuseSwitchHosts(c.Host, c.GalePath); err != nil {
		return err
	}
	cfg, err := rawGaleConfig(c.GalePath)
	if err != nil {
		return err
	}
	declared := declaredForTarget(cfg, "")
	if len(declared) == 0 {
		return noDeclarations(cfg, "", c.GalePath)
	}
	draft, _, err := planAdopt(ctx, src, declared)
	if err != nil {
		return err
	}
	if err := refuseMixedV2(draft); err != nil {
		return err
	}
	return runLockFetch(ctx, c, lockFetch{
		Source: src,
		Roots:  append([]string(nil), draft.Targets.Default.Roots...),
	})
}

func pinsForUpdate(declared map[string]string, args []string) (map[string]string, []string, error) {
	if len(args) == 0 {
		out := make(map[string]string, len(declared))
		names := make([]string, 0, len(declared))
		for name := range declared {
			out[name] = ""
			names = append(names, name)
		}
		slices.Sort(names)
		return out, names, nil
	}
	out := make(map[string]string, len(args))
	names := make([]string, 0, len(args))
	for _, arg := range args {
		name, ver, err := parsePackageArg(arg)
		if err != nil {
			return nil, nil, err
		}
		if _, ok := declared[name]; !ok {
			return nil, nil, fmt.Errorf("%s is not in gale.toml", name)
		}
		out[name] = ver
		names = append(names, name)
	}
	return out, names, nil
}
