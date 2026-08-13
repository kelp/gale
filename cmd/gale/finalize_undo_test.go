package main

// #187: FinalizeInstall commits gale.toml before it writes gale.lock,
// and a failed lock write left the manifest committed. Lock
// enforcement then refuses every later command in that scope, so one
// failure surfaced as two errors and the second did not point back at
// the first. A failed finalization must leave the scope exactly as it
// found it.

import (
	"os"
	"strings"
	"testing"
)

// newFinalizeFixture builds a HOME-rooted context over an empty
// manifest, with a store that already holds hello@1.0.0-1 and its
// provenance record — everything FinalizeInstall needs to reach its
// lock write.
func newFinalizeFixture(t *testing.T) *cmdContext {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("GALE_HOST", "testbox")
	ctx := installCtx(t, tmp, "[packages]\n")
	seedProvenanced(t, ctx.StoreRoot, "hello", "1.0.0-1")
	return ctx
}

// breakLockWrite makes gale.lock unwritable by shape rather than by
// permission: the tests run as root in the agent container and on CI
// they must fail for the same reason everywhere, so a mode change
// would prove nothing. A directory in the lockfile's place fails the
// write with EISDIR on every platform.
func breakLockWrite(t *testing.T, ctx *cmdContext) {
	t.Helper()
	lp, err := lockfilePath(ctx.GalePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(lp, 0o755); err != nil {
		t.Fatal(err)
	}
}

// repinManifest returns a test seam body that stands in for another
// command committing a manifest edit inside WriteLock's critical
// section, after this command wrote gale.toml and before the lock
// write reads it.
func repinManifest(t *testing.T, ctx *cmdContext, body string) func() {
	t.Helper()
	return func() {
		if err := os.WriteFile(ctx.GalePath, []byte(body), 0o644); err != nil {
			t.Errorf("standing in for a concurrent writer: %v", err)
		}
	}
}

// TestFinalizeInstallUndoesConfigWhenLockWriteFails is the repro.
// The error was never the defect — FinalizeInstall already returns
// one. What persisted is a gale.toml declaring a package gale.lock
// does not record, which is the stale-lock state the next command in
// the scope refuses on.
//
// The foreign-host case is the same failure through the other branch
// of the config write. `install --host otherbox` is declaration-only
// (gh#72), but "declaration-only" means it commits the manifest and
// the lock and skips only the PATH-presence check, so it diverges
// exactly like a local install and needs the same undo.
func TestFinalizeInstallUndoesConfigWhenLockWriteFails(t *testing.T) {
	for _, tc := range []struct{ name, host string }{
		{name: "shared packages", host: ""},
		{name: "foreign host overlay", host: "otherbox"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := newFinalizeFixture(t)
			ctx.Host = tc.host
			breakLockWrite(t, ctx)

			before, err := os.ReadFile(ctx.GalePath)
			if err != nil {
				t.Fatal(err)
			}

			if err := ctx.FinalizeInstall(
				"hello", "1.0.0", "1.0.0-1",
			); err == nil {
				t.Fatal("a failed lock write must fail the finalization")
			}

			after, err := os.ReadFile(ctx.GalePath)
			if err != nil {
				t.Fatalf("gale.toml missing after a failed "+
					"finalization: %v", err)
			}
			if string(after) != string(before) {
				t.Errorf("a failed finalization left gale.toml "+
					"modified:\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

// TestFinalizeInstallUndoRemovesConfigItCreated is the other half of
// the snapshot: the first install in a scope creates gale.toml, so
// putting it back means removing it. Restoring an empty manifest
// instead would declare no packages, which is a different state from
// no manifest at all — `gale env` resolves scope from the file's
// existence.
func TestFinalizeInstallUndoRemovesConfigItCreated(t *testing.T) {
	ctx := newFinalizeFixture(t)
	if err := os.Remove(ctx.GalePath); err != nil {
		t.Fatal(err)
	}
	breakLockWrite(t, ctx)

	if err := ctx.FinalizeInstall("hello", "1.0.0", "1.0.0-1"); err == nil {
		t.Fatal("a failed lock write must fail the finalization")
	}

	if _, err := os.Lstat(ctx.GalePath); !os.IsNotExist(err) {
		got, _ := os.ReadFile(ctx.GalePath)
		t.Errorf("a failed finalization left a gale.toml behind in a "+
			"scope that had none (lstat %v):\n%s", err, got)
	}
}

// TestFinalizeInstallUndoLeavesAConcurrentManifestAlone: the undo is
// a compare-and-swap, not a blind write.
//
// This is the interleaving a spanning finalize lock would have
// excluded. It does not need one: another command owning gale.toml by
// the time the undo runs means its manifest is the state the machine
// is actually in, so restoring over it would trade one inconsistency
// for a worse one. The undo stands down and says which file it left
// alone.
//
// The concurrent edit is also what fails the lock write here, which
// is the real shape of the race: lockwrite refuses a verified root the
// manifest it reads no longer declares.
func TestFinalizeInstallUndoLeavesAConcurrentManifestAlone(t *testing.T) {
	ctx := newFinalizeFixture(t)
	const concurrent = "[packages]\nother = \"2.0.0\"\n"

	beforeLockRead = repinManifest(t, ctx, concurrent)
	t.Cleanup(func() { beforeLockRead = nil })

	err := ctx.FinalizeInstall("hello", "1.0.0", "1.0.0-1")
	if err == nil {
		t.Fatal("a manifest that no longer declares the root must " +
			"fail the lock write")
	}
	if !strings.Contains(err.Error(), "changed since this command wrote it") {
		t.Errorf("the failure must report that gale.toml was left as "+
			"it is, got: %v", err)
	}
	if !strings.Contains(err.Error(), "does not declare it") {
		t.Errorf("the failure must still report why the lock write "+
			"refused, got: %v", err)
	}

	got, err := os.ReadFile(ctx.GalePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != concurrent {
		t.Errorf("the concurrent writer's manifest was overwritten:\n%s", got)
	}
}

// TestFinalizeInstallKeepsConfigWhenLockWriteSucceeds guards the undo
// from over-correcting: the ordinary install must still land its pin.
func TestFinalizeInstallKeepsConfigWhenLockWriteSucceeds(t *testing.T) {
	ctx := newFinalizeFixture(t)

	if err := ctx.FinalizeInstall("hello", "1.0.0", "1.0.0-1"); err != nil {
		t.Fatalf("FinalizeInstall: %v", err)
	}

	got, err := os.ReadFile(ctx.GalePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `hello = "1.0.0"`) {
		t.Errorf("gale.toml = %q, want the pin this install wrote", got)
	}
	lf := readLock(t, ctx)
	if lf.Targets.Default == nil ||
		len(lf.Targets.Default.Roots) != 1 ||
		lf.Targets.Default.Roots[0] != "hello@1.0.0-1" {
		t.Errorf("lock default roots = %v, want [hello@1.0.0-1]",
			lf.Targets.Default)
	}
}
