package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/projects"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/recipe"
)

// Regenerating a scope moves its symlinks from a pre-revision bare
// directory into the canonical one.
//
// This is the half of a relocation that no per-scope command may
// perform, and the reason design §13 hands pre-revision directories
// to machine-wide migrate: another scope's generation links the bare
// path, and moving the identity without repairing those symlinks
// would break a scope whose owner ran nothing.
//
// The package set comes from the ACTIVE generation, so it arrives as
// the bare "1.0" the link actually names, and store resolution
// carries it to the "1.0-1" that now exists beside it.
func TestRegenerateScopeFollowsIntoTheCanonicalDir(t *testing.T) {
	home := t.TempDir()
	storeRoot := filepath.Join(home, "pkg")
	galeDir := filepath.Join(home, ".gale")

	// The machine as it was before revisions existed.
	seedStore(t, storeRoot, "old", "1.0")
	if err := generation.Build(
		map[string]string{"old": "1.0"}, galeDir, storeRoot,
	); err != nil {
		t.Fatal(err)
	}
	if got := linkTarget(t, galeDir, "old"); !strings.Contains(
		got, filepath.Join("old", "1.0"),
	) {
		t.Fatalf("the fixture does not link the bare dir: %s", got)
	}

	// Migrate has installed the canonical artifact beside it.
	seedStore(t, storeRoot, "old", "1.0-1")

	if err := regenerateScope(projects.Scope{
		Label: "the global scope", GaleDir: galeDir,
	}, storeRoot, discardOutput()); err != nil {
		t.Fatal(err)
	}

	got := linkTarget(t, galeDir, "old")
	if !strings.Contains(got, filepath.Join("old", "1.0-1")) {
		t.Errorf("the generation still links %s, want the canonical dir", got)
	}
}

// linkTarget reads where a scope's active generation points one
// binary, without resolving it: the question is which store
// directory the link NAMES.
func linkTarget(t *testing.T, galeDir, name string) string {
	t.Helper()
	target, err := os.Readlink(filepath.Join(galeDir, "current", "bin", name))
	if err != nil {
		t.Fatal(err)
	}
	return target
}

// The pre-revision directory is removed only after the closure walk
// proves no scope reaches it.
//
// Rebuilding a generation moves the ROOTS that resolved to the bare
// directory. A transitive dependency is reached through a recorded
// closure rather than through any symlink, so a rebuild cannot move
// it, and deleting the directory anyway would break a dependent that
// resolves it at runtime. Leaving one directory behind is the honest
// outcome; destroying referenced bytes is not.
// seedRelocation builds a finished relocation: the pre-revision
// directory, the attested canonical one beside it, and optionally a
// scope that reaches the first or disagrees about the second.
//
// The global scope's gale dir IS galeHome, which in production is
// filepath.Dir(storeRoot). Nesting a ".gale" under it would build a
// generation projects.Scopes never looks at, and every veto would
// pass by seeing nothing.
func seedRelocation(
	t *testing.T, home, storeRoot string, referenced bool, lockSHA string,
) (string, migrateTarget) {
	t.Helper()
	bare := seedStore(t, storeRoot, "old", "1.0")
	writeDepsMeta(t, storeRoot, "old", "1.0")
	if referenced {
		if err := generation.Build(
			map[string]string{"old": "1.0"}, home, storeRoot,
		); err != nil {
			t.Fatal(err)
		}
	}
	if lockSHA != "" {
		proj := t.TempDir()
		if err := projects.Register(home, proj); err != nil {
			t.Fatal(err)
		}
		writeScopeLock(t, filepath.Join(proj, "gale.lock"),
			"old@1.0-1", lockSHA)
	}
	// The canonical artifact migrate would have installed, attested,
	// which the removal re-proves before it acts.
	seedProvenanced(t, storeRoot, "old", "1.0-1")
	r, err := migrateResolver("old")("old", "1.0-1")
	if err != nil {
		t.Fatal(err)
	}
	return bare, migrateTarget{
		name: "old", version: "1.0-1", dir: bare, bare: true, recipe: r,
	}
}

func TestRemoveRelocatedDirKeepsAReferencedDir(t *testing.T) {
	tests := []struct {
		name string
		// referenced puts the bare directory inside a scope's active
		// closure, which is the state that must stop the removal.
		referenced bool
		// lockSHA registers a project whose lock names these bytes for
		// the identity. Set to something other than the recipe's
		// declared hash, it is a scope disagreeing about what the
		// store should hold.
		lockSHA  string
		wantGone bool
		wantErr  error
	}{
		{
			name: "a referenced dir survives", referenced: true,
			wantErr: errBareDirStillReferenced,
		},
		{name: "an unreferenced dir is removed", wantGone: true},
		{
			// The removal is its own destructive commit, so it
			// re-establishes the whole relocation rather than trusting
			// what was checked before the generations were rebuilt. A
			// scope that re-locked in that window is exactly what the
			// recheck exists to catch.
			name: "a disagreeing scope survives", lockSHA: shaY,
			wantErr: errScopeDisagrees,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			storeRoot := filepath.Join(home, "pkg")
			bare, target := seedRelocation(
				t, home, storeRoot, tt.referenced, tt.lockSHA,
			)

			err := removeRelocatedDir(storeRoot, home, target, discardOutput())
			if tt.wantErr == nil && err != nil {
				t.Fatalf("an unreferenced dir was not removed: %v", err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}

			_, err = os.Lstat(bare)
			if tt.wantGone && err == nil {
				t.Error("an unreferenced pre-revision dir was left behind")
			}
			if !tt.wantGone && err != nil {
				t.Errorf("a referenced pre-revision dir was destroyed: %v", err)
			}
		})
	}
}

// A record that parses is not a record that attests the directory it
// sits in.
//
// This predicate authorizes deleting the pre-revision bytes, so it
// has to prove the canonical directory really holds the migrated
// artifact. Method and SHA alone do not: a record copied from
// another package carries the same declared hash whenever the
// fixture's recipes share one, and would authorize the deletion on
// the strength of a file somebody moved.
func TestCanonicalAttestsRejectsARecordForAnotherIdentity(t *testing.T) {
	home := t.TempDir()
	storeRoot := filepath.Join(home, "pkg")
	seedStore(t, storeRoot, "old", "1.0-1")
	// The record belongs to a different package and is written into
	// old's directory, which is what a copy or a partial restore
	// leaves behind.
	seedStore(t, storeRoot, "other", "1.0-1")
	writeProvenance(t, storeRoot, "other", "1.0-1")
	moveProvenance(t, storeRoot, "other", "old", "1.0-1")

	r, err := migrateResolver("old")("old", "1.0-1")
	if err != nil {
		t.Fatal(err)
	}
	target := migrateTarget{
		name: "old", version: "1.0-1",
		dir:    filepath.Join(storeRoot, "old", "1.0"),
		bare:   true,
		recipe: r,
	}

	if err := canonicalAttests(storeRoot, target); err == nil {
		t.Fatal("a record for another identity authorized the removal")
	}
}

// A relocating migration commits its canonical artifact without
// asking the per-commit farm guard.
//
// The guard cannot be asked, because it must refuse. Migrate runs
// with a GLOBAL context, so every registered project is an external
// claimant, and a project loading a versioned library out of a
// pre-revision BARE directory claims that soname at the bare path.
// The canonical copy proposes the same soname at a different
// directory, which is the one thing design §4 tells GuardPopulate to
// refuse — so the command that exists to end the pre-revision layout
// would deadlock against it.
//
// Deferring is safe in the direction that matters: the install adds a
// directory and touches neither the bare bytes nor a single farm
// link, so a failure leaves the machine exactly as it was. The farm
// is put right once, machine-wide, after every canonical artifact is
// in place (finishRelocations).
func TestMigrateOneRelocatingDefersTheFarm(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")

	// The machine before revisions: the library sits in a bare
	// directory and another project's generation loads it from there.
	bare := seedStore(t, storeRoot, "libfoo", "1.0")
	seedDylib(t, bare)
	writeDepsMeta(t, storeRoot, "libfoo", "1.0")
	proj := t.TempDir()
	if err := projects.Register(galeDir, proj); err != nil {
		t.Fatal(err)
	}
	if err := generation.Build(map[string]string{"libfoo": "1.0"},
		filepath.Join(proj, ".gale"), storeRoot); err != nil {
		t.Fatal(err)
	}

	r := dylibBinaryRecipe(t, "libfoo")
	ctx := buildFakeCtx(t, filepath.Join(galeDir, "gale.toml"),
		galeDir, storeRoot, func(string) (*recipe.Recipe, error) {
			return r, nil
		})
	wireFarmGuards(ctx.Installer, ctx.GaleDir, ctx.StoreRoot)

	moved, err := migrateOne(ctx, galeDir, migrateTarget{
		name: "libfoo", version: "1.0-1", dir: bare, bare: true, recipe: r,
	}, discardOutput())
	if err != nil {
		t.Fatalf("a relocating migration was refused: %v", err)
	}
	if !moved {
		t.Fatal("a relocating migration must report the move")
	}
	canonical := filepath.Join(storeRoot, "libfoo", "1.0-1")
	if _, err := os.Stat(
		filepath.Join(canonical, "lib", sonameFor("libfoo")),
	); err != nil {
		t.Fatalf("the canonical artifact is not installed: %v", err)
	}
	// Untouched, not repointed: nothing has moved the project's
	// symlinks yet, so its binaries still resolve through the bare
	// directory and the farm must still describe that.
	target, err := os.Readlink(filepath.Join(
		farm.DirFromStoreRoot(storeRoot), sonameFor("libfoo"),
	))
	if err != nil {
		t.Fatalf("reading the farm entry: %v", err)
	}
	if !strings.HasPrefix(target, bare+string(filepath.Separator)) {
		t.Errorf("the farm links %s, want the pre-revision %s: the "+
			"population belongs to the machine-wide rebuild", target, bare)
	}
}

// The farm follows a relocation even where no scope's generation
// does.
//
// Something has to put the shared farm right, because the install
// deferred it. A scope regeneration is not that something: a scope
// that claims the soname through its LOCK alone has no generation to
// rebuild, regenerateScope skips it, and the farm is left pointing
// into the very directory this pass then deletes. Nothing reports the
// dangling link — the next binary to load that library simply fails.
func TestFinishRelocationsRepointsTheFarm(t *testing.T) {
	home := t.TempDir()
	storeRoot := filepath.Join(home, "pkg")
	proj := t.TempDir()
	if err := projects.Register(home, proj); err != nil {
		t.Fatal(err)
	}
	// The scope requires the identity and links nothing, which is
	// what a lock without a sync looks like.
	writeScopeLock(t, filepath.Join(proj, "gale.lock"),
		"libfoo@1.0-1", testSHA)

	bare := seedStore(t, storeRoot, "libfoo", "1.0")
	seedDylib(t, bare)
	writeDepsMeta(t, storeRoot, "libfoo", "1.0")
	farmDir := farm.DirFromStoreRoot(storeRoot)
	if err := farm.Populate(bare, farmDir); err != nil {
		t.Fatal(err)
	}

	// The canonical artifact migrate has just installed and attested.
	seedProvenanced(t, storeRoot, "libfoo", "1.0-1")
	canonical := filepath.Join(storeRoot, "libfoo", "1.0-1")
	seedDylib(t, canonical)
	writeDepsMeta(t, storeRoot, "libfoo", "1.0-1")

	r, rerr := migrateResolver("libfoo")("libfoo", "1.0-1")
	if rerr != nil {
		t.Fatal(rerr)
	}
	if err := finishRelocations(
		&cmdContext{StoreRoot: storeRoot}, home,
		[]migrateTarget{{
			name: "libfoo", version: "1.0-1", dir: bare, bare: true, recipe: r,
		}},
		discardOutput(),
	); err != nil {
		t.Fatalf("finishRelocations: %v", err)
	}

	target, err := os.Readlink(filepath.Join(farmDir, sonameFor("libfoo")))
	if err != nil {
		t.Fatalf("reading the farm entry: %v", err)
	}
	if !strings.HasPrefix(target, canonical+string(filepath.Separator)) {
		t.Errorf("the farm links %s, want the canonical %s: the "+
			"pre-revision directory it names is gone", target, canonical)
	}
}

// seedDylib gives a store directory one versioned library, which is
// what puts it in the shared farm at all.
func seedDylib(t *testing.T, dir string) {
	t.Helper()
	lib := filepath.Join(dir, "lib")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(lib, sonameFor("libfoo")), []byte("bytes"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
}

// dylibBinaryRecipe serves a prebuilt archive holding one versioned
// library and returns the recipe that declares it. Migrate installs
// binary-method artifacts only, so a source-building fixture cannot
// reach the code under test.
func dylibBinaryRecipe(t *testing.T, name string) *recipe.Recipe {
	t.Helper()
	return &recipe.Recipe{
		Package: recipe.Package{Name: name, Version: "1.0"},
		Binary: servedBinary(t, archiveEntry{
			name: "lib/" + sonameFor("libfoo"), body: "bytes", mode: 0o644,
		}),
	}
}

// hashOf is the hex SHA256 of a file, which is what a recipe's
// binary declares and the installer verifies the download against.
func hashOf(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// moveProvenance relocates a written record into another package's
// store directory, which no gale code path produces and a restore
// from a mixed-up backup does.
func moveProvenance(t *testing.T, storeRoot, from, to, version string) {
	t.Helper()
	src := filepath.Join(storeRoot, from, version, provenance.File)
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(storeRoot, to, version, provenance.File)
	if err := os.WriteFile(dst, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// A scope that disagrees stops the pass BEFORE the farm moves.
//
// The unit test on RebuildFarm proves only that it runs whatever
// callback it is given. This one goes through the production caller,
// so it also pins that finishRelocations supplies the right predicate
// and applies it to every relocated target — the pipeline regression
// the callback exists to prevent.
//
// The error alone does not discriminate. A scope's disagreement is
// caught by the per-removal check either way, and the pre-revision
// bytes survive either way, so a run without the callback looks
// safe while having already repointed a soname the disagreeing scope
// resolves through. The farm assertion is what separates the two.
func TestFinishRelocationsRefusesBeforeTheFarmMoves(t *testing.T) {
	tests := []struct {
		name string
		// lockSHA gives a registered scope a lock naming these bytes.
		// Other than the recipe's declared hash, it is a scope that
		// disagrees about what the store should hold.
		lockSHA string
		// raced attests the pre-revision directory, as a concurrent
		// run that got there first would.
		raced   bool
		wantErr error
	}{
		{
			name: "a scope disagrees", lockSHA: shaY,
			wantErr: errScopeDisagrees,
		},
		{
			// The pre-revision directory stopped being the
			// unprovenanced thing this pass decided about. Deleting it
			// would destroy the record another run just wrote, which is
			// the one thing the design says replacement must never do.
			name: "the bare dir gained a record", raced: true,
			wantErr: errRaceLostToProvenance,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			storeRoot := filepath.Join(home, "pkg")
			if tt.lockSHA != "" {
				proj := t.TempDir()
				if err := projects.Register(home, proj); err != nil {
					t.Fatal(err)
				}
				writeScopeLock(t, filepath.Join(proj, "gale.lock"),
					"libfoo@1.0-1", tt.lockSHA)
			}

			bare := seedStore(t, storeRoot, "libfoo", "1.0")
			seedDylib(t, bare)
			writeDepsMeta(t, storeRoot, "libfoo", "1.0")
			farmDir := farm.DirFromStoreRoot(storeRoot)
			if err := farm.Populate(bare, farmDir); err != nil {
				t.Fatal(err)
			}

			seedProvenanced(t, storeRoot, "libfoo", "1.0-1")
			seedDylib(t, filepath.Join(storeRoot, "libfoo", "1.0-1"))
			writeDepsMeta(t, storeRoot, "libfoo", "1.0-1")
			if tt.raced {
				copyProvenance(t, storeRoot, "libfoo", "1.0-1", bare)
			}

			r, rerr := migrateResolver("libfoo")("libfoo", "1.0-1")
			if rerr != nil {
				t.Fatal(rerr)
			}
			err := finishRelocations(
				&cmdContext{StoreRoot: storeRoot}, home,
				[]migrateTarget{{
					name: "libfoo", version: "1.0-1", dir: bare,
					bare: true, recipe: r,
				}},
				discardOutput(),
			)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}

			target, rlerr := os.Readlink(
				filepath.Join(farmDir, sonameFor("libfoo")),
			)
			if rlerr != nil {
				t.Fatalf("the refused pass destroyed the farm entry: %v", rlerr)
			}
			if !strings.HasPrefix(target, bare+string(filepath.Separator)) {
				t.Errorf("the farm moved to %s despite the refusal; the "+
					"scope that objected resolves that soname", target)
			}
		})
	}
}

// copyProvenance puts one directory's record into another, which is
// what the store's bare fallback produces: an install of the
// canonical identity resolves to a pre-revision directory and commits
// its record there.
func copyProvenance(t *testing.T, storeRoot, name, version, dst string) {
	t.Helper()
	body, err := os.ReadFile(
		filepath.Join(storeRoot, name, version, provenance.File),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dst, provenance.File), body, 0o644,
	); err != nil {
		t.Fatal(err)
	}
}
