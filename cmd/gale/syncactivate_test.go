package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/lockplan"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/recipe"
)

// plannedCtx builds a context whose installer carries a one-node plan
// and whose store already holds that node's directory. Shared by the
// activation tests so each asserts one thing about the same fixture.
func plannedCtx(t *testing.T) (*cmdContext, string) {
	t.Helper()
	tmp := t.TempDir()
	storeRoot := filepath.Join(tmp, "store")
	galeDir := filepath.Join(tmp, ".gale")
	galePath := filepath.Join(tmp, "gale.toml")

	if err := os.WriteFile(galePath,
		[]byte("[packages]\n  jq = \"1.7\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	storeDir := seedStore(t, storeRoot, "jq", "1.7-3")

	ctx := buildFakeCtx(t, galePath, galeDir, storeRoot, nil)
	ctx.Installer.Plan = &lockplan.Plan{
		Nodes: map[string]lockplan.Node{
			"jq@1.7-3": {
				Name: "jq", Version: "1.7-3",
				Method:      lockgraph.MethodBinary,
				GraphDigest: "sha256:" + strings.Repeat("aa", 32),
			},
		},
		Order: []string{"jq@1.7-3"},
		Roots: []string{"jq@1.7-3"},
	}
	return ctx, storeDir
}

// Design §6 steps 2 and 3: a final revalidation of every plan entry
// runs inside the same store-gen lock acquisition that builds and
// swaps the generation, and a failure aborts having mutated nothing.
//
// P8 built `BuildWithValidate` for this and every caller passed nil,
// so the callback existed and never ran. Sync is the only command that
// has a plan, which makes it the only place the window can be closed:
// artifacts verify one at a time, minutes apart under a source build,
// and nothing rechecked them before the swap published the lot.
func TestLockedRebuildRevalidatesBeforeSwapping(t *testing.T) {
	ctx, storeDir := plannedCtx(t)

	// The store directory is present but attests nothing, which is
	// what a directory swapped out from under a verified install looks
	// like.
	if _, err := os.Stat(filepath.Join(storeDir, ".gale-provenance.toml")); !os.IsNotExist(err) {
		t.Fatalf("fixture must start unprovenanced, got %v", err)
	}

	err := ctx.RebuildGenerationLocked()
	if err == nil {
		t.Fatal("locked rebuild: want refusal over an unattested node, got nil")
	}
	if !errors.Is(err, provenance.ErrInvalid) {
		t.Errorf("want ErrInvalid, got %v", err)
	}
	if got := exitCodeFor(err); got != exitLockIntegrity {
		t.Errorf("got exit %d, want %d", got, exitLockIntegrity)
	}

	// Nothing activated. §6: before the swap, a validation failure is
	// fatal and nothing is published.
	if _, err := os.Lstat(filepath.Join(ctx.GaleDir, "current")); !os.IsNotExist(err) {
		t.Errorf("generation was swapped despite a failed revalidation "+
			"(current exists, err=%v)", err)
	}
}

// The locked rebuild takes its versions from the plan, never from a
// re-resolved recipe. `RebuildGenerationCanonical` maps gale.toml's
// bare pins through the recipe resolver, so under a lock it would
// select whatever revision the recipe now names — activating a version
// the lock does not describe, immediately after verifying the one it
// does.
func TestLockedRebuildTakesVersionsFromThePlan(t *testing.T) {
	ctx, _ := plannedCtx(t)

	// A resolver that would answer with a different revision. Under a
	// correct locked rebuild it is never consulted.
	resolved := false
	ctx.Resolver = func(_ context.Context, _ string) (*recipe.Recipe, error) {
		resolved = true
		return minimalRecipe("jq", "9.9"), nil
	}

	pkgs, err := lockedRebuildPackages(ctx.Installer.Plan)
	if err != nil {
		t.Fatalf("lockedRebuildPackages: %v", err)
	}
	if pkgs["jq"] != "1.7-3" {
		t.Errorf("pkgs[jq] = %q, want the locked %q", pkgs["jq"], "1.7-3")
	}
	if resolved {
		t.Error("locked rebuild resolved a recipe to pick a version")
	}
}

// A locked no-op sync must still notice that the ACTIVE generation
// disagrees with the plan. generationDrifted resolves gale.toml's bare
// pins through the recipe resolver, so it answers "no drift" whenever
// the active generation matches the CURRENT recipe — which under a
// lock is the wrong question entirely.
//
// The reachable case, with nothing to install:
//
//	manifest       jq = "1.7"      (bare, so CheckDeclared accepts either)
//	lock           jq@1.7-1
//	active gen     jq@1.7-2
//	recipe now     jq@1.7-2
//
// The locked store entry verifies as a cache hit, so installed and
// failed are both zero. Asking the recipe produces "active matches
// current", finishSync skips the rebuild, and the sync reports success
// while leaving a generation the lock does not describe on PATH.
func TestLockedDriftComparesAgainstThePlanNotTheRecipe(t *testing.T) {
	tmp := t.TempDir()
	storeRoot := filepath.Join(tmp, "store")
	galeDir := filepath.Join(tmp, ".gale")

	// Both revisions exist in the store; the generation links the newer.
	seedStore(t, storeRoot, "jq", "1.7-1")
	seedStore(t, storeRoot, "jq", "1.7-2")
	if err := generation.Build(
		map[string]string{"jq": "1.7-2"}, galeDir, storeRoot,
	); err != nil {
		t.Fatal(err)
	}

	plan := &lockplan.Plan{
		Nodes: map[string]lockplan.Node{
			"jq@1.7-1": {Name: "jq", Version: "1.7-1"},
		},
		Order: []string{"jq@1.7-1"},
		Roots: []string{"jq@1.7-1"},
	}

	if !lockedGenerationDrifted(galeDir, storeRoot, plan) {
		t.Error("active generation links 1.7-2 while the lock names " +
			"1.7-1, but drift was not reported: the sync would skip the " +
			"rebuild and leave an unlocked version active")
	}
}

// The other direction, so the check is not simply "always drifted":
// a generation that already matches the plan needs no rebuild, or
// every no-op locked sync would bump the generation counter.
func TestLockedDriftReportsNoneWhenTheGenerationMatchesThePlan(t *testing.T) {
	tmp := t.TempDir()
	storeRoot := filepath.Join(tmp, "store")
	galeDir := filepath.Join(tmp, ".gale")

	seedStore(t, storeRoot, "jq", "1.7-1")
	if err := generation.Build(
		map[string]string{"jq": "1.7-1"}, galeDir, storeRoot,
	); err != nil {
		t.Fatal(err)
	}

	plan := &lockplan.Plan{
		Nodes: map[string]lockplan.Node{
			"jq@1.7-1": {Name: "jq", Version: "1.7-1"},
		},
		Order: []string{"jq@1.7-1"},
		Roots: []string{"jq@1.7-1"},
	}

	if lockedGenerationDrifted(galeDir, storeRoot, plan) {
		t.Error("generation already matches the plan, but drift was " +
			"reported: every no-op locked sync would rebuild")
	}
}

// The two drift paths must be mutually exclusive, not computed then
// discarded. Calling generationDrifted under a plan and overwriting
// its result costs one registry round trip per bare pin, immediately
// after plan construction already resolved every recipe the operation
// needs — and it leaves the locked answer one deleted line away from
// being the unused one.
func TestSyncDriftSelectorDoesNotResolveUnderAPlan(t *testing.T) {
	tmp := t.TempDir()
	storeRoot := filepath.Join(tmp, "store")
	galeDir := filepath.Join(tmp, ".gale")
	seedStore(t, storeRoot, "jq", "1.7-1")
	// The generation has to exist and match the declared set in size,
	// or the unlocked path returns early on a read error and never
	// reaches its resolver — which would make the assertion below pass
	// under both branches and prove nothing.
	if err := generation.Build(
		map[string]string{"jq": "1.7-1"}, galeDir, storeRoot,
	); err != nil {
		t.Fatal(err)
	}

	plan := &lockplan.Plan{
		Nodes: map[string]lockplan.Node{
			"jq@1.7-1": {Name: "jq", Version: "1.7-1"},
		},
		Order: []string{"jq@1.7-1"},
		Roots: []string{"jq@1.7-1"},
	}

	resolved := false
	syncDrifted(driftQuery{
		galeDir:   galeDir,
		storeRoot: storeRoot,
		declared:  map[string]string{"jq": "1.7"},
		plan:      plan,
		pinResolve: func(string, string) (*recipe.Recipe, error) {
			resolved = true
			return minimalRecipe("jq", "1.7"), nil
		},
	})

	if resolved {
		t.Error("locked drift check resolved a recipe: the lock already " +
			"answered this question during plan construction")
	}
}

// Unlocked, the resolver is still consulted: gh#137's canonical-pin
// mapping is what stops a no-op sync rebuilding on every run.
func TestSyncDriftSelectorStillResolvesWithoutAPlan(t *testing.T) {
	tmp := t.TempDir()
	storeRoot := filepath.Join(tmp, "store")
	galeDir := filepath.Join(tmp, ".gale")
	seedStore(t, storeRoot, "jq", "1.7-1")
	if err := generation.Build(
		map[string]string{"jq": "1.7-1"}, galeDir, storeRoot,
	); err != nil {
		t.Fatal(err)
	}

	resolved := false
	syncDrifted(driftQuery{
		galeDir:   galeDir,
		storeRoot: storeRoot,
		declared:  map[string]string{"jq": "1.7"},
		pinResolve: func(string, string) (*recipe.Recipe, error) {
			resolved = true
			return minimalRecipe("jq", "1.7"), nil
		},
	})

	if !resolved {
		t.Error("unlocked drift check must still map bare pins through " +
			"the recipe (gh#137)")
	}
}
