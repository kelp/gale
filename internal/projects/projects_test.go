package projects

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kelp/gale/internal/filelock"
)

// writeGaleToml drops a minimal gale.toml in dir so the
// path counts as a live project for Prune.
func writeGaleToml(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(
		filepath.Join(dir, "gale.toml"),
		[]byte("[packages]\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterAndList(t *testing.T) {
	galeHome := filepath.Join(t.TempDir(), ".gale")
	proj1 := t.TempDir()
	proj2 := t.TempDir()

	if err := Register(galeHome, proj1); err != nil {
		t.Fatalf("Register(%s): %v", proj1, err)
	}
	if err := Register(galeHome, proj2); err != nil {
		t.Fatalf("Register(%s): %v", proj2, err)
	}

	got, err := List(galeHome)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 projects, got %d: %v", len(got), got)
	}
	want1, _ := filepath.EvalSymlinks(proj1)
	want2, _ := filepath.EvalSymlinks(proj2)
	if got[0] != want1 || got[1] != want2 {
		t.Errorf("want [%s %s], got %v", want1, want2, got)
	}
}

func TestRegisterDedupes(t *testing.T) {
	galeHome := filepath.Join(t.TempDir(), ".gale")
	proj := t.TempDir()

	for i := 0; i < 3; i++ {
		if err := Register(galeHome, proj); err != nil {
			t.Fatalf("Register #%d: %v", i, err)
		}
	}

	got, err := List(galeHome)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("want 1 entry after repeat registers, got %d: %v",
			len(got), got)
	}
}

// TestRegisterCanonicalizesSymlinks verifies that registering
// the same project via a symlinked spelling and via its real
// path produces one entry. macOS /var vs /private/var is the
// motivating case.
func TestRegisterCanonicalizesSymlinks(t *testing.T) {
	galeHome := filepath.Join(t.TempDir(), ".gale")
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	if err := Register(galeHome, real); err != nil {
		t.Fatal(err)
	}
	if err := Register(galeHome, link); err != nil {
		t.Fatal(err)
	}

	got, err := List(galeHome)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("symlinked and real spelling must dedupe to "+
			"1 entry, got %d: %v", len(got), got)
	}
}

func TestListMissingFileReturnsEmpty(t *testing.T) {
	got, err := List(filepath.Join(t.TempDir(), ".gale"))
	if err != nil {
		t.Fatalf("List on missing file must not error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want empty list, got %v", got)
	}
}

func TestRegisterCreatesGaleHome(t *testing.T) {
	galeHome := filepath.Join(t.TempDir(), "deep", ".gale")
	proj := t.TempDir()
	if err := Register(galeHome, proj); err != nil {
		t.Fatalf("Register must create gale home: %v", err)
	}
	got, err := List(galeHome)
	if err != nil || len(got) != 1 {
		t.Fatalf("want 1 entry, got %v (err %v)", got, err)
	}
}

func TestPruneRemovesVanishedProjects(t *testing.T) {
	galeHome := filepath.Join(t.TempDir(), ".gale")
	live := t.TempDir()
	writeGaleToml(t, live)
	ghost := t.TempDir() // no gale.toml — vanished project

	if err := Register(galeHome, live); err != nil {
		t.Fatal(err)
	}
	if err := Register(galeHome, ghost); err != nil {
		t.Fatal(err)
	}

	if err := Prune(galeHome); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	got, err := List(galeHome)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	wantLive, _ := filepath.EvalSymlinks(live)
	if len(got) != 1 || got[0] != wantLive {
		t.Errorf("want [%s] after prune, got %v", wantLive, got)
	}
}

// TestPruneKeepsToolVersionsProjects verifies that a project
// managed via .tool-versions (no gale.toml) is still treated
// as live — gale's config loading falls back to
// .tool-versions, so its generation deserves retention too.
func TestPruneKeepsToolVersionsProjects(t *testing.T) {
	galeHome := filepath.Join(t.TempDir(), ".gale")
	proj := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(proj, ".tool-versions"),
		[]byte("jq 1.7\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	if err := Register(galeHome, proj); err != nil {
		t.Fatal(err)
	}
	if err := Prune(galeHome); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	got, err := List(galeHome)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Errorf(".tool-versions project must survive prune, "+
			"got %v", got)
	}
}

// denyStat makes os.Stat on anything under proj fail with
// EACCES (not ErrNotExist) by stripping permissions from
// proj's parent. Restores perms on cleanup so TempDir removal
// works. Skips the test when running as root, which bypasses
// permission checks.
func denyStat(t *testing.T, parent string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	if err := os.Chmod(parent, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(parent, 0o755); err != nil {
			t.Error(err)
		}
	})
}

// TestLivesKeepsUnstatableProjects: a stat error that does not
// prove absence (EACCES here; also EIO, flaky network mounts)
// must count as live. Treating it as dead would let a single
// transient error during gc drop the entry and sweep a
// still-active project's store versions. gc's principle is
// conservative retention: when in doubt, keep.
func TestLivesKeepsUnstatableProjects(t *testing.T) {
	parent := t.TempDir()
	proj := filepath.Join(parent, "proj")
	if err := os.Mkdir(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	writeGaleToml(t, proj)
	denyStat(t, parent)

	if !Lives(proj) {
		t.Error("Lives must treat a permission-denied stat as " +
			"live (not provably vanished), got dead")
	}
}

// TestPruneKeepsUnstatableProjects: Prune must keep a registry
// entry whose liveness stat fails for reasons other than
// absence — the project still exists, it is just unreadable
// right now.
func TestPruneKeepsUnstatableProjects(t *testing.T) {
	galeHome := filepath.Join(t.TempDir(), ".gale")
	parent := t.TempDir()
	proj := filepath.Join(parent, "proj")
	if err := os.Mkdir(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	writeGaleToml(t, proj)

	if err := Register(galeHome, proj); err != nil {
		t.Fatal(err)
	}
	denyStat(t, parent)

	if err := Prune(galeHome); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	got, err := List(galeHome)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("unstatable project must survive prune, got %v",
			got)
	}
}

// TestRegisterPruneConcurrent stresses Register racing Prune.
// Register is read-check-append and Prune is
// read-filter-rewrite (atomic rename); without mutual
// exclusion a Register can append to the inode Prune just
// replaced (or be overwritten by the rewrite) and silently
// vanish — and a dropped registry entry means a later gc can
// sweep that project's store versions, the exact gh#115 bug.
// Ghost registrations force Prune to actually rewrite on every
// iteration. Afterwards every live registered project must
// still be listed.
func TestRegisterPruneConcurrent(t *testing.T) {
	const (
		registrars      = 4
		perRegistrar    = 25
		pruneIterations = 200
	)

	galeHome := filepath.Join(t.TempDir(), ".gale")
	base := t.TempDir()

	// Pre-create the live project dirs so the goroutines only
	// race on the registry file, not on dir creation.
	live := make([]string, 0, registrars*perRegistrar)
	for i := 0; i < registrars*perRegistrar; i++ {
		dir := filepath.Join(base, fmt.Sprintf("proj-%d", i))
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeGaleToml(t, dir)
		live = append(live, dir)
	}

	var wg sync.WaitGroup
	for r := 0; r < registrars; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			for i := 0; i < perRegistrar; i++ {
				p := live[r*perRegistrar+i]
				if err := Register(galeHome, p); err != nil {
					t.Errorf("Register(%s): %v", p, err)
				}
			}
		}(r)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < pruneIterations; i++ {
			// A ghost entry (no gale.toml) makes Prune rewrite
			// the file instead of returning early.
			ghost := filepath.Join(
				base, fmt.Sprintf("ghost-%d", i),
			)
			if err := Register(galeHome, ghost); err != nil {
				t.Errorf("Register(ghost): %v", err)
			}
			if err := Prune(galeHome); err != nil {
				t.Errorf("Prune: %v", err)
			}
		}
	}()

	wg.Wait()
	<-done

	got, err := List(galeHome)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	listed := make(map[string]bool, len(got))
	for _, p := range got {
		listed[p] = true
	}
	var missing int
	for _, p := range live {
		want, _ := filepath.EvalSymlinks(p)
		if !listed[want] {
			missing++
			if missing <= 5 {
				t.Errorf("live project lost from registry: %s", want)
			}
		}
	}
	if missing > 5 {
		t.Errorf("... and %d more lost entries", missing-5)
	}
}

// TestRegisterAlreadyRegisteredSkipsLock: Register of an
// already-listed project must return without touching
// projects.lock. Register runs on command hot paths (gale env
// via direnv, sync, newCmdContext) and must not block behind a
// long-held lock — e.g. a Prune stat'ing a project on a dead
// network mount.
func TestRegisterAlreadyRegisteredSkipsLock(t *testing.T) {
	galeHome := filepath.Join(t.TempDir(), ".gale")
	proj := t.TempDir()
	writeGaleToml(t, proj)
	if err := Register(galeHome, proj); err != nil {
		t.Fatal(err)
	}

	unlock, err := filelock.Acquire(lockPath(galeHome))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	done := make(chan error, 1)
	go func() { done <- Register(galeHome, proj) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Register(already registered): %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Register of an already-registered project " +
			"blocked behind projects.lock")
	}
}

// TestRegisterAlreadyRegisteredReadOnlyGaleHome: the
// already-registered case must succeed silently on a read-only
// gale home (best-effort contract: a read-only gale home must
// never block install or sync, and gale env runs Register on
// every direnv activation).
func TestRegisterAlreadyRegisteredReadOnlyGaleHome(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	galeHome := filepath.Join(t.TempDir(), ".gale")
	proj := t.TempDir()
	writeGaleToml(t, proj)
	if err := Register(galeHome, proj); err != nil {
		t.Fatal(err)
	}
	// Hardened gale home: lock file gone, dir unwritable. Lock
	// creation would fail EACCES; the dedup check must come
	// first so no lock is needed.
	if err := os.Remove(lockPath(galeHome)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(galeHome, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(galeHome, 0o755) })

	if err := Register(galeHome, proj); err != nil {
		t.Errorf("Register(already registered) on read-only "+
			"gale home must succeed: %v", err)
	}
}

// TestPruneKeepsEntriesAppendedAfterScan: Prune must compute
// liveness OUTSIDE the lock (a stat on a dead network mount can
// hang for minutes, and holding projects.lock during it would
// wedge every concurrent Register), then under the lock re-read
// and drop only entries that were dead at scan time. Entries
// appended between scan and rewrite survive, live or not — a
// dead one is removed by the next prune. The Registers inside
// the hook also prove the lock is free at scan time: Register's
// append path takes the same lock and would deadlock otherwise.
func TestPruneKeepsEntriesAppendedAfterScan(t *testing.T) {
	galeHome := filepath.Join(t.TempDir(), ".gale")
	ghostA := t.TempDir() // dead at scan time — forces a rewrite
	lateLive := t.TempDir()
	writeGaleToml(t, lateLive)
	lateGhost := t.TempDir() // dead but appended after the scan

	if err := Register(galeHome, ghostA); err != nil {
		t.Fatal(err)
	}

	pruneAfterScan = func() {
		if err := Register(galeHome, lateLive); err != nil {
			t.Errorf("Register(lateLive) during prune: %v", err)
		}
		if err := Register(galeHome, lateGhost); err != nil {
			t.Errorf("Register(lateGhost) during prune: %v", err)
		}
	}
	defer func() { pruneAfterScan = nil }()

	if err := Prune(galeHome); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	got, err := List(galeHome)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	listed := make(map[string]bool, len(got))
	for _, p := range got {
		listed[p] = true
	}
	wantGone, _ := filepath.EvalSymlinks(ghostA)
	wantLive, _ := filepath.EvalSymlinks(lateLive)
	wantGhost, _ := filepath.EvalSymlinks(lateGhost)
	if listed[wantGone] {
		t.Errorf("entry dead at scan time must be pruned: %v", got)
	}
	if !listed[wantLive] {
		t.Errorf("live entry appended after scan must survive: %v",
			got)
	}
	if !listed[wantGhost] {
		t.Errorf("entry appended after scan must survive even if "+
			"dead (next prune's job): %v", got)
	}
}

func TestPruneMissingFileIsNoop(t *testing.T) {
	if err := Prune(filepath.Join(t.TempDir(), ".gale")); err != nil {
		t.Fatalf("Prune on missing registry must not error: %v", err)
	}
}
