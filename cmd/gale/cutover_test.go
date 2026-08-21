package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kelp/gale/internal/index"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/store"
)

func TestCutoverDropsRecipesOnInstallerVerbs(t *testing.T) {
	for _, name := range []string{"install", "sync", "update", "remove", "lock"} {
		cmd := findCmd(name)
		if cmd == nil {
			t.Fatalf("command %q missing", name)
		}
		if cmd.Flags().Lookup("recipes") != nil {
			t.Errorf("%s: --recipes must be gone", name)
		}
	}
}

func TestCutoverIndexOnResolveVerbsOnly(t *testing.T) {
	for _, name := range []string{"install", "update", "lock"} {
		cmd := findCmd(name)
		if cmd == nil {
			t.Fatalf("command %q missing", name)
		}
		if cmd.Flags().Lookup("index") == nil {
			t.Errorf("%s: --index must exist", name)
		}
	}
	for _, name := range []string{"sync", "remove"} {
		cmd := findCmd(name)
		if cmd == nil {
			t.Fatalf("command %q missing", name)
		}
		if cmd.Flags().Lookup("index") != nil {
			t.Errorf("%s: --index must not exist", name)
		}
	}
}

func TestCutoverSourceFlagsGone(t *testing.T) {
	gone := map[string][]string{
		"install": {"recipe", "path", "git", "build"},
		"sync":    {"build", "no-frozen"},
		"update": {
			"recipe", "path", "git", "build",
			"no-install", "no-refresh",
		},
	}
	for name, flags := range gone {
		cmd := findCmd(name)
		if cmd == nil {
			t.Fatalf("command %q missing", name)
		}
		for _, flag := range flags {
			if cmd.Flags().Lookup(flag) != nil {
				t.Errorf("%s: --%s must be gone", name, flag)
			}
		}
	}
}

func stageTestFetch(_ context.Context, st *store.Store, name, version string, a index.Artifact) (string, error) {
	dest, err := st.FetchPath(name, version, a.SHA256)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(dest, "bin"), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dest, "bin", name), []byte("ok"), 0o755); err != nil {
		return "", err
	}
	if err := provenance.WriteFetch(dest, provenance.FetchRecord{
		Name: name, Version: version, SHA256: a.SHA256,
		TreeDigest: a.TreeDigest, Method: provenance.MethodFetch,
	}); err != nil {
		return "", err
	}
	return dest, nil
}

func TestInstallFetchesAndWritesV2(t *testing.T) {
	clearAdoptCI(t)
	fx := newLockFetchFix(t)
	if err := os.WriteFile(fx.c.GalePath, []byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	installToStore = func(ctx context.Context, st *store.Store, name, version string, a index.Artifact) (string, error) {
		calls.Add(1)
		return stageTestFetch(ctx, st, name, version, a)
	}
	t.Cleanup(func() { installToStore = nil })
	if err := runInstallFetch(context.Background(), fx.c, "just", "1.56.0", fx.src); err != nil {
		t.Fatalf("install: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("ToStore calls = %d, want 1", calls.Load())
	}
	got, err := lockfile.ReadV2(fx.lockPath())
	if err != nil {
		t.Fatalf("ReadV2: %v", err)
	}
	if _, ok := got.Packages["just@1.56.0"]; !ok {
		t.Errorf("packages = %v, want just@1.56.0", got.Packages)
	}
	cfg, err := os.ReadFile(fx.c.GalePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "just") {
		t.Errorf("gale.toml = %s, want just", cfg)
	}
}

func TestInstallRefusesHostFlag(t *testing.T) {
	fx := newLockFetchFix(t)
	fx.c.Host = "otherbox"
	err := runInstallFetch(context.Background(), fx.c, "just", "1.56.0", fx.src)
	if !errors.Is(err, errSwitchHosts) {
		t.Fatalf("err = %v, want errSwitchHosts", err)
	}
	if _, err := lockfile.ReadV2(fx.lockPath()); err == nil {
		t.Error("host refuse wrote a v2 lock")
	}
}

func TestInstallKeepsUnrelatedLockedRoot(t *testing.T) {
	clearAdoptCI(t)
	fx := newLockFetchFix(t)
	if err := runLockFetch(context.Background(), fx.c, fx.req("fd@10.2.0")); err != nil {
		t.Fatal(err)
	}
	installToStore = stageTestFetch
	t.Cleanup(func() { installToStore = nil })
	if err := os.WriteFile(fx.c.GalePath, []byte("[packages]\nfd = \"10.2.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runInstallFetch(context.Background(), fx.c, "just", "1.56.0", fx.src); err != nil {
		t.Fatalf("install: %v", err)
	}
	got, err := lockfile.ReadV2(fx.lockPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Packages["fd@10.2.0"]; !ok {
		t.Errorf("lost fd: %v", got.Packages)
	}
	if _, ok := got.Packages["just@1.56.0"]; !ok {
		t.Errorf("missing just: %v", got.Packages)
	}
}

func TestLockWritesV2WithoutFetch(t *testing.T) {
	fx := newLockFetchFix(t)
	var calls atomic.Int32
	installToStore = func(ctx context.Context, st *store.Store, name, version string, a index.Artifact) (string, error) {
		calls.Add(1)
		return stageTestFetch(ctx, st, name, version, a)
	}
	t.Cleanup(func() { installToStore = nil })
	if err := runLockLive(context.Background(), fx.c, fx.src); err != nil {
		t.Fatalf("lock: %v", err)
	}
	if calls.Load() != 0 {
		t.Errorf("lock fetched %d times", calls.Load())
	}
	if _, err := lockfile.ReadV2(fx.lockPath()); err != nil {
		t.Fatalf("ReadV2: %v", err)
	}
	if cur := currentGen(t, fx.c.GaleDir); cur != fx.prev {
		t.Errorf("current gen = %d, want unchanged %d", cur, fx.prev)
	}
}

func TestSyncDoesNotWriteLock(t *testing.T) {
	clearAdoptCI(t)
	fx := newLockFetchFix(t)
	installToStore = stageTestFetch
	t.Cleanup(func() { installToStore = nil })
	if err := os.WriteFile(fx.c.GalePath, []byte("[packages]\njust = \"1.56.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runInstallFetch(context.Background(), fx.c, "just", "1.56.0", fx.src); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(fx.lockPath())
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(fx.lockPath())
	if err != nil {
		t.Fatal(err)
	}
	mtime := info.ModTime()
	out := newOutput()
	if err := runSyncFetch(context.Background(), fx.c, syncRun{}, out); err != nil {
		t.Fatalf("sync: %v", err)
	}
	after, err := os.ReadFile(fx.lockPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("sync rewrote the lock")
	}
	info2, err := os.Stat(fx.lockPath())
	if err != nil {
		t.Fatal(err)
	}
	if !info2.ModTime().Equal(mtime) {
		t.Error("sync changed lock mtime")
	}
}

func TestSyncRefusesAbsentLock(t *testing.T) {
	fx := newLockFetchFix(t)
	err := runSyncFetch(context.Background(), fx.c, syncRun{}, newOutput())
	if !errors.Is(err, errSwitchNoLock) {
		t.Fatalf("err = %v, want errSwitchNoLock", err)
	}
}

func TestSyncRefusesStaleLock(t *testing.T) {
	clearAdoptCI(t)
	fx := newLockFetchFix(t)
	installToStore = stageTestFetch
	t.Cleanup(func() { installToStore = nil })
	if err := os.WriteFile(fx.c.GalePath, []byte("[packages]\njust = \"1.56.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runInstallFetch(context.Background(), fx.c, "just", "1.56.0", fx.src); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fx.c.GalePath, []byte("[packages]\njust = \"1.56.0\"\nfd = \"10.2.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runSyncFetch(context.Background(), fx.c, syncRun{}, newOutput())
	if !errors.Is(err, lockfile.ErrStaleLock) {
		t.Fatalf("err = %v, want ErrStaleLock", err)
	}
}

func TestUpdateReplacesNamedKeepsRest(t *testing.T) {
	clearAdoptCI(t)
	fx := newLockFetchFix(t)
	installToStore = stageTestFetch
	t.Cleanup(func() { installToStore = nil })
	if err := os.WriteFile(fx.c.GalePath, []byte("[packages]\njust = \"1.56.0\"\nfd = \"10.2.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runInstallFetch(context.Background(), fx.c, "just", "1.56.0", fx.src); err != nil {
		t.Fatal(err)
	}
	if err := runInstallFetch(context.Background(), fx.c, "fd", "10.2.0", fx.src); err != nil {
		t.Fatal(err)
	}
	fx.src.Commit = lockFetchPinB
	if err := runUpdateFetch(context.Background(), fx.c, []string{"just"}, fx.src); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := lockfile.ReadV2(fx.lockPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Packages["just@9.9.9"]; !ok {
		t.Errorf("just not updated: %v", got.Packages)
	}
	if _, ok := got.Packages["fd@10.2.0"]; !ok {
		t.Errorf("fd rewritten: %v", got.Packages)
	}
}

func TestRemoveDropsRoot(t *testing.T) {
	clearAdoptCI(t)
	fx := newLockFetchFix(t)
	installToStore = stageTestFetch
	t.Cleanup(func() { installToStore = nil })
	if err := os.WriteFile(fx.c.GalePath, []byte("[packages]\njust = \"1.56.0\"\nfd = \"10.2.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runInstallFetch(context.Background(), fx.c, "just", "1.56.0", fx.src); err != nil {
		t.Fatal(err)
	}
	if err := runInstallFetch(context.Background(), fx.c, "fd", "10.2.0", fx.src); err != nil {
		t.Fatal(err)
	}
	if err := runRemoveFetch(context.Background(), fx.c, "just", newOutput()); err != nil {
		t.Fatalf("remove: %v", err)
	}
	got, err := lockfile.ReadV2(fx.lockPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Packages["just@1.56.0"]; ok {
		t.Error("just still locked")
	}
	if _, ok := got.Packages["fd@10.2.0"]; !ok {
		t.Error("fd dropped")
	}
	cfg, err := os.ReadFile(fx.c.GalePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfg), "just") {
		t.Errorf("gale.toml still has just: %s", cfg)
	}
}

func TestLivePathRefusesV1(t *testing.T) {
	fx := newLockFetchFix(t)
	if err := os.WriteFile(fx.lockPath(), []byte("[packages.just]\nversion = \"1.56.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runSyncFetch(context.Background(), fx.c, syncRun{}, newOutput())
	if !errors.Is(err, errSwitchV1) {
		t.Fatalf("err = %v, want errSwitchV1", err)
	}
}

func TestLivePathRefusesMixedLock(t *testing.T) {
	fx := newLockFetchFix(t)
	mixed := &lockfile.V2{
		Version: lockfile.SchemaV2,
		Targets: lockfile.Targets{Default: &lockfile.Target{
			Roots: []string{"just@1.56.0"},
		}},
		Packages: map[string]lockfile.V2Package{
			"just@1.56.0": {
				Artifacts: map[string]lockfile.V2Artifact{
					"darwin/arm64": {
						SHA256: lockFetchSHA,
						Method: "source",
					},
				},
			},
		},
	}
	if err := lockfile.WriteV2(fx.lockPath(), mixed); err != nil {
		t.Fatal(err)
	}
	err := runSyncFetch(context.Background(), fx.c, syncRun{}, newOutput())
	if !errors.Is(err, errSwitchMixed) {
		t.Fatalf("err = %v, want errSwitchMixed", err)
	}
}
