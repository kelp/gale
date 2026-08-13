package generation

import (
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/depsmeta"
)

// gh#238 asked whether FarmStoreDirsStrict should read dependency
// metadata through depsmeta.ReadStrict, which separates StateAbsent
// from StateRecorded, rather than depsmeta.Read, which decodes a
// missing .gale-deps.toml to an empty Metadata. The answer is no, and
// this file is where that answer is enforced rather than remembered.
//
// A committed store dir with no metadata is a LEAF. closure.go's
// proposed() states why: calling it unknown would make the claim
// unusable for every package installed before metadata existed, and an
// unusable claim refuses every operation on the machine. The swap
// looks like a one-line cleanup, which is exactly why it needs a test
// under it — the three callers that would change behavior
// (ProposedClaimant, scopeClosureDirs, and gc's expandInstalledDeps)
// all fail closed, so the cost of getting this wrong is a machine that
// installs and removes nothing until its whole store is reinstalled.

// TestFarmStoreDirsStrictTreatsAbsentDepsMetadataAsLeaf pins the
// invariant: the strict walk must SUCCEED over a store dir that has no
// .gale-deps.toml, and must report that dir as reached.
//
// The store is mixed on purpose — one pre-metadata package beside one
// that records a dep. That is the shape of every real machine partway
// through the upgrade that introduced the file, and it keeps the test
// from passing on an empty walk.
func TestFarmStoreDirsStrictTreatsAbsentDepsMetadataAsLeaf(t *testing.T) {
	storeRoot := t.TempDir()

	// The package under test: installed before .gale-deps.toml existed.
	createStoreEntry(t, storeRoot, "legacy", "1.0", []string{"legacy"})
	legacyDir := filepath.Join(storeRoot, "legacy", "1.0")
	if depsmeta.Has(legacyDir) {
		t.Fatalf(
			"fixture wrote %s into %s; the case under test is its ABSENCE",
			depsmeta.File, legacyDir,
		)
	}

	// A package that does record its closure, so the walk has something
	// to descend into and the success being asserted is not vacuous.
	createStoreEntry(t, storeRoot, "modern", "2.0-1", []string{"modern"})
	modernDir := filepath.Join(storeRoot, "modern", "2.0-1")
	createStoreEntry(t, storeRoot, "libdep", "3.0-1", nil)
	libdepDir := filepath.Join(storeRoot, "libdep", "3.0-1")
	if err := depsmeta.Write(modernDir, depsmeta.Metadata{
		Deps: []depsmeta.ResolvedDep{
			{Name: "libdep", Version: "3.0", Revision: 1},
		},
	}); err != nil {
		t.Fatalf("write modern deps metadata: %v", err)
	}

	dirs, err := FarmStoreDirsStrict(map[string]string{
		"legacy": "1.0",
		"modern": "2.0-1",
	}, storeRoot)
	if err != nil {
		t.Fatalf(
			"FarmStoreDirsStrict errored on a store dir with no %s: %v\n"+
				"A committed directory with no recorded metadata is a LEAF, "+
				"not an unknown closure (gh#238). Reading this walk through "+
				"depsmeta.ReadStrict makes every pre-metadata install refuse "+
				"every install and removal on the machine, with no upgrade "+
				"path. See closure.go's proposed().",
			depsmeta.File, err,
		)
	}

	for _, want := range []string{legacyDir, modernDir, libdepDir} {
		if !containsDir(dirs, want) {
			t.Errorf("FarmStoreDirsStrict = %v, want to include %s", dirs, want)
		}
	}
	// Exactly those three: the legacy dir contributes itself and nothing
	// else, which is what "leaf" means here.
	if len(dirs) != 3 {
		t.Errorf("FarmStoreDirsStrict = %v, want exactly 3 dirs", dirs)
	}
}
