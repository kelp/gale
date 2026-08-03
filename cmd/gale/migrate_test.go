package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/kelp/gale/internal/projects"
	"github.com/kelp/gale/internal/recipe"
)

// migrateResolver answers with a recipe that declares a prebuilt
// binary for this platform only for the names in binary, so a
// fixture can put a package on either side of the source/binary
// split without building anything.
func migrateResolver(binary ...string) func(string, string) (*recipe.Recipe, error) {
	return func(name, version string) (*recipe.Recipe, error) {
		r := &recipe.Recipe{
			Package: recipe.Package{Name: name, Version: "1.0"},
		}
		if slices.Contains(binary, name) {
			r.Binary = map[string]recipe.Binary{
				runtime.GOOS + "-" + runtime.GOARCH: {
					URL:    "oci://ghcr.io/kelp/gale/" + name,
					SHA256: testSHA,
				},
			}
		}
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
func TestMigrateScanCoversDirsNoGenerationReaches(t *testing.T) {
	storeRoot := t.TempDir()
	// Nothing here is linked by any generation; there are no scopes
	// at all.
	seedStore(t, storeRoot, "orphan", "1.0-1")
	seedStore(t, storeRoot, "fromsrc", "1.0-1")
	seedProvenanced(t, storeRoot, "done", "1.0-1")

	scan, err := migrateScan(storeRoot, migrateResolver("orphan", "done"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(scan.candidates, named("orphan")) {
		t.Errorf("an unreferenced binary-method dir was not a candidate: %v",
			scan.candidates)
	}
	// A source-built package cannot be migrated by refetching, so §13
	// says migrate prints it rather than pretending.
	if !slices.ContainsFunc(scan.sourceOnly, named("fromsrc")) {
		t.Errorf("a source-only dir was not listed: %v", scan.sourceOnly)
	}
	if slices.ContainsFunc(scan.candidates, named("fromsrc")) {
		t.Error("a source-only dir was queued for refetching")
	}
	// A provenanced directory is not migrate's business: §13 forbids
	// writing a record beside a directory it did not replace, and
	// replacing one that already attests itself destroys bytes for
	// nothing.
	for _, set := range [][]migrateTarget{scan.candidates, scan.sourceOnly} {
		if slices.ContainsFunc(set, named("done")) {
			t.Errorf("an already provenanced dir was queued: %v", set)
		}
	}
}

func named(name string) func(migrateTarget) bool {
	return func(t migrateTarget) bool { return t.name == name }
}

// Migrate does not inherit the per-scope veto that refuses a
// directory another scope loads without naming a hash for it.
//
// Design §13 is explicit that this is what separates the two
// commands. The per-scope rule is correct for `lock --refresh`: one
// scope cannot know which bytes a legacy neighbour needs, so it
// refuses. On upgrade day EVERY scope is legacy and every transitive
// dependency is a reference with no hash, so applying that rule to
// migrate makes it refuse exactly the state it exists to escape.
// §13 names migrate's failure set instead: unreadable state, or an
// explicit hash disagreement covering every recorded and proposed
// candidate hash.
//
// The unreadable case stays a refusal under both commands, and is
// asserted here rather than left to the refresh tests, because the
// two rules live one branch apart and relaxing the wrong one would
// let migrate replace bytes it could not prove nobody needs.
func TestMigratePreflightPolicySeparatesReferenceFromUnreadable(t *testing.T) {
	tests := []struct {
		name string
		// withMeta writes an explicitly empty .gale-deps.toml, so the
		// closure walk is complete. Omitting it leaves the closure
		// unknown, which is the unreadable state.
		withMeta bool
		wantErr  bool
	}{
		{
			name:     "a reference with no hash is a participant",
			withMeta: true,
			wantErr:  false,
		},
		{
			name:     "an unreadable closure still refuses",
			withMeta: false,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			storeRoot := filepath.Join(home, "pkg")
			proj := t.TempDir()
			if err := projects.Register(home, proj); err != nil {
				t.Fatal(err)
			}
			// The scope loads the very directory migrate would replace
			// and holds no lock at all, so it names no hash for it.
			seedScopeClosure(t, scopePkg{
				galeDir: filepath.Join(proj, ".gale"), storeRoot: storeRoot,
				name: "legacydep", version: "1.0-1", withMeta: tt.withMeta,
			})

			scan, err := migrateScan(storeRoot, migrateResolver("legacydep"))
			if err != nil {
				t.Fatal(err)
			}
			if len(scan.candidates) != 1 {
				t.Fatalf("candidates = %v, want the seeded dir", scan.candidates)
			}

			err = migratePreflight(home, storeRoot, scan)
			if tt.wantErr && !errors.Is(err, errScopeDisagrees) {
				t.Fatalf("err = %v, want errScopeDisagrees", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("migrate refused a scope that only references "+
					"the candidate: %v", err)
			}
		})
	}
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
func TestMigratePreflightRefusesBeforeReplacingAnything(t *testing.T) {
	home := t.TempDir()
	storeRoot := filepath.Join(home, "pkg")
	proj := t.TempDir()
	if err := projects.Register(home, proj); err != nil {
		t.Fatal(err)
	}
	// The project requires other bytes at the identity migrate would
	// refetch.
	writeScopeLock(t, filepath.Join(proj, "gale.lock"),
		"disputed@1.0-1", shaY)

	marker := filepath.Join(
		seedStore(t, storeRoot, "disputed", "1.0-1"), "legacy-marker",
	)
	if err := os.WriteFile(marker, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	scan, err := migrateScan(storeRoot, migrateResolver("disputed"))
	if err != nil {
		t.Fatal(err)
	}

	err = migratePreflight(home, storeRoot, scan)
	if !errors.Is(err, errScopeDisagrees) {
		t.Fatalf("err = %v, want errScopeDisagrees", err)
	}
	kept, rerr := os.ReadFile(marker)
	if rerr != nil {
		t.Fatalf("the scan or the preflight destroyed bytes: %v", rerr)
	}
	if string(kept) != "old" {
		t.Errorf("marker holds %q, want %q", kept, "old")
	}
}
