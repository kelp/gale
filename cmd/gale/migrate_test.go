package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/recipe"
)

// migrateResolver answers with a leftover recipe for the
// given names. Bottles are gone; the names are only a
// fixture handle.
func migrateResolver(binary ...string) func(string, string) (*recipe.Recipe, error) {
	return func(name, version string) (*recipe.Recipe, error) {
		r := &recipe.Recipe{
			Package: recipe.Package{Name: name, Version: "1.0"},
		}
		_ = binary
		return r, nil
	}
}

// The scan reads the STORE, not any scope's closure.
//
// Design §13 revision 7 makes this one of migrate's five qualifying
// properties, and it is the one that separates migrate from
// `--refresh`: a closure-based scan covers only what some generation
// still links, so a directory left behind by a removed root, or one
// whose dependency metadata predates the format, would never be
// migrated and would go on vetoing every per-scope replacement
// forever. There is no command to reach it and no way to converge.
func TestMigrateOrdersDependenciesFirst(t *testing.T) {
	// The order the scan produces, which is sorted by name.
	candidates := []migrateTarget{
		{
			name: "app", version: "1.0-1",
			recipe: runtimeDepRecipe("app", "1.0", "zdep"),
		},
		{
			name: "zdep", version: "1.0-1",
			recipe: runtimeDepRecipe("zdep", "1.0"),
		},
	}

	got, err := orderCandidates(candidates, byNameResolver("zdep", "1.0"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("ordering dropped candidates: %v", got)
	}
	if got[0].name != "zdep" {
		t.Errorf("order = %s, %s; want the dependency first",
			got[0].name, got[1].name)
	}
}

// Two versions of one package are two candidates, and the edge goes
// to the one a refetch would actually link.
//
// The scan reads the whole store, so it routinely holds several
// versions of one name — which is why migrate cannot reuse
// `orderRoots`: that function keys its node map and its traversal
// state on the package name alone, so one version would silently
// replace the other and the store would converge halfway.
func TestMigrateOrdersEveryVersionOfOneName(t *testing.T) {
	candidates := []migrateTarget{
		{
			name: "app", version: "1.0-1",
			recipe: runtimeDepRecipe("app", "1.0", "jq"),
		},
		{name: "jq", version: "1.6-1", recipe: runtimeDepRecipe("jq", "1.6")},
		{name: "jq", version: "1.7-1", recipe: runtimeDepRecipe("jq", "1.7")},
	}

	got, err := orderCandidates(candidates, byNameResolver("jq", "1.7"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("ordering dropped a candidate: %v", got)
	}
	if !precedes(got, "jq@1.7-1", "app@1.0-1") {
		t.Errorf("order = %v, want jq@1.7-1 before app", ids(got))
	}
}

// An unrelated cycle does not discard a real dependency order.
//
// Edges drawn by NAME rather than by resolved identity look
// conservative and are not. Here app needs lib, the resolver picks
// lib@2.0, and a stale lib@1.0 in the store names app. A name-drawn
// app→lib@1.0 edge closes a loop that does not exist, and a
// traversal that answers any cycle by returning its input would then
// hand back alphabetical order — app replaced before the lib@2.0 it
// links, which is the one outcome the ordering exists to prevent.
//
// Resolving the identity removes the edge, so nothing is unorderable
// and the real order survives.
func TestMigrateOrderingSurvivesAnUnrelatedCycle(t *testing.T) {
	candidates := []migrateTarget{
		{
			name: "app", version: "1.0-1",
			recipe: runtimeDepRecipe("app", "1.0", "lib"),
		},
		{
			name: "lib", version: "1.0-1",
			recipe: runtimeDepRecipe("lib", "1.0", "app"),
		},
		{name: "lib", version: "2.0-1", recipe: runtimeDepRecipe("lib", "2.0")},
	}

	got, err := orderCandidates(candidates, byNameResolver("lib", "2.0"))
	if err != nil {
		t.Fatal(err)
	}
	if !precedes(got, "lib@2.0-1", "app@1.0-1") {
		t.Errorf("order = %v, want lib@2.0-1 before app@1.0-1", ids(got))
	}
}

// A genuine cycle is refused rather than ordered arbitrarily.
//
// Replacement is destructive and the order carries meaning, so
// emitting a cycle's members in whatever order a depth-first walk
// reaches them would destroy bytes on a sequence that satisfies
// nothing.
func TestMigrateOrderingRefusesARealCycle(t *testing.T) {
	candidates := []migrateTarget{
		{
			name: "a", version: "1.0-1",
			recipe: runtimeDepRecipe("a", "1.0", "b"),
		},
		{
			name: "b", version: "1.0-1",
			recipe: runtimeDepRecipe("b", "1.0", "a"),
		},
	}

	_, err := orderCandidates(candidates, func(_ context.Context, name string) (*recipe.Recipe, error) {
		return runtimeDepRecipe(name, "1.0"), nil
	})
	if !errors.Is(err, errMigrateCycle) {
		t.Fatalf("err = %v, want errMigrateCycle", err)
	}
}

// byNameResolver answers every name with version "1.0" except one,
// which is how a fixture says "this is the version a refetch would
// actually link".
func byNameResolver(name, version string) installer.RecipeResolver {
	return func(_ context.Context, want string) (*recipe.Recipe, error) {
		if want == name {
			return runtimeDepRecipe(want, version), nil
		}
		return runtimeDepRecipe(want, "1.0"), nil
	}
}

func ids(targets []migrateTarget) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, t.name+"@"+t.version)
	}
	return out
}

// precedes reports whether first appears before second.
func precedes(targets []migrateTarget, first, second string) bool {
	all := ids(targets)
	return slices.Index(all, first) >= 0 &&
		slices.Index(all, first) < slices.Index(all, second)
}

// A store directory gale cannot classify stops the scan, before
// anything is replaced.
//
// §13's second qualifying property: fail BEFORE replacing on
// unreadable state. A record that exists and does not validate is
// exactly that — something wrote bytes disagreeing with the format
// gale produces — and migrating past it would replace directories
// while a known-corrupt one sits in the same store, unexplained.
func TestMigrateScanStopsOnAnUnreadableRecord(t *testing.T) {
	storeRoot := t.TempDir()
	dir := seedStore(t, storeRoot, "corrupt", "1.0-1")
	if err := os.WriteFile(
		filepath.Join(dir, ".gale-provenance.toml"), []byte("nope\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	_, err := migrateScan(storeRoot, migrateResolver())
	if !errors.Is(err, errMigrateUnreadable) {
		t.Fatalf("err = %v, want errMigrateUnreadable", err)
	}
}

// Every candidate is checked against the scopes BEFORE the first
// replacement.
//
// §13 revision 7's second qualifying property, and it is what makes
// the machine-wide unit meaningful: a pass that replaced half the
// store and then met a disagreement would leave the machine in a
// state neither the old nor the new description covers.
//
// The hash compared is the one migrate PROPOSES, from the recipe, so
// two scopes resolving different artifacts for one identity conflict
// even where neither lock records a hash. This fixture's project
// records a hash, which is the narrower case.
//
// What this does NOT prove, said plainly because the code comment
// claims it: that no scope is exempt. Migrate passes an empty
// selfGaleDir, so there is no initiating scope to exempt and nothing
// observable to assert — the property is structural, held by the
// call site rather than by this test. A fixture cannot distinguish
// "exempts nobody" from "exempts a scope that is not present".
func TestMigrateOrdersTheCanonicalDirBeforeItsBareTwin(t *testing.T) {
	r := runtimeDepRecipe("jq", "1.0")
	candidates := []migrateTarget{
		{name: "jq", version: "1.0-1", dir: "/s/jq/1.0", bare: true, recipe: r},
		{name: "jq", version: "1.0-1", dir: "/s/jq/1.0-1", recipe: r},
	}

	got, err := orderCandidates(candidates, byNameResolver("jq", "1.0"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("ordering dropped a candidate: %v", ids(got))
	}
	if got[0].bare {
		t.Error("the pre-revision directory was ordered before its " +
			"canonical twin")
	}
}
