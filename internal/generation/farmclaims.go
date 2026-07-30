package generation

import (
	"fmt"
	"path/filepath"
	"runtime"
	"slices"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/projects"
	"github.com/kelp/gale/internal/store"
)

// This file collects the claimant set for the farm guard (design
// §4): the scopes whose closures the shared farm must keep
// satisfied. It lives in this package because the unlocked side of
// a claim is the scope's active generation, which only this package
// can read, and internal/farm cannot import it back.
//
// Claimants are the registered projects from the ~/.gale/projects
// walk plus the global scope, which registerProject skips by design
// and a registry-only scan would therefore miss entirely.
// Unregistered projects and already-open shells remain the
// acknowledged limitation until the scoped-farm follow-up.

// FarmClaimants collects every scope that claims sonames in the
// shared farm, excluding the scope at selfGaleDir: the initiating
// scope's claim is its proposed closure, which the caller supplies
// to the guard itself, and passing its old claim here would
// deadlock the scope against its own update, remove and repair.
//
// A scope's claim source depends on its lock. A v1 lock is the
// authority on what the scope requires, so the claim is the lock's
// recorded runtime closure — which must hold even while the scope
// is mid-sync or not yet installed. Without a v1 lock (absent or
// legacy — a legacy lock predates enforcement and records no
// closure), the scope requires whatever it actually links, so the
// claim is the active generation's farm closure. A scope that is
// known but cannot be read is returned with Err set, and the guard
// fails closed on it. Scopes with nothing to claim are dropped.
func FarmClaimants(storeRoot, selfGaleDir string) []farm.Claimant {
	galeHome := filepath.Dir(storeRoot)
	regProjects, err := projects.List(galeHome)
	if err != nil {
		// The registry names scopes the walk cannot see without it,
		// so an unreadable registry fails the guard closed rather
		// than silently shrinking the claimant set.
		return []farm.Claimant{{Label: "the project registry", Err: err}}
	}

	scopes := []struct {
		label, galeDir, lockPath string
	}{{
		label:    "the global scope",
		galeDir:  galeHome,
		lockPath: filepath.Join(galeHome, "gale.lock"),
	}}
	for _, proj := range regProjects {
		scopes = append(scopes, struct {
			label, galeDir, lockPath string
		}{
			label:    "project " + proj,
			galeDir:  filepath.Join(proj, ".gale"),
			lockPath: filepath.Join(proj, "gale.lock"),
		})
	}

	host := config.CurrentHost()
	platform := runtime.GOOS + "/" + runtime.GOARCH
	var out []farm.Claimant
	for _, s := range scopes {
		if samePath(s.galeDir, selfGaleDir) {
			continue
		}
		dirs, err := scopeClosureDirs(
			s.lockPath, s.galeDir, storeRoot, host, platform,
		)
		if err != nil {
			out = append(out, farm.Claimant{Label: s.label, Err: err})
			continue
		}
		if len(dirs) > 0 {
			out = append(out, farm.Claimant{
				Label: s.label, StoreDirs: dirs,
			})
		}
	}
	return out
}

// scopeClosureDirs resolves one scope's claimed closure to store
// dirs, choosing the claim source by the scope's lock kind.
func scopeClosureDirs(
	lockPath, galeDir, storeRoot, host, platform string,
) ([]string, error) {
	view, err := lockfile.Load(lockPath)
	if err != nil {
		return nil, fmt.Errorf("reading lock: %w", err)
	}
	switch view.Kind {
	case lockfile.KindV1:
		ids, err := lockRuntimeClosure(view.V1, host, platform)
		if err != nil {
			return nil, err
		}
		return identityStoreDirs(storeRoot, ids)
	case lockfile.KindAbsent, lockfile.KindLegacy:
		pkgs, err := CurrentVersions(galeDir, storeRoot)
		if err != nil {
			return nil, fmt.Errorf("reading active generation: %w", err)
		}
		return FarmStoreDirs(pkgs, storeRoot), nil
	default:
		// A kind this build does not know claims something it
		// cannot read; fail closed like any unreadable scope.
		return nil, fmt.Errorf("unknown lock kind %s", view.Kind)
	}
}

// lockRuntimeClosure walks the lock's recorded runtime edges from
// the effective roots and returns every canonical identity
// reachable, sorted.
//
// Runtime edges only: farm links exist so binaries resolve the
// dylibs they load at run time, and the farm mapping generations
// maintain (FarmStoreDirs) walks the recorded runtime closure
// alone. Build deps never enter the farm, so claiming them would
// refuse operations over sonames no claimant binary loads.
//
// A node without an artifact for this platform contributes nothing
// and is not followed: platform minting is all-or-nothing (design
// §10), so a missing artifact means the lock was not minted for
// this platform and the scope cannot activate this closure here. A
// root or edge naming a node the lock does not define is a
// malformed lock and fails closed.
func lockRuntimeClosure(lf *lockfile.V1, host, platform string) ([]string, error) {
	roots, err := lf.EffectiveRoots(host)
	if err != nil {
		return nil, err
	}
	queue := make([]string, 0, len(roots))
	for _, id := range roots {
		queue = append(queue, id)
	}
	slices.Sort(queue)

	visited := map[string]bool{}
	var out []string
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if visited[id] {
			continue
		}
		visited[id] = true
		pkg, ok := lf.Packages[id]
		if !ok {
			return nil, fmt.Errorf("%w: %s", lockfile.ErrMissingNode, id)
		}
		art, minted := pkg.Artifacts[platform]
		if !minted {
			continue
		}
		out = append(out, id)
		queue = append(queue, art.RuntimeDeps...)
	}
	slices.Sort(out)
	return out, nil
}

// identityStoreDirs resolves canonical identities to store dirs.
// Identities come from lock dep lists, which nothing validated yet,
// so a malformed one is refused rather than looked up: a missed
// lookup is indistinguishable from a package with no dylibs.
func identityStoreDirs(storeRoot string, ids []string) ([]string, error) {
	st := store.NewStore(storeRoot)
	dirs := make([]string, 0, len(ids))
	for _, id := range ids {
		name, version, err := lockfile.ParseIdentity(id)
		if err != nil {
			return nil, err
		}
		dirs = append(dirs, st.ResolveDir(name, version))
	}
	return dirs, nil
}

// guardedRebuildDirs is the farm-guard step every rebuild boundary
// runs before its swap: it collects the claimant set and returns
// the store dirs the farm rebuild must use — the proposed closure
// plus every claim, so a wipe-and-recreate rebuild cannot delete a
// soname only another scope claims. A conflicting claim refuses
// the whole operation, which is why callers must run this BEFORE
// swapping the generation: after a refusal nothing may have moved.
//
// Only the shared farm is guarded. A project-scoped galeDir owns a
// project-local lib dir that no binary rpath resolves through
// (rpaths are derived from store dirs, which live under the shared
// gale home), so mutating it cannot violate any scope's claim.
func guardedRebuildDirs(
	pkgs map[string]string, galeDir, storeRoot string,
) ([]string, error) {
	proposed := FarmStoreDirs(pkgs, storeRoot)
	if !samePath(galeDir, filepath.Dir(storeRoot)) {
		return proposed, nil
	}
	return farm.GuardRebuild(proposed, FarmClaimants(storeRoot, galeDir))
}

// samePath reports whether two paths name the same location,
// resolving symlinks first so macOS /var vs /private/var spellings
// compare equal (the rule cmd/gale's sameDir applies). Resolution
// walks up to the nearest existing ancestor because the compared
// dirs may not exist yet — a project's .gale before its first sync
// — and EvalSymlinks fails outright on a missing path, which would
// make the two spellings of the initiating scope compare unequal
// and turn its own superseded claim into a self-veto.
func samePath(a, b string) bool {
	return resolveExisting(filepath.Clean(a)) ==
		resolveExisting(filepath.Clean(b))
}

// resolveExisting resolves symlinks in the longest existing prefix
// of a cleaned path and rejoins the missing tail unchanged.
func resolveExisting(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	parent := filepath.Dir(path)
	if parent == path {
		return path // filesystem root; nothing left to resolve
	}
	return filepath.Join(resolveExisting(parent), filepath.Base(path))
}
