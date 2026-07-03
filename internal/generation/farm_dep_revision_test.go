package generation

// gh#172 — FarmStoreDirs resolved each recorded dep at its
// exact version-revision, so when a dep's installed revision
// advanced past the revision recorded in a dependent's
// .gale-deps.toml the dep dir silently dropped out of the farm
// (the missing exact dir was skipped by the os.Stat guard).
// The version pin keeps SONAME/ABI identity; the revision must
// float to the highest installed revision, exactly as top-level
// generation entries resolve.

import (
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/depsmeta"
)

// containsDir reports whether dirs contains target.
func containsDir(dirs []string, target string) bool {
	for _, d := range dirs {
		if d == target {
			return true
		}
	}
	return false
}

// gh#172: a dep recorded at foo 1.2.3 revision 2 whose only
// on-disk install is foo/1.2.3-3 must still enter the farm at
// the installed revision. Before the fix, FarmStoreDirs asked
// the store for the exact "1.2.3-2" dir, which does not exist,
// and the os.Stat guard dropped it.
func TestFarmStoreDirsFloatsDepRevisionToInstalled(t *testing.T) {
	storeRoot := t.TempDir()

	mainDir := filepath.Join(storeRoot, "mainpkg", "1.0-1")
	createStoreEntry(t, storeRoot, "mainpkg", "1.0-1", []string{"mainpkg"})
	// Only revision 3 of the dep is installed.
	installedDepDir := filepath.Join(storeRoot, "foo", "1.2.3-3")
	createStoreEntry(t, storeRoot, "foo", "1.2.3-3", nil)

	// mainpkg was built against foo revision 2, which has since
	// been superseded on disk by revision 3.
	if err := depsmeta.Write(mainDir, depsmeta.Metadata{
		Deps: []depsmeta.ResolvedDep{
			{Name: "foo", Version: "1.2.3", Revision: 2},
		},
	}); err != nil {
		t.Fatalf("write mainpkg deps metadata: %v", err)
	}

	dirs := FarmStoreDirs(map[string]string{"mainpkg": "1.0-1"}, storeRoot)

	if !containsDir(dirs, installedDepDir) {
		t.Errorf(
			"FarmStoreDirs = %v, want to include %s "+
				"(dep dropped when installed revision advanced "+
				"past recorded .gale-deps.toml revision)",
			dirs, installedDepDir,
		)
	}
}

// The complementary case: when the exact recorded revision is
// the highest installed revision, that dir is used. Pins the
// pre-existing correct behavior so the gh#172 fix does not
// regress it.
func TestFarmStoreDirsUsesRecordedRevisionWhenInstalled(t *testing.T) {
	storeRoot := t.TempDir()

	mainDir := filepath.Join(storeRoot, "mainpkg", "1.0-1")
	createStoreEntry(t, storeRoot, "mainpkg", "1.0-1", []string{"mainpkg"})
	recordedDepDir := filepath.Join(storeRoot, "foo", "1.2.3-2")
	createStoreEntry(t, storeRoot, "foo", "1.2.3-2", nil)

	if err := depsmeta.Write(mainDir, depsmeta.Metadata{
		Deps: []depsmeta.ResolvedDep{
			{Name: "foo", Version: "1.2.3", Revision: 2},
		},
	}); err != nil {
		t.Fatalf("write mainpkg deps metadata: %v", err)
	}

	dirs := FarmStoreDirs(map[string]string{"mainpkg": "1.0-1"}, storeRoot)

	if !containsDir(dirs, recordedDepDir) {
		t.Errorf(
			"FarmStoreDirs = %v, want to include %s",
			dirs, recordedDepDir,
		)
	}
}
