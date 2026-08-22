package installer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestReplaceStoreDirWrapsRestoreError pins the errorlint
// dual-wrap: when promote and restore both fail, callers can
// match either error with errors.Is. The pre-fix line hid
// restoreErr behind %v.
func TestReplaceStoreDirWrapsRestoreError(t *testing.T) {
	root := t.TempDir()
	storeDir := filepath.Join(root, "pkg")
	buildDir := filepath.Join(root, "staging")
	if err := os.Mkdir(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(buildDir, 0o755); err != nil {
		t.Fatal(err)
	}

	promoteErr := errors.New("promote failed")
	restoreErr := errors.New("restore failed")

	orig := renameDir
	t.Cleanup(func() { renameDir = orig })
	renameDir = func(oldPath, newPath string) error {
		if oldPath == storeDir && newPath == storeDir+".bak" {
			return orig(oldPath, newPath)
		}
		if oldPath == buildDir && newPath == storeDir {
			return promoteErr
		}
		if oldPath == storeDir+".bak" && newPath == storeDir {
			return restoreErr
		}
		return orig(oldPath, newPath)
	}

	err := replaceStoreDir(storeDir, buildDir)
	if err == nil {
		t.Fatal("expected dual-failure error")
	}
	if !errors.Is(err, promoteErr) {
		t.Errorf("errors.Is(err, promoteErr) = false, err = %v", err)
	}
	if !errors.Is(err, restoreErr) {
		t.Errorf("errors.Is(err, restoreErr) = false, err = %v", err)
	}
}
