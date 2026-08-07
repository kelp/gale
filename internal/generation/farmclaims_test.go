package generation

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/projects"
)

// claimsFixture is one machine: a gale home with a shared store, a
// registered project, and helpers to give either scope a lock or an
// installed dylib-providing package.
type claimsFixture struct {
	t         *testing.T
	galeHome  string
	storeRoot string
	proj      string
}

func newClaimsFixture(t *testing.T) *claimsFixture {
	t.Helper()
	root := t.TempDir()
	f := &claimsFixture{
		t:         t,
		galeHome:  filepath.Join(root, ".gale"),
		storeRoot: filepath.Join(root, ".gale", "pkg"),
		proj:      filepath.Join(root, "proj"),
	}
	if err := os.MkdirAll(f.proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := projects.Register(f.galeHome, f.proj); err != nil {
		t.Fatal(err)
	}
	return f
}

// install lays out an installed package with one versioned dylib
// and returns its store dir.
func (f *claimsFixture) install(name, version, stem string) string {
	f.t.Helper()
	dir := filepath.Join(f.storeRoot, name, version)
	lib := filepath.Join(dir, "lib")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(lib, soname(stem)), []byte("x"), 0o644,
	); err != nil {
		f.t.Fatal(err)
	}
	return dir
}

// platform is the artifact key claims are read for.
func platform() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}

// lockV1 writes a v1 lock for the project with the given roots and
// nodes.
func (f *claimsFixture) lockV1(roots []string, pkgs map[string]lockfile.Package) {
	f.t.Helper()
	lf := &lockfile.V1{
		Version: lockfile.SchemaVersion,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{Roots: roots},
		},
		Packages: pkgs,
	}
	if err := lockfile.WriteV1(
		filepath.Join(f.proj, "gale.lock"), lf,
	); err != nil {
		f.t.Fatal(err)
	}
}

// artifact builds a minimal locked node for the current platform.
func artifact(method string, runtimeDeps, buildDeps []string) lockfile.Package {
	return lockfile.Package{
		Artifacts: map[string]lockfile.Artifact{
			platform(): {
				SHA256:      "deadbeef",
				Method:      method,
				RuntimeDeps: runtimeDeps,
				BuildDeps:   buildDeps,
				GraphDigest: "digest",
			},
		},
	}
}

// TestFarmClaimants_LockedScopeClaimsLockClosure: a registered
// project with a v1 lock claims the runtime closure the lock
// records — roots plus recorded runtime edges — resolved to store
// dirs. Build deps produce no farm links, so they must not be
// claimed: a claim on a build tool's dylib would refuse operations
// over sonames no claimant binary loads at runtime.
func TestFarmClaimants_LockedScopeClaimsLockClosure(t *testing.T) {
	f := newClaimsFixture(t)
	curl := f.install("curl", "8.19.0-1", "libcurl")
	zstd := f.install("zstd", "1.5.6-1", "libzstd")
	f.install("cmake", "3.30.0-1", "libmanip")

	f.lockV1(
		[]string{"curl@8.19.0-1"},
		map[string]lockfile.Package{
			"curl@8.19.0-1": artifact("source",
				[]string{"zstd@1.5.6-1"}, []string{"cmake@3.30.0-1"}),
			"zstd@1.5.6-1":   artifact("binary", nil, nil),
			"cmake@3.30.0-1": artifact("binary", nil, nil),
		},
	)

	claimants := FarmClaimants(f.storeRoot, f.galeHome)
	if len(claimants) != 1 {
		t.Fatalf("claimants = %+v, want exactly the project", claimants)
	}
	c := claimants[0]
	if c.Err != nil {
		t.Fatalf("claimant error: %v", c.Err)
	}
	want := map[string]bool{curl: true, zstd: true}
	if len(claimDirs(c)) != len(want) {
		t.Fatalf("claims = %v, want curl and zstd only", claimDirs(c))
	}
	for _, d := range claimDirs(c) {
		if !want[d] {
			t.Errorf("unexpected claimed dir %s (build deps must "+
				"not be claimed)", d)
		}
	}
}

// TestFarmClaimants_LockedScopeAlsoClaimsActiveGeneration: a v1
// lock says what a scope REQUIRES; its active generation says what
// that scope's binaries are loading right now. Design §4 names the
// second as the claim ("every other scope's current active
// closure"), and the lock cannot replace it: between a repin and
// the sync that satisfies it, the lock names the new version while
// the live generation still links the old one. Claiming only the
// lock would leave the running closure's soname unclaimed and free
// for another scope to retarget under it.
//
// So both sources contribute, and where they name the same package
// the generation wins. That tie-break is settled HERE rather than in
// the guard, which refuses a claimant contradicting itself: only
// this function knows which of the two dirs is the one a running
// binary resolves through. The lock still contributes every package
// the generation does not link at all, which is most of a closure —
// transitive deps are not generation entries.
func TestFarmClaimants_LockedScopeAlsoClaimsActiveGeneration(t *testing.T) {
	f := newClaimsFixture(t)
	running := f.install("curl", "8.18.0-1", "libcurl")
	superseded := f.install("curl", "8.19.0-1", "libcurl")
	runningDep := f.install("zstd", "1.5.5-1", "libzstd")
	supersededDep := f.install("zstd", "1.5.6-1", "libzstd")
	lockOnlyDep := f.install("brotli", "1.1.0-1", "libbrotli")
	// zstd is reached only through curl's recorded deps, so it is a
	// live claim that is not a generation entry.
	if err := depsmeta.Write(running, depsmeta.Metadata{
		Deps: []depsmeta.ResolvedDep{
			{Name: "zstd", Version: "1.5.5", Revision: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := Build(
		map[string]string{"curl": "8.18.0-1"},
		filepath.Join(f.proj, ".gale"), f.storeRoot,
	); err != nil {
		t.Fatal(err)
	}
	// The lock has moved to curl 8.19 with a newer zstd, and pulls
	// in a dep the generation has never linked.
	f.lockV1([]string{"curl@8.19.0-1"}, map[string]lockfile.Package{
		"curl@8.19.0-1": artifact("binary",
			[]string{"zstd@1.5.6-1", "brotli@1.1.0-1"}, nil),
		"zstd@1.5.6-1":   artifact("binary", nil, nil),
		"brotli@1.1.0-1": artifact("binary", nil, nil),
	})

	claimants := FarmClaimants(f.storeRoot, f.galeHome)
	if len(claimants) != 1 {
		t.Fatalf("claimants = %+v, want the project", claimants)
	}
	c := claimants[0]
	if c.Err != nil {
		t.Fatalf("claimant error: %v", c.Err)
	}
	got := map[string]bool{}
	for _, d := range claimDirs(c) {
		got[d] = true
	}
	for _, want := range []string{running, runningDep, lockOnlyDep} {
		if !got[want] {
			t.Errorf("claims = %v, missing %s", claimDirs(c), want)
		}
	}
	for _, unwanted := range []string{superseded, supersededDep} {
		if got[unwanted] {
			t.Errorf("claims = %v names two versions of one "+
				"package (%s); the live generation must win, or the "+
				"guard refuses every operation while this scope has "+
				"an unsynced repin", claimDirs(c), unwanted)
		}
	}
	// The consequence, stated as the guard sees it: a scope with an
	// unsynced repin must not freeze the machine. Populating a
	// package this claimant has no opinion about has to be allowed,
	// which is false the moment the claim contradicts itself, since
	// the guard rejects such a claimant before it ever looks at
	// which soname the operation touches.
	unrelated := f.install("jq", "1.8.1-1", "libjq")
	if err := farm.GuardPopulate(
		farm.At(unrelated), claimants,
	); err != nil {
		t.Errorf("populating an unrelated package must be allowed "+
			"while another scope has an unsynced repin, got: %v", err)
	}
}

// TestProposedClaimant_KeepsADependentsVersion: the initiating
// scope is a claimant too (design §4 step 1), and its claim is the
// closure the operation LEAVES BEHIND, not the one it starts with.
//
// The case that needs it: app is built against zlib 1, the user
// installs zlib 2 as a direct root, and both export the same
// soname. Every other check passes it. farm.Populate permits the
// overwrite because both dirs belong to package zlib; the batch
// being populated is one dir, so it cannot conflict with itself;
// and a project generation rebuilds into its own lib dir, so the
// whole-closure check at the rebuild boundary never runs for the
// scope that did this. app is left resolving zlib 2.
//
// Superseding by name alone would not catch it either: zlib 1 is
// still in the resulting closure, reached through app's recorded
// deps rather than as a root. Recomputing the closure from the
// post-operation package set is what keeps it.
func TestProposedClaimant_KeepsADependentsVersion(t *testing.T) {
	f := newClaimsFixture(t)
	app := f.install("app", "1.0-1", "libapp")
	oldZlib := f.install("zlib", "1.3-1", "libz")
	newZlib := f.install("zlib", "2.0-1", "libz")
	if err := depsmeta.Write(app, depsmeta.Metadata{
		Deps: []depsmeta.ResolvedDep{
			{Name: "zlib", Version: "1.3", Revision: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	galeDir := filepath.Join(f.proj, ".gale")
	if err := Build(map[string]string{
		"app": "1.0-1", "zlib": "1.3-1",
	}, galeDir, f.storeRoot); err != nil {
		t.Fatal(err)
	}

	// Installing zlib 2 is refused: app still requires zlib 1.
	self, err := ProposedClaimant(
		map[string]string{"zlib": "2.0-1"}, galeDir, f.storeRoot,
	)
	if err != nil {
		t.Fatalf("building the proposed claim: %v", err)
	}
	err = farm.GuardPopulate(farm.At(newZlib), []farm.Claimant{self})
	if err == nil || !errors.Is(err, farm.ErrClaimConflict) {
		t.Errorf("installing zlib 2 under a dependent built against "+
			"zlib 1 must be refused, got: %v", err)
	}

	// The same operation with no dependent is an ordinary update
	// and must be allowed, or every update deadlocks.
	g := newClaimsFixture(t)
	g.install("zlib", "1.3-1", "libz")
	lone := g.install("zlib", "2.0-1", "libz")
	loneDir := filepath.Join(g.proj, ".gale")
	if err := Build(
		map[string]string{"zlib": "1.3-1"}, loneDir, g.storeRoot,
	); err != nil {
		t.Fatal(err)
	}
	selfLone, err := ProposedClaimant(
		map[string]string{"zlib": "2.0-1"}, loneDir, g.storeRoot,
	)
	if err != nil {
		t.Fatalf("building the proposed claim: %v", err)
	}
	if err := farm.GuardPopulate(
		farm.At(lone), []farm.Claimant{selfLone},
	); err != nil {
		t.Errorf("a self-update with no dependent must be allowed, "+
			"got: %v", err)
	}
	_ = oldZlib
}

// TestFarmClaimants_UnreadableDepsMetadataFailsClosed: the active
// half of a claim walks each dir's recorded deps, and the helper
// generations rebuild with treats an unreadable .gale-deps.toml as
// best-effort — it warns and stops expanding. That contract is
// right for a rebuild and wrong for a claim: the claimant would
// come back holding the root and silently missing its runtime
// closure, so the guard would allow an operation over a soname only
// the dropped dep provides. A claim that cannot be read must fail
// closed, like every other unreadable scope.
func TestFarmClaimants_UnreadableDepsMetadataFailsClosed(t *testing.T) {
	f := newClaimsFixture(t)
	curl := f.install("curl", "8.19.0-1", "libcurl")
	f.install("zstd", "1.5.6-1", "libzstd")
	if err := os.WriteFile(
		filepath.Join(curl, ".gale-deps.toml"),
		[]byte("deps = [ this is not toml\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := Build(
		map[string]string{"curl": "8.19.0-1"},
		filepath.Join(f.proj, ".gale"), f.storeRoot,
	); err != nil {
		t.Fatal(err)
	}

	claimants := FarmClaimants(f.storeRoot, f.galeHome)
	if len(claimants) != 1 {
		t.Fatalf("claimants = %+v, want the project", claimants)
	}
	if claimants[0].Err == nil {
		t.Fatalf("claimant = %+v, want Err set: an unreadable dep "+
			"closure must refuse the operation, not shrink the claim",
			claimants[0])
	}
	if !strings.Contains(claimants[0].Err.Error(), "gale sync") {
		t.Errorf("error %q must carry its repair: this one blocks "+
			"every scope until it clears", claimants[0].Err)
	}

	// The escape hatch the error names has to exist, or a machine
	// with one corrupt file stays wedged. Deleting the whole
	// directory clears it, and the claim then shrinks honestly:
	// bytes that are not in the store cannot be mapped into the
	// farm, so nothing is being hidden. Deleting only the metadata
	// file would clear it too, and THAT is the fail-open — the walk
	// would succeed while quietly dropping the dep closure.
	if err := os.RemoveAll(curl); err != nil {
		t.Fatal(err)
	}
	repaired := FarmClaimants(f.storeRoot, f.galeHome)
	for _, c := range repaired {
		if c.Err != nil {
			t.Errorf("deleting the directory left the guard wedged: %v",
				c.Err)
		}
	}
}

// TestFarmClaimants_UnlockedScopeClaimsActiveGeneration: without a
// v1 lock the scope's claim is what it actually links — its active
// generation's farm closure. Covers both the absent-lock and the
// legacy-lock scope, which predate enforcement equally.
func TestFarmClaimants_UnlockedScopeClaimsActiveGeneration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		legacy bool
	}{
		{"absent lock", false},
		{"legacy lock", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newClaimsFixture(t)
			curl := f.install("curl", "8.19.0-1", "libcurl")
			projGale := filepath.Join(f.proj, ".gale")
			if err := Build(
				map[string]string{"curl": "8.19.0-1"},
				projGale, f.storeRoot,
			); err != nil {
				t.Fatal(err)
			}
			if tc.legacy {
				legacy := &lockfile.LockFile{
					Packages: map[string]lockfile.LockedPackage{
						"curl": {Version: "8.19.0-1", SHA256: "deadbeef"},
					},
				}
				if err := lockfile.Write(
					filepath.Join(f.proj, "gale.lock"), legacy,
				); err != nil {
					t.Fatal(err)
				}
			}

			claimants := FarmClaimants(f.storeRoot, f.galeHome)
			if len(claimants) != 1 {
				t.Fatalf("claimants = %+v, want the project", claimants)
			}
			c := claimants[0]
			if c.Err != nil {
				t.Fatalf("claimant error: %v", c.Err)
			}
			if len(claimDirs(c)) != 1 || claimDirs(c)[0] != curl {
				t.Errorf("claims = %v, want [%s]", claimDirs(c), curl)
			}
		})
	}
}

// TestFarmClaimants_ExcludesInitiatorIncludesGlobal: the initiating
// scope's old claim is superseded and must not be collected, while
// the global scope — which the project registry skips by design —
// must be. A guard that misses the global scope lets any project
// operation violate the global locked closure unnoticed.
func TestFarmClaimants_ExcludesInitiatorIncludesGlobal(t *testing.T) {
	f := newClaimsFixture(t)
	curl := f.install("curl", "8.19.0-1", "libcurl")

	globalLock := &lockfile.V1{
		Version: lockfile.SchemaVersion,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{Roots: []string{"curl@8.19.0-1"}},
		},
		Packages: map[string]lockfile.Package{
			"curl@8.19.0-1": artifact("binary", nil, nil),
		},
	}
	if err := lockfile.WriteV1(
		filepath.Join(f.galeHome, "gale.lock"), globalLock,
	); err != nil {
		t.Fatal(err)
	}
	// The project also has a claimable lock; it must be excluded
	// as the initiator.
	f.lockV1([]string{"curl@8.19.0-1"}, map[string]lockfile.Package{
		"curl@8.19.0-1": artifact("binary", nil, nil),
	})

	claimants := FarmClaimants(
		f.storeRoot, filepath.Join(f.proj, ".gale"),
	)
	if len(claimants) != 1 {
		t.Fatalf("claimants = %+v, want the global scope only", claimants)
	}
	c := claimants[0]
	if c.Err != nil {
		t.Fatalf("claimant error: %v", c.Err)
	}
	if len(claimDirs(c)) != 1 || claimDirs(c)[0] != curl {
		t.Errorf("claims = %v, want [%s]", claimDirs(c), curl)
	}
}

// TestBuildRefusesConflictingFarmRebuild (acceptance test 28): a
// global generation build whose proposed closure would repoint a
// soname a registered locked project requires at another version is
// refused before the farm mutates and before the generation swap,
// naming both versions.
func TestBuildRefusesConflictingFarmRebuild(t *testing.T) {
	f := newClaimsFixture(t)
	f.install("curl", "8.19.0-1", "libcurl")
	f.install("curl", "8.20.0-1", "libcurl")
	f.lockV1([]string{"curl@8.19.0-1"}, map[string]lockfile.Package{
		"curl@8.19.0-1": artifact("binary", nil, nil),
	})

	err := Build(
		map[string]string{"curl": "8.20.0-1"}, f.galeHome, f.storeRoot,
	)
	if err == nil {
		t.Fatal("conflicting rebuild must be refused")
	}
	if !errors.Is(err, farm.ErrClaimConflict) {
		t.Fatalf("refusal must wrap ErrClaimConflict, got: %v", err)
	}
	for _, name := range []string{"curl@8.19.0-1", "curl@8.20.0-1"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("refusal %q must name %s", err, name)
		}
	}
	// Refused before the swap: no generation is active.
	if cur, cerr := Current(f.galeHome); cerr != nil || cur != 0 {
		t.Errorf("generation swapped to %d (err %v), want none", cur, cerr)
	}
	// Refused before any farm mutation: the shared farm is untouched.
	if entries, _ := os.ReadDir(
		filepath.Join(f.galeHome, "lib"),
	); len(entries) != 0 {
		t.Errorf("farm mutated: %v", entries)
	}
}

// TestBuildFarmRebuildKeepsExternalClaims (design §4): a global
// rebuild proposing a closure that does not include a soname a
// registered project claims must succeed AND leave that soname's
// farm entry in place — Rebuild wipes before recreating, so
// rebuilding from the proposed dirs alone would delete a claimed
// entry, which is refusal-by-deletion the rule forbids.
func TestBuildFarmRebuildKeepsExternalClaims(t *testing.T) {
	f := newClaimsFixture(t)
	curl := f.install("curl", "8.19.0-1", "libcurl")
	f.install("zstd", "1.5.6-1", "libzstd")
	f.lockV1([]string{"curl@8.19.0-1"}, map[string]lockfile.Package{
		"curl@8.19.0-1": artifact("binary", nil, nil),
	})

	if err := Build(
		map[string]string{"zstd": "1.5.6-1"}, f.galeHome, f.storeRoot,
	); err != nil {
		t.Fatalf("disjoint external claim must not block the build: %v", err)
	}

	soname := "libcurl.4.dylib"
	zsoname := "libzstd.4.dylib"
	if runtime.GOOS == "linux" {
		soname = "libcurl.so.4"
		zsoname = "libzstd.so.4"
	}
	target, err := os.Readlink(
		filepath.Join(f.galeHome, "lib", soname),
	)
	if err != nil {
		t.Fatalf("claimed farm entry %s missing after rebuild: %v",
			soname, err)
	}
	if want := filepath.Join(curl, "lib", soname); target != want {
		t.Errorf("claimed entry -> %s, want %s", target, want)
	}
	if _, err := os.Readlink(
		filepath.Join(f.galeHome, "lib", zsoname),
	); err != nil {
		t.Errorf("own entry %s missing after rebuild: %v", zsoname, err)
	}
}

// TestRollbackRefusesConflictingFarmRebuild: Rollback rebuilds the
// shared farm from the rolled-to generation, so it is a farm
// mutation boundary like Build and must refuse — before the swap —
// when the rolled-to closure conflicts with another scope's claim.
func TestRollbackRefusesConflictingFarmRebuild(t *testing.T) {
	f := newClaimsFixture(t)
	f.install("curl", "8.19.0-1", "libcurl")
	f.install("curl", "8.20.0-1", "libcurl")

	// Gen 1 links the old curl, gen 2 the new one. Lock the
	// project to the new version AFTER the builds so both
	// generations exist to roll between.
	if err := Build(
		map[string]string{"curl": "8.19.0-1"}, f.galeHome, f.storeRoot,
	); err != nil {
		t.Fatal(err)
	}
	if err := Build(
		map[string]string{"curl": "8.20.0-1"}, f.galeHome, f.storeRoot,
	); err != nil {
		t.Fatal(err)
	}
	f.lockV1([]string{"curl@8.20.0-1"}, map[string]lockfile.Package{
		"curl@8.20.0-1": artifact("binary", nil, nil),
	})

	err := Rollback(f.galeHome, f.storeRoot, 1)
	if !errors.Is(err, farm.ErrClaimConflict) {
		t.Fatalf("conflicting rollback must be refused, got: %v", err)
	}
	if cur, _ := Current(f.galeHome); cur != 2 {
		t.Errorf("current = %d after refusal, want 2 (no swap)", cur)
	}
}

// TestFarmClaimants_UnreadableScopeFailsClosed: a registered scope
// whose claim cannot be read is returned with Err set, so the
// guard refuses rather than proceeding past a claim it could not
// see. The dangling-symlink lock is the readLockFile trap: present
// but unreadable, never "no lock". A lock whose roots name a node
// the lock does not define is equally unreadable as a claim.
func TestFarmClaimants_UnreadableScopeFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		break_ func(f *claimsFixture)
	}{
		{"dangling symlink lock", func(f *claimsFixture) {
			if err := os.Symlink(
				filepath.Join(f.proj, "no-such-file"),
				filepath.Join(f.proj, "gale.lock"),
			); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"root missing from lock", func(f *claimsFixture) {
			f.lockV1([]string{"curl@8.19.0-1"},
				map[string]lockfile.Package{})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newClaimsFixture(t)
			tc.break_(f)

			claimants := FarmClaimants(f.storeRoot, f.galeHome)
			if len(claimants) != 1 {
				t.Fatalf("claimants = %+v, want the broken project",
					claimants)
			}
			if claimants[0].Err == nil {
				t.Fatal("unreadable scope must carry Err (fail closed)")
			}
		})
	}
}

// A staged artifact that ADDS a runtime dependency must have that
// dependency in the scope's proposed claim.
//
// This is the fail-open direction, and the only one. Reading the
// canonical directory's metadata describes the artifact being
// REPLACED, so a dependency the candidate adds is missing from the
// claim. If that dependency provides a library the root itself
// stopped providing, the removal guard sees no claim on the soname
// and approves deleting a farm entry the proposed closure needs.
func TestProposedClaimantStagedWalksStagedDeps(t *testing.T) {
	storeRoot := filepath.Join(t.TempDir(), "pkg")
	galeDir := filepath.Join(filepath.Dir(storeRoot), "gale")

	// The dependency the candidate adds, and the transitive one
	// behind it: the walk must not stop at the direct edge.
	deep := seedPkg(t, storeRoot, "deeplib", "3.0-1")
	// An explicitly recorded EMPTY closure, which is what a real leaf
	// carries. Absent metadata would mean an unknown closure and the
	// walk would rightly refuse, testing the wrong thing.
	if err := depsmeta.Write(deep, depsmeta.Metadata{}); err != nil {
		t.Fatal(err)
	}
	added := seedPkg(t, storeRoot, "newdep", "2.0-1",
		depsmeta.ResolvedDep{Name: "deeplib", Version: "3.0", Revision: 1})
	// The root as installed today: no dependencies at all.
	seedPkg(t, storeRoot, "root", "1.0-1")

	// The rebuilt root, staged, declaring the new dependency.
	staging := filepath.Join(t.TempDir(), ".build-x")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := depsmeta.Write(staging, depsmeta.Metadata{
		Deps: []depsmeta.ResolvedDep{
			{Name: "newdep", Version: "2.0", Revision: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}

	c, err := ProposedClaimantStaged([]farm.Placement{{
		ScanDir:  staging,
		FinalDir: filepath.Join(storeRoot, "root", "1.0-1"),
	}}, galeDir, storeRoot)
	if err != nil {
		t.Fatalf("ProposedClaimantStaged: %v", err)
	}
	if c.Err != nil {
		t.Fatalf("claim reported unreadable: %v", c.Err)
	}
	for _, want := range []string{added, deep} {
		if !slices.Contains(claimDirs(c), want) {
			t.Errorf("claim omits %s; claims = %v", want, claimDirs(c))
		}
	}
}

// Staged metadata that cannot be read leaves the closure unknown,
// and a claim built on an unknown closure approves deletions nobody
// checked. Absent is not empty for the STRICT builder, exactly as it
// is not in the migration veto.
//
// The lenient builder tolerates it, and must: the installer commits
// an artifact whose closure cannot be attested (design §7), so a
// guard on the non-destructive side that refused it would reject
// installs the provenance policy allows. Strictness belongs where
// bytes go away.
func TestProposedClaimantStagedFailsClosedOnUnreadableStagedDeps(t *testing.T) {
	storeRoot := filepath.Join(t.TempDir(), "pkg")
	galeDir := filepath.Join(filepath.Dir(storeRoot), "gale")
	seedPkg(t, storeRoot, "root", "1.0-1")

	// Staged with no metadata at all.
	staging := filepath.Join(t.TempDir(), ".build-y")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}

	p := []farm.Placement{{
		ScanDir:  staging,
		FinalDir: filepath.Join(storeRoot, "root", "1.0-1"),
	}}
	strict, err := ProposedClaimantRequired(p, galeDir, storeRoot)
	if err != nil {
		t.Fatalf("ProposedClaimantRequired: %v", err)
	}
	if strict.Err == nil {
		t.Error("an unreadable staged closure produced a claim a " +
			"deletion could be checked against")
	}
	lenient, err := ProposedClaimantStaged(p, galeDir, storeRoot)
	if err != nil {
		t.Fatalf("ProposedClaimantStaged: %v", err)
	}
	if lenient.Err != nil {
		t.Errorf("the lenient claim refused an artifact the installer "+
			"commits without provenance: %v", lenient.Err)
	}
}

// A dependency the proposed closure names but that is no longer on
// disk leaves the closure UNSATISFIED, not satisfied-by-absence.
//
// The migration veto treats an absent dir as nothing to protect,
// which is right for it and wrong here: dependency locks are
// released before the root commits, so a dependency can be
// collected between the mint and this walk. Claiming a partial
// closure then approves pruning links the proposed closure needs.
func TestProposedClaimantStagedFailsClosedOnAMissingDep(t *testing.T) {
	for _, tc := range []struct{ name, remove string }{
		{"direct", "newdep"},
		{"transitive", "deeplib"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storeRoot := filepath.Join(t.TempDir(), "pkg")
			galeDir := filepath.Join(filepath.Dir(storeRoot), "gale")

			deep := seedPkg(t, storeRoot, "deeplib", "3.0-1")
			if err := depsmeta.Write(deep, depsmeta.Metadata{}); err != nil {
				t.Fatal(err)
			}
			seedPkg(t, storeRoot, "newdep", "2.0-1",
				depsmeta.ResolvedDep{
					Name: "deeplib", Version: "3.0", Revision: 1,
				})
			seedPkg(t, storeRoot, "root", "1.0-1")

			staging := filepath.Join(t.TempDir(), ".build-z")
			if err := os.MkdirAll(staging, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := depsmeta.Write(staging, depsmeta.Metadata{
				Deps: []depsmeta.ResolvedDep{
					{Name: "newdep", Version: "2.0", Revision: 1},
				},
			}); err != nil {
				t.Fatal(err)
			}

			// Collected after the record was minted, before the walk.
			if err := os.RemoveAll(
				filepath.Join(storeRoot, tc.remove),
			); err != nil {
				t.Fatal(err)
			}

			c, err := ProposedClaimantRequired([]farm.Placement{{
				ScanDir:  staging,
				FinalDir: filepath.Join(storeRoot, "root", "1.0-1"),
			}}, galeDir, storeRoot)
			if err != nil {
				t.Fatalf("ProposedClaimantStaged: %v", err)
			}
			if c.Err == nil {
				t.Error("a closure missing a dependency it names was " +
					"reported as usable")
			}
		})
	}
}

// The artifact being REPLACED must not decide the claim. Its
// metadata is precisely what the operation supersedes, so malformed
// metadata there would refuse the refresh that fixes it — and an
// unprovenanced legacy artifact is exactly where malformed metadata
// lives.
func TestProposedClaimantStagedIgnoresTheSupersededMetadata(t *testing.T) {
	storeRoot := filepath.Join(t.TempDir(), "pkg")
	galeDir := filepath.Join(filepath.Dir(storeRoot), "gale")

	old := seedPkg(t, storeRoot, "root", "1.0-1")
	if err := os.WriteFile(
		filepath.Join(old, depsmeta.File), []byte("not toml at all\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	// The rebuilt artifact records a valid empty closure.
	staging := filepath.Join(t.TempDir(), ".build-w")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := depsmeta.Write(staging, depsmeta.Metadata{}); err != nil {
		t.Fatal(err)
	}

	c, err := ProposedClaimantStaged([]farm.Placement{{
		ScanDir: staging, FinalDir: old,
	}}, galeDir, storeRoot)
	if err != nil {
		t.Fatalf("ProposedClaimantStaged: %v", err)
	}
	if c.Err != nil {
		t.Errorf("the superseded artifact's metadata refused the "+
			"operation that replaces it: %v", c.Err)
	}
}

// claimDirs is the final store dir of every claim, which is what the
// old StoreDirs field held. A claim whose bytes are staged reports
// where they will land, so the two agree for every assertion here.
func claimDirs(c farm.Claimant) []string {
	out := make([]string, 0, len(c.Claims))
	for _, p := range c.Claims {
		out = append(out, p.FinalDir)
	}
	return out
}
