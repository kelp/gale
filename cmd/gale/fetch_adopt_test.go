package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kelp/gale/internal/index"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/store"
)

func clearAdoptCI(t *testing.T) {
	t.Helper()
	t.Setenv("CI", "")
	t.Setenv("GITHUB_ACTIONS", "")
}

func adoptOut() (io.Writer, *bytes.Buffer) {
	var buf bytes.Buffer
	return &buf, &buf
}

func TestParseConfirm(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want bool
	}{
		{"y\n", true},
		{"yes\n", true},
		{"Y\n", true},
		{"n\n", false},
		{"\n", false},
		{"", false},
	}
	for _, tc := range cases {
		got, err := parseConfirm(strings.NewReader(tc.in))
		if err != nil {
			t.Fatalf("parseConfirm(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("parseConfirm(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestFetchAdoptCommandExists(t *testing.T) {
	if rootCmd.Commands() == nil {
		t.Fatal("rootCmd has no commands")
	}
	cmd, _, err := rootCmd.Find([]string{"fetch-adopt"})
	if err != nil {
		t.Fatalf("find fetch-adopt: %v", err)
	}
	if cmd.Name() != "fetch-adopt" {
		t.Errorf("command = %q, want fetch-adopt", cmd.Name())
	}
}

func TestFetchAdoptRefusesInCI(t *testing.T) {
	fx := newLockFetchFix(t)
	t.Setenv("CI", "true")
	t.Setenv("GITHUB_ACTIONS", "")
	out, _ := adoptOut()
	err := runFetchAdopt(context.Background(), fx.c, adoptReq{
		Source: fx.src,
		Yes:    true,
		Out:    out,
	})
	if !errors.Is(err, errAdoptCI) {
		t.Fatalf("err = %v, want errAdoptCI", err)
	}
	if _, err := lockfile.ReadV2(fx.lockPath()); err == nil {
		t.Error("CI refuse wrote a v2 lock")
	}
}

func TestFetchAdoptYesDoesNotOverrideCI(t *testing.T) {
	fx := newLockFetchFix(t)
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("CI", "")
	err := runFetchAdopt(context.Background(), fx.c, adoptReq{
		Source: fx.src,
		Yes:    true,
		Out:    io.Discard,
	})
	if !errors.Is(err, errAdoptCI) {
		t.Fatalf("err = %v, want errAdoptCI", err)
	}
}

func TestFetchAdoptRequiresYesWithoutTTY(t *testing.T) {
	clearAdoptCI(t)
	fx := newLockFetchFix(t)
	err := runFetchAdopt(context.Background(), fx.c, adoptReq{
		Source: fx.src,
		TTY:    false,
		Out:    io.Discard,
	})
	if !errors.Is(err, errAdoptNeedYes) {
		t.Fatalf("err = %v, want errAdoptNeedYes", err)
	}
}

func TestFetchAdoptPlansAllRootsBeforeFetch(t *testing.T) {
	clearAdoptCI(t)
	fx := newLockFetchFix(t)
	if err := os.WriteFile(fx.c.GalePath, []byte("[packages]\njust = \"1.56.0\"\nmissing = \"1.0.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	err := runFetchAdopt(context.Background(), fx.c, adoptReq{
		Source: fx.src,
		Yes:    true,
		Out:    io.Discard,
		ToStore: func(context.Context, *store.Store, string, string, index.Artifact) (string, error) {
			calls.Add(1)
			return "", nil
		},
	})
	if err == nil {
		t.Fatal("want resolve error for missing")
	}
	if calls.Load() != 0 {
		t.Errorf("ToStore calls = %d, want 0", calls.Load())
	}
	if _, err := lockfile.ReadV2(fx.lockPath()); err == nil {
		t.Error("failed plan wrote a v2 lock")
	}
}

func TestFetchAdoptPrintsLockDiff(t *testing.T) {
	clearAdoptCI(t)
	fx := newLockFetchFix(t)
	out, buf := adoptOut()
	err := runFetchAdopt(context.Background(), fx.c, adoptReq{
		Source: fx.src,
		DryRun: true,
		Out:    out,
	})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "index_commit "+lockFetchPinA) {
		t.Errorf("diff missing index_commit:\n%s", got)
	}
	if !strings.Contains(got, "+ just@1.56.0") {
		t.Errorf("diff missing just:\n%s", got)
	}
	if !strings.Contains(got, "+ fd@10.2.0") {
		t.Errorf("diff missing fd:\n%s", got)
	}
	if _, err := lockfile.ReadV2(fx.lockPath()); err == nil {
		t.Error("dry-run wrote a v2 lock")
	}
}

func TestFetchAdoptDryRunWinsOverYes(t *testing.T) {
	clearAdoptCI(t)
	fx := newLockFetchFix(t)
	var calls atomic.Int32
	err := runFetchAdopt(context.Background(), fx.c, adoptReq{
		Source: fx.src,
		Yes:    true,
		DryRun: true,
		Out:    io.Discard,
		ToStore: func(context.Context, *store.Store, string, string, index.Artifact) (string, error) {
			calls.Add(1)
			return "", nil
		},
	})
	if err != nil {
		t.Fatalf("dry-run --yes: %v", err)
	}
	if calls.Load() != 0 {
		t.Errorf("ToStore calls = %d, want 0", calls.Load())
	}
}

func TestFetchAdoptYesPublishesViaFinalize(t *testing.T) {
	clearAdoptCI(t)
	fx := newLockFetchFix(t)
	seedStore(t, fx.c.StoreRoot, "just", "1.56.0")
	seedStore(t, fx.c.StoreRoot, "fd", "10.2.0")
	var calls atomic.Int32
	err := runFetchAdopt(context.Background(), fx.c, adoptReq{
		Source: fx.src,
		Yes:    true,
		Out:    io.Discard,
		ToStore: func(_ context.Context, st *store.Store, name, version string, a index.Artifact) (string, error) {
			calls.Add(1)
			dest, err := st.FetchPath(name, version, a.SHA256)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return "", err
			}
			return dest, nil
		},
	})
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("ToStore calls = %d, want 2", calls.Load())
	}
	got, err := lockfile.ReadV2(fx.lockPath())
	if err != nil {
		t.Fatalf("ReadV2: %v", err)
	}
	if _, err := lockfile.Load(fx.lockPath()); !errors.Is(err, lockfile.ErrUnknownVersion) {
		t.Errorf("Load = %v, want ErrUnknownVersion", err)
	}
	if currentGen(t, fx.c.GaleDir) <= fx.prev {
		t.Errorf("current = %d, want > %d", currentGen(t, fx.c.GaleDir), fx.prev)
	}
	just := got.Packages["just@1.56.0"]
	if _, ok := just.Artifacts["darwin/arm64"]; !ok {
		t.Error("just missing darwin/arm64 row")
	}
	if _, ok := just.Artifacts["linux/amd64"]; !ok {
		t.Error("just missing linux/amd64 row")
	}
	if calls.Load() != 2 {
		t.Error("fetched more than the current-platform arts")
	}
}

func TestFetchAdoptSecondRootStageFailureLeavesOldLock(t *testing.T) {
	clearAdoptCI(t)
	fx := newLockFetchFix(t)
	var calls atomic.Int32
	err := runFetchAdopt(context.Background(), fx.c, adoptReq{
		Source: fx.src,
		Yes:    true,
		Out:    io.Discard,
		ToStore: func(_ context.Context, _ *store.Store, name, _ string, _ index.Artifact) (string, error) {
			n := calls.Add(1)
			if n == 2 {
				return "", errors.New("second root failed")
			}
			return filepath.Join(t.TempDir(), name), nil
		},
	})
	if err == nil {
		t.Fatal("want second-root ToStore error")
	}
	if _, err := lockfile.ReadV2(fx.lockPath()); err == nil {
		t.Error("failed publish wrote a v2 lock")
	}
	if currentGen(t, fx.c.GaleDir) != fx.prev {
		t.Errorf("current = %d, want %d", currentGen(t, fx.c.GaleDir), fx.prev)
	}
}

func TestFetchAdoptRefusesHostOverlayManifest(t *testing.T) {
	clearAdoptCI(t)
	fx := newLockFetchFix(t)
	if err := os.WriteFile(fx.c.GalePath, []byte("[packages]\njust = \"1.56.0\"\n\n[hosts.laptop.packages]\nfd = \"10.2.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runFetchAdopt(context.Background(), fx.c, adoptReq{
		Source: fx.src,
		Yes:    true,
		Out:    io.Discard,
	})
	if !errors.Is(err, errAdoptHosts) {
		t.Fatalf("err = %v, want errAdoptHosts", err)
	}
}

func TestFetchAdoptRefusesV1HostTargets(t *testing.T) {
	clearAdoptCI(t)
	fx := newLockFetchFix(t)
	if err := lockfile.WriteV1(fx.lockPath(), &lockfile.V1{
		Version: lockfile.SchemaVersion,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{Roots: []string{"just@1.56.0-1"}},
			Host: map[string]lockfile.Target{
				"laptop": {Roots: []string{"fd@10.2.0-1"}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	err := runFetchAdopt(context.Background(), fx.c, adoptReq{
		Source: fx.src,
		Yes:    true,
		Out:    io.Discard,
	})
	if !errors.Is(err, errAdoptHosts) {
		t.Fatalf("err = %v, want errAdoptHosts", err)
	}
}

func TestFetchAdoptRefusesAlreadyV2(t *testing.T) {
	clearAdoptCI(t)
	fx := newLockFetchFix(t)
	if err := lockfile.WriteV2(fx.lockPath(), &lockfile.V2{
		Version: lockfile.SchemaV2,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{Roots: []string{"just@1.56.0"}},
		},
		Packages: map[string]lockfile.V2Package{},
	}); err != nil {
		t.Fatal(err)
	}
	err := runFetchAdopt(context.Background(), fx.c, adoptReq{
		Source: fx.src,
		Yes:    true,
		Out:    io.Discard,
	})
	if !errors.Is(err, errAdoptAlreadyV2) {
		t.Fatalf("err = %v, want errAdoptAlreadyV2", err)
	}
}

func TestFetchAdoptEmptyDeclarations(t *testing.T) {
	clearAdoptCI(t)
	fx := newLockFetchFix(t)
	if err := os.WriteFile(fx.c.GalePath, []byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runFetchAdopt(context.Background(), fx.c, adoptReq{
		Source: fx.src,
		Yes:    true,
		Out:    io.Discard,
	})
	if !errors.Is(err, errNoDeclarations) {
		t.Fatalf("err = %v, want errNoDeclarations", err)
	}
}

func TestFetchAdoptEmptyPinUsesLatest(t *testing.T) {
	clearAdoptCI(t)
	fx := newLockFetchFix(t)
	if err := os.WriteFile(fx.c.GalePath, []byte("[packages]\njust = \"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, buf := adoptOut()
	err := runFetchAdopt(context.Background(), fx.c, adoptReq{
		Source: fx.src,
		DryRun: true,
		Out:    out,
	})
	if err != nil {
		t.Fatalf("empty pin: %v", err)
	}
	if !strings.Contains(buf.String(), "+ just@1.56.0") {
		t.Errorf("empty pin should resolve to latest:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "just@\n") {
		t.Error("empty pin produced name@")
	}
}

func TestFetchAdoptStripsNumericRevision(t *testing.T) {
	clearAdoptCI(t)
	fx := newLockFetchFix(t)
	if err := os.WriteFile(fx.c.GalePath, []byte("[packages]\njust = \"1.56.0-3\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, buf := adoptOut()
	err := runFetchAdopt(context.Background(), fx.c, adoptReq{
		Source: fx.src,
		DryRun: true,
		Out:    out,
	})
	if err != nil {
		t.Fatalf("revision pin: %v", err)
	}
	if !strings.Contains(buf.String(), "+ just@1.56.0") {
		t.Errorf("want stripped pin:\n%s", buf.String())
	}
}

func TestFetchAdoptConfirmNoAborts(t *testing.T) {
	clearAdoptCI(t)
	fx := newLockFetchFix(t)
	err := runFetchAdopt(context.Background(), fx.c, adoptReq{
		Source: fx.src,
		TTY:    true,
		In:     strings.NewReader("n\n"),
		Out:    io.Discard,
		Err:    io.Discard,
	})
	if !errors.Is(err, errAdoptAborted) {
		t.Fatalf("err = %v, want errAdoptAborted", err)
	}
	if _, err := lockfile.ReadV2(fx.lockPath()); err == nil {
		t.Error("n wrote a v2 lock")
	}
}

func TestFetchAdoptLockMovedAfterDiff(t *testing.T) {
	clearAdoptCI(t)
	fx := newLockFetchFix(t)
	if err := lockfile.WriteV1(fx.lockPath(), &lockfile.V1{
		Version: lockfile.SchemaVersion,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{Roots: []string{"just@1.56.0-1"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { adoptAfterDiff = nil })
	adoptAfterDiff = func() {
		if err := os.WriteFile(fx.lockPath(), []byte("moved\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	err := runFetchAdopt(context.Background(), fx.c, adoptReq{
		Source: fx.src,
		Yes:    true,
		Out:    io.Discard,
		ToStore: func(context.Context, *store.Store, string, string, index.Artifact) (string, error) {
			t.Fatal("ToStore ran after lock moved")
			return "", nil
		},
	})
	if !errors.Is(err, errAdoptLockMoved) {
		t.Fatalf("err = %v, want errAdoptLockMoved", err)
	}
}

func TestFetchAdoptMissingCurrentPlatform(t *testing.T) {
	clearAdoptCI(t)
	fx := newLockFetchFix(t)
	other := "darwin/arm64"
	if currentPlatform() == other {
		other = "linux/amd64"
	}
	fx.h.files["/"+lockFetchPinA+"/index/o/other.toml"] = `[package]
name = "other"
description = "other"
license = "MIT"
homepage = "https://github.com/kelp/other"
repo = "kelp/other"
latest = "1.0.0"

[versions."1.0.0".artifacts."` + other + `"]
url = "https://github.com/kelp/other/releases/download/1.0.0/other.tar.gz"
format = "tar.gz"
sha256 = "` + lockFetchSHA + `"
tree_digest = "` + lockFetchTree + `"
hash_source = "upstream-sha256sums"
strip = 1

[[versions."1.0.0".artifacts."` + other + `".files]]
src = "other"
dest = "bin/other"
mode = 0o755
`
	if err := os.WriteFile(fx.c.GalePath, []byte("[packages]\nother = \"1.0.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runFetchAdopt(context.Background(), fx.c, adoptReq{
		Source: fx.src,
		Yes:    true,
		Out:    io.Discard,
	})
	if !errors.Is(err, errAdoptNoPlatform) {
		t.Fatalf("err = %v, want errAdoptNoPlatform", err)
	}
}

func TestFetchAdoptNotTheInstaller(t *testing.T) {
	if installCmd.Flags().Lookup("index") != nil {
		t.Fatal("install must not grow --index")
	}
	cmd, _, err := rootCmd.Find([]string{"install"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd == fetchAdoptCmd {
		t.Fatal("install and fetch-adopt are the same command")
	}
}
