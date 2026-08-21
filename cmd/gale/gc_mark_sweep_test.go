package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kelp/gale/internal/filelock"
	"github.com/kelp/gale/internal/store"
)

func TestGCWaitsOnMutateLock(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	jqDir := mkStorePkg(t, storeRoot, "jq", "1.7")
	fdDir := mkStorePkg(t, storeRoot, "fd", "9.0")
	mkActiveGen(t, galeDir, 1, filepath.Join(jqDir, "bin", "jq"))

	held := make(chan struct{})
	release := make(chan struct{})
	holderErr := make(chan error, 1)
	go func() {
		holderErr <- filelock.With(mutateLockPath(galeDir), func() error {
			close(held)
			<-release
			return nil
		})
	}()
	select {
	case <-held:
	case err := <-holderErr:
		t.Fatalf("holder returned early: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("did not acquire mutate.lock")
	}

	done := make(chan error, 1)
	go func() { done <- gcCmd.RunE(gcCmd, nil) }()
	select {
	case <-time.After(50 * time.Millisecond):
	case err := <-done:
		t.Fatalf("gc finished while mutate.lock was held: %v", err)
	}

	if err := os.Symlink(
		filepath.Join(fdDir, "bin", "fd"),
		filepath.Join(galeDir, "gen", "1", "bin", "fd"),
	); err != nil {
		t.Fatal(err)
	}

	close(release)
	if err := <-holderErr; err != nil {
		t.Fatalf("holder: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("gc: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("gc did not finish after mutate.lock release")
	}

	if _, err := os.Stat(fdDir); err != nil {
		t.Errorf("fd@9.0 was linked while gc waited and must be kept: %v", err)
	}
}

func TestGCWaitsOnGenerationLock(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	jqDir := mkStorePkg(t, storeRoot, "jq", "1.8")
	mkActiveGen(t, galeDir, 1, filepath.Join(jqDir, "bin", "jq"))
	mkActiveGen(t, galeDir, 2, filepath.Join(jqDir, "bin", "jq"))
	mkActiveGen(t, galeDir, 3, filepath.Join(jqDir, "bin", "jq"))

	held := make(chan struct{})
	release := make(chan struct{})
	holderErr := make(chan error, 1)
	go func() {
		holderErr <- filelock.With(genLockPath(storeRoot), func() error {
			close(held)
			<-release
			return nil
		})
	}()
	select {
	case <-held:
	case err := <-holderErr:
		t.Fatalf("holder returned early: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("did not acquire generation.lock")
	}

	done := make(chan error, 1)
	go func() { done <- gcCmd.RunE(gcCmd, nil) }()
	select {
	case <-time.After(50 * time.Millisecond):
	case err := <-done:
		t.Fatalf("gc finished while generation.lock was held: %v", err)
	}
	if _, err := os.Stat(filepath.Join(galeDir, "gen", "1")); err != nil {
		t.Errorf("gen/1 deleted while generation.lock was held: %v", err)
	}

	close(release)
	if err := <-holderErr; err != nil {
		t.Fatalf("holder: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("gc: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("gc did not finish after generation.lock release")
	}
	if _, err := os.Stat(filepath.Join(galeDir, "gen", "1")); !os.IsNotExist(err) {
		t.Errorf("gen/1 must be removed after generation.lock release, err=%v", err)
	}
}

func TestGCSweepsConfigPinNotInKeptGens(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	writeGlobalConfig(t, galeDir, "[packages]\nfd = \"9.0\"\n")
	jqDir := mkStorePkg(t, storeRoot, "jq", "1.7")
	fdDir := mkStorePkg(t, storeRoot, "fd", "9.0")
	mkActiveGen(t, galeDir, 1, filepath.Join(jqDir, "bin", "jq"))

	if err := gcCmd.RunE(gcCmd, nil); err != nil {
		t.Fatalf("gc: %v", err)
	}
	if _, err := os.Stat(jqDir); err != nil {
		t.Errorf("jq@1.7 is linked by a kept gen: %v", err)
	}
	if _, err := os.Stat(fdDir); !os.IsNotExist(err) {
		t.Errorf("fd@9.0 is only a config pin and must be swept, err=%v", err)
	}
}

func TestGCKeepsRegisteredProjectGenTargets(t *testing.T) {
	_, storeRoot := setupGCHome(t)
	jqDir := mkStorePkg(t, storeRoot, "jq", "1.7")
	fdDir := mkStorePkg(t, storeRoot, "fd", "9.0")
	proj := makeRegisteredProject(t, storeRoot, "[packages]\njq = \"1.7\"\n", "jq", "1.7", "jq")
	if err := os.WriteFile(
		filepath.Join(os.Getenv("HOME"), ".gale", "projects"),
		[]byte(proj+"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	if err := gcCmd.RunE(gcCmd, nil); err != nil {
		t.Fatalf("gc: %v", err)
	}
	if _, err := os.Stat(jqDir); err != nil {
		t.Errorf("registered project gen target must survive: %v", err)
	}
	if _, err := os.Stat(fdDir); !os.IsNotExist(err) {
		t.Errorf("unreferenced fd must be swept, err=%v", err)
	}
}

func TestGCHostOverlayNotInKeptGensIsSwept(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	other := currentHost(t) + "-other"
	writeGlobalConfig(t, galeDir,
		"[packages]\n[hosts."+other+".packages]\nfd = \"9.0\"\n")
	jqDir := mkStorePkg(t, storeRoot, "jq", "1.7")
	fdDir := mkStorePkg(t, storeRoot, "fd", "9.0")
	mkActiveGen(t, galeDir, 1, filepath.Join(jqDir, "bin", "jq"))

	if err := gcCmd.RunE(gcCmd, nil); err != nil {
		t.Fatalf("gc: %v", err)
	}
	if _, err := os.Stat(jqDir); err != nil {
		t.Errorf("linked jq must stay: %v", err)
	}
	if _, err := os.Stat(fdDir); !os.IsNotExist(err) {
		t.Errorf("host overlay pin with no kept-gen link must be swept, err=%v", err)
	}
}

func TestGCRefusesIncompleteRetentionNoForce(t *testing.T) {
	storeRoot, proj := gcUnreadableProjectFixture(t)
	breakGenerationWalk(t, filepath.Join(proj, ".gale"))

	err := gcCmd.RunE(gcCmd, nil)
	if err == nil {
		t.Fatal("gc must refuse incomplete retention")
	}
	if strings.Contains(err.Error(), "--force") {
		t.Errorf("refusal must not advertise --force, got: %v", err)
	}
	if gcCmd.Flags().Lookup("force") != nil {
		t.Error("gc must not have a --force flag")
	}
	assertGCRefused(t, err, filepath.Join(proj, ".gale"), storeRoot)
}

func TestGCNeverDeletesFetchStaging(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	jqDir := mkStorePkg(t, storeRoot, "jq", "1.7")
	mkActiveGen(t, galeDir, 1, filepath.Join(jqDir, "bin", "jq"))
	orphan := mkFetchPkg(t, storeRoot, "fd", "10.0.0", storeFetchSHA256B)
	ageEntry(t, orphan)
	staging := filepath.Join(storeRoot, store.FetchNamespace, ".tmp-live")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	unused := mkStorePkg(t, storeRoot, "old", "1.0")

	err := gcCmd.RunE(gcCmd, nil)
	if err == nil {
		t.Fatal("gc must refuse the fetch sweep while staging is live")
	}
	if !strings.Contains(err.Error(), staging) {
		t.Errorf("refusal must name %s, got: %v", staging, err)
	}
	if _, err := os.Stat(staging); err != nil {
		t.Errorf("staging directory must survive: %v", err)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Errorf("fetch identity must survive a refused fetch sweep: %v", err)
	}
	if _, err := os.Stat(unused); !os.IsNotExist(err) {
		t.Errorf("source layout may still sweep, err=%v", err)
	}
}

func TestGCSweepsUnreferencedFetchIdentity(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	jqDir := mkStorePkg(t, storeRoot, "jq", "1.7")
	mkActiveGen(t, galeDir, 1, filepath.Join(jqDir, "bin", "jq"))
	orphan := mkFetchPkg(t, storeRoot, "fd", "10.0.0", storeFetchSHA256A)
	ageEntry(t, orphan)

	if err := gcCmd.RunE(gcCmd, nil); err != nil {
		t.Fatalf("gc: %v", err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("old unlinked fetch identity must be swept, err=%v", err)
	}
}

func TestGCPrefixSafeFetchIdentity(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	keep := mkFetchPkg(t, storeRoot, "jq", "1.8.1", storeFetchSHA256A)
	drop := mkFetchPkg(t, storeRoot, "jq", "1.8.1", storeFetchSHA256B)
	prefix := filepath.Join(storeRoot, store.FetchNamespace, "jq", "1.8.1")
	if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	ageEntry(t, drop)
	ageEntry(t, prefix)
	if err := os.MkdirAll(filepath.Join(galeDir, "gen", "1", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join(keep, "bin", "jq"),
		filepath.Join(galeDir, "gen", "1", "bin", "jq"),
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("gen/1", filepath.Join(galeDir, "current")); err != nil {
		t.Fatal(err)
	}

	if err := gcCmd.RunE(gcCmd, nil); err != nil {
		t.Fatalf("gc: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("retained sha12 must stay: %v", err)
	}
	if _, err := os.Stat(drop); !os.IsNotExist(err) {
		t.Errorf("unlinked sha12 sibling must be swept, err=%v", err)
	}
	if _, err := os.Stat(prefix); err != nil {
		t.Errorf("short prefix sibling is not a sha12 identity: %v", err)
	}
}

func TestGCDryRunRefusesIncompleteRetention(t *testing.T) {
	storeRoot, proj := gcUnreadableProjectFixture(t)
	breakGenerationWalk(t, filepath.Join(proj, ".gale"))
	dryRun = true
	t.Cleanup(func() { dryRun = false })

	var runErr error
	stderr := captureStderr(t, func() {
		runErr = gcCmd.RunE(gcCmd, nil)
	})
	if runErr == nil {
		t.Fatal("gc -n must refuse incomplete retention")
	}
	if strings.Contains(stderr, "Would remove") {
		t.Errorf("dry-run must not report removals from an incomplete set: %s", stderr)
	}
	if _, err := os.Stat(filepath.Join(storeRoot, "fd", "9.0")); err != nil {
		t.Errorf("dry-run must not sweep: %v", err)
	}
}

func TestGCKeepsYoungUnlinkedFetchIdentity(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	jqDir := mkStorePkg(t, storeRoot, "jq", "1.7")
	mkActiveGen(t, galeDir, 1, filepath.Join(jqDir, "bin", "jq"))
	young := mkFetchPkg(t, storeRoot, "fd", "10.0.0", storeFetchSHA256A)

	if err := gcCmd.RunE(gcCmd, nil); err != nil {
		t.Fatalf("gc: %v", err)
	}
	if _, err := os.Stat(young); err != nil {
		t.Errorf("young unlinked fetch dest must survive grace: %v", err)
	}
}

func TestGCHasNoResolver(t *testing.T) {
	data, err := os.ReadFile("gc.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	for _, bad := range []string{"newCmdContext", "installer.RecipeResolver"} {
		if strings.Contains(src, bad) {
			t.Errorf("gc.go must not mention %s", bad)
		}
	}
}

const (
	storeFetchSHA256A = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	storeFetchSHA256B = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func mkFetchPkg(t *testing.T, storeRoot, name, version, sha256 string) string {
	t.Helper()
	dir, err := store.NewStore(storeRoot).FetchPath(name, version, sha256)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "bin", name), []byte("x"), 0o755,
	); err != nil {
		t.Fatal(err)
	}
	return dir
}
