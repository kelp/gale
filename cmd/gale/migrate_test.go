package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/projects"
	"github.com/kelp/gale/internal/provenance"
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

// A record naming the candidate refuses the replacement, in ANY
// scope, before anything is replaced.
//
// The tempting argument for omitting this check is that §7 makes
// provenance all-or-nothing, so nothing provenanced can record an
// unprovenanced dependency and there is no record to invalidate.
// That holds only for an undamaged history. A partial restore, or a
// deleted record, leaves exactly this state, and migrateScan reads
// records with ReadUnverified, which establishes that a record
// parses and not that its graph digest still describes the store.
//
// Migrate walks every scope rather than an initiating one. `lock
// --refresh` walks only its own because checkReplaceable refuses for
// every other scope on the wider ground that it cannot tell which
// bytes that scope needs; migrate deliberately dropped that veto, so
// this scan is the only thing left watching the other scopes.
func TestMigratePreflightRefusesADependentRecordInAnyScope(t *testing.T) {
	home := t.TempDir()
	storeRoot := filepath.Join(home, "pkg")
	proj := t.TempDir()
	if err := projects.Register(home, proj); err != nil {
		t.Fatal(err)
	}

	// dep is provenanced first so app's record can be built over it,
	// then its record is removed: the partial restore, reproduced.
	seedProvenanced(t, storeRoot, "dep", "1.0-1")
	writeDepsMeta(t, storeRoot, "dep", "1.0-1")
	seedStore(t, storeRoot, "app", "1.0-1")
	writeDepsMeta(t, storeRoot, "app", "1.0-1",
		depsmeta.ResolvedDep{Name: "dep", Version: "1.0", Revision: 1})
	writeProvenanceOver(t, storeRoot,
		pkgRef{"app", "1.0-1"}, pkgRef{"dep", "1.0-1"})
	if err := os.Remove(filepath.Join(
		storeRoot, "dep", "1.0-1", provenance.File,
	)); err != nil {
		t.Fatal(err)
	}
	// Only the OTHER scope loads app. The global scope links nothing.
	if err := generation.Build(map[string]string{"app": "1.0-1"},
		filepath.Join(proj, ".gale"), storeRoot); err != nil {
		t.Fatal(err)
	}

	scan, err := migrateScan(storeRoot, migrateResolver("dep"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(scan.candidates, named("dep")) {
		t.Fatalf("dep is not a candidate: %v", scan.candidates)
	}

	err = migratePreflight(home, storeRoot, scan)
	if !errors.Is(err, errDependentRecord) {
		t.Fatalf("err = %v, want errDependentRecord", err)
	}
	if !strings.Contains(err.Error(), proj) {
		t.Errorf("the refusal must name the scope holding the record: %q", err)
	}
}

// writeDepsMeta records one directory's built-against closure, which
// is what the reference walk descends through.
func writeDepsMeta(
	t *testing.T, storeRoot, name, version string, deps ...depsmeta.ResolvedDep,
) {
	t.Helper()
	if err := depsmeta.Write(
		filepath.Join(storeRoot, name, version),
		depsmeta.Metadata{Deps: deps},
	); err != nil {
		t.Fatal(err)
	}
}

// The commit is pinned to the artifact preflight cleared with every
// scope, by hash AND by method.
//
// Design §13's fourth qualifying property is revalidation before each
// destructive commit, and the two ways an unlocked install can arrive
// somewhere else are both live here. It falls back from a failed
// binary fetch to a source build, so a run cleared against a declared
// binary can commit a source artifact nobody was asked about; and the
// artifact behind a URL can change, so passing whatever landed
// straight to the veto would clear X and commit Y.
func TestMigrateCommitRefusesWhatPreflightDidNotClear(t *testing.T) {
	const otherSHA = "9999999999999999999999999999999999999999999999" +
		"999999999999999999"
	tests := []struct {
		name   string
		method installer.InstallMethod
		sha    string
		want   error
	}{
		{"a source fallback", installer.MethodSource, testSHA, errMigrateNotBinary},
		{"another artifact", installer.MethodBinary, otherSHA, errMigrateHashMoved},
		{"the cleared artifact", installer.MethodBinary, testSHA, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			storeRoot := filepath.Join(home, "pkg")
			canonical := seedStore(t, storeRoot, "jq", "1.0-1")
			staging := stagedProvenanced(t, storeRoot, "jq", "1.0-1")

			r, err := migrateResolver("jq")("jq", "1.0-1")
			if err != nil {
				t.Fatal(err)
			}
			target := migrateTarget{
				name: "jq", version: "1.0-1", dir: canonical, recipe: r,
			}

			err = checkMigrateCommit(home, storeRoot, target,
				installer.Replacement{
					CanonicalDir: canonical, StagingDir: staging,
					Result: installer.InstallResult{
						Name: "jq", Version: "1.0-1",
						Method: tt.method, SHA256: tt.sha,
					},
				})
			if tt.want == nil {
				if err != nil {
					t.Fatalf("the cleared artifact was refused: %v", err)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

// stagedProvenanced builds a staging directory carrying a valid
// record, which is what the commit check requires of a candidate:
// trading one unprovenanced directory for another repairs nothing.
func stagedProvenanced(t *testing.T, storeRoot, name, version string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "staged")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rec, err := provenance.New(storeRoot, lockgraph.Node{
		Name: name, Version: version,
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		Method: lockgraph.MethodBinary, SHA256: testSHA,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := provenance.Write(dir, rec); err != nil {
		t.Fatal(err)
	}
	return dir
}

// pkgRef names one installed package.
type pkgRef struct{ name, version string }

// writeProvenanceOver writes a record whose runtime closure names
// another installed package, so the dependent scan has an edge to
// find. The dependency must already be provenanced: the digest is
// computed from its record.
func writeProvenanceOver(
	t *testing.T, storeRoot string, node, dep pkgRef,
) {
	t.Helper()
	name, version := node.name, node.version
	rec, err := provenance.New(storeRoot, lockgraph.Node{
		Name: name, Version: version,
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		Method: lockgraph.MethodBinary, SHA256: testSHA,
		Edges: []lockgraph.Edge{{
			Kind: lockgraph.KindRuntime, Name: dep.name, Version: dep.version,
		}},
	})
	if err != nil {
		t.Fatalf("provenance.New(%s): %v", name, err)
	}
	if err := provenance.Write(
		filepath.Join(storeRoot, name, version), rec,
	); err != nil {
		t.Fatal(err)
	}
}

// runtimeDepRecipe declares runtime dependencies and nothing else,
// which is the only edge kind migrate's ordering may use: a binary
// record serializes runtime deps only, so a build tool cannot leave
// its dependent unattestable here the way it does for a source
// artifact.
func runtimeDepRecipe(name, version string, deps ...string) *recipe.Recipe {
	return &recipe.Recipe{
		Package:      recipe.Package{Name: name, Version: version},
		Dependencies: recipe.Dependencies{Runtime: deps},
	}
}

// A candidate another candidate depends on is replaced first.
//
// Design §5 and §7 make the order load-bearing rather than cosmetic,
// exactly as they do for `gale lock`. Provenance is all-or-nothing,
// so refetching a dependent while its dependency is still
// unprovenanced commits an artifact with no record: bytes destroyed,
// nothing repaired, and the candidate check then refuses the
// replacement. Alphabetical order would decide whether the machine
// can converge at all, and "app" sorts before the "zdep" it links.
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

	_, err := orderCandidates(candidates, func(name string) (*recipe.Recipe, error) {
		return runtimeDepRecipe(name, "1.0"), nil
	})
	if !errors.Is(err, errMigrateCycle) {
		t.Fatalf("err = %v, want errMigrateCycle", err)
	}
}

// byNameResolver answers every name with version "1.0" except one,
// which is how a fixture says "this is the version a refetch would
// actually link".
func byNameResolver(name, version string) func(string) (*recipe.Recipe, error) {
	return func(want string) (*recipe.Recipe, error) {
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
