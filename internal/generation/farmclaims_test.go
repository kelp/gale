package generation

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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
	soname := stem + ".4.dylib"
	if runtime.GOOS == "linux" {
		soname = stem + ".so.4"
	}
	lib := filepath.Join(dir, "lib")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(lib, soname), []byte("x"), 0o644,
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
	if len(c.StoreDirs) != len(want) {
		t.Fatalf("StoreDirs = %v, want curl and zstd only", c.StoreDirs)
	}
	for _, d := range c.StoreDirs {
		if !want[d] {
			t.Errorf("unexpected claimed dir %s (build deps must "+
				"not be claimed)", d)
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
			if len(c.StoreDirs) != 1 || c.StoreDirs[0] != curl {
				t.Errorf("StoreDirs = %v, want [%s]", c.StoreDirs, curl)
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
	if len(c.StoreDirs) != 1 || c.StoreDirs[0] != curl {
		t.Errorf("StoreDirs = %v, want [%s]", c.StoreDirs, curl)
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
