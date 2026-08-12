package main

// Tests for the sync completion stamp (gh#186).
//
// The direnv hook used to gate `gale sync` on
// `[ gale.toml -nt .gale/current ]`. A partial sync deliberately
// rebuilds the generation (issue #20), which swaps a fresh `current`
// symlink and so makes the manifest older than it. From the second
// activation on, the gate is false forever and the incomplete
// environment is never retried — silently.
//
// The stamp moves that decision into Go, where it can distinguish
// "the last sync completed" from "the last sync gave up", and can
// rate-limit the retry so a permanently broken package cannot make
// every `cd` rebuild.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kelp/gale/internal/generation"
)

// stampTime is the fixed instant every stamp in this file is recorded
// at. Wall-clock never enters these tests: syncNeeded takes `now`.
var stampTime = time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)

// syncStateFixture builds the exact state that defeats the old mtime
// gate: a generation swapped into place just now, and a gale.toml
// older than it. Returns the project's .gale dir, its manifest path,
// and the fingerprint of its inputs.
func syncStateFixture(t *testing.T) (galeDir, galePath, fp string) {
	t.Helper()
	tmp := t.TempDir()
	storeRoot := filepath.Join(tmp, "store")
	galeDir = filepath.Join(tmp, ".gale")
	galePath = filepath.Join(tmp, "gale.toml")

	if err := os.WriteFile(galePath,
		[]byte("[packages]\n  jq = \"1.7\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedStore(t, storeRoot, "jq", "1.7-1")
	if err := generation.Build(
		map[string]string{"jq": "1.7-1"}, galeDir, storeRoot,
	); err != nil {
		t.Fatal(err)
	}

	// The manifest predates the generation swap, so
	// `[ "$manifest" -nt "$gale_dir/current" ]` is false.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(galePath, old, old); err != nil {
		t.Fatal(err)
	}

	fp, err := syncFingerprint(galePath, "testhost", "linux/amd64")
	if err != nil {
		t.Fatalf("syncFingerprint: %v", err)
	}
	return galeDir, galePath, fp
}

// stamp records one outcome at stampTime. Shared so each test asserts
// one thing about the same fixture.
func stamp(t *testing.T, galeDir, fp string, complete bool, failed ...string) {
	t.Helper()
	if err := recordSyncOutcome(syncOutcomeRecord{
		galeDir:     galeDir,
		fingerprint: fp,
		complete:    complete,
		failed:      failed,
		now:         stampTime,
	}); err != nil {
		t.Fatalf("recordSyncOutcome: %v", err)
	}
}

// A sync that could not install every package must say so on disk.
// Without this the only record of the failure is a warning line that
// scrolled past, and the next activation has nothing to consult.
func TestSyncStateRecordsIncompleteWhenPackagesFailed(t *testing.T) {
	galeDir, _, fp := syncStateFixture(t)

	stamp(t, galeDir, fp, false, "python@3.13.1")

	data, err := os.ReadFile(filepath.Join(galeDir, syncStateFile))
	if err != nil {
		t.Fatalf("reading sync state: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		`status = "incomplete"`, "python@3.13.1", fp,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("sync state missing %q, got:\n%s", want, got)
		}
	}
}

// This is gh#186, expressed at the layer that can hold it. The
// generation is fresh and the manifest is old, so the shell gate says
// "nothing to do"; the stamp says the last sync gave up.
func TestSyncNeededAfterPartialSync(t *testing.T) {
	galeDir, _, fp := syncStateFixture(t)

	stamp(t, galeDir, fp, false, "python@3.13.1")

	// Past the backoff, so this asks the retry question and not the
	// rate-limit one (TestSyncNotRetriedWithinBackoff covers that).
	check := syncNeeded(galeDir, fp, stampTime.Add(syncRetryInterval+time.Minute))
	if !check.Needed {
		t.Errorf("got Needed=false after a partial sync, want true: "+
			"the failed package is never retried (%+v)", check)
	}
}

// AC 3, and the property that must not regress: a project that synced
// cleanly costs one file read and prints nothing.
func TestSyncNotNeededAfterCompleteSync(t *testing.T) {
	galeDir, _, fp := syncStateFixture(t)

	stamp(t, galeDir, fp, true)

	check := syncNeeded(galeDir, fp, stampTime.Add(24*time.Hour))
	if check.Needed {
		t.Errorf("got Needed=true after a complete sync, want false: "+
			"every cd would re-sync (%+v)", check)
	}
	if check.Notice != "" {
		t.Errorf("a clean activation printed %q, want silence", check.Notice)
	}
}

// The loop check. A permanently broken package must not make every
// `cd` attempt a build; within the interval the activation reads one
// file, prints one line naming what failed, and does no work.
func TestSyncNotRetriedWithinBackoff(t *testing.T) {
	galeDir, _, fp := syncStateFixture(t)

	stamp(t, galeDir, fp, false, "python@3.13.1")

	check := syncNeeded(galeDir, fp, stampTime.Add(time.Minute))
	if check.Needed {
		t.Error("got Needed=true one minute after a failed sync: a " +
			"broken package would rebuild on every cd")
	}
	if !strings.Contains(check.Notice, "python@3.13.1") {
		t.Errorf("Notice = %q, want it to name the failed package",
			check.Notice)
	}
	if !strings.Contains(check.Notice, "gale sync") {
		t.Errorf("Notice = %q, want it to name the retry command",
			check.Notice)
	}
}

// Editing gale.toml must reach the packages, backoff or not. The
// fingerprint is over content, so this holds without consulting any
// mtime.
func TestSyncNeededWhenManifestChanged(t *testing.T) {
	galeDir, galePath, fp := syncStateFixture(t)

	stamp(t, galeDir, fp, false, "python@3.13.1")

	if err := os.WriteFile(galePath,
		[]byte("[packages]\n  jq = \"1.8\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	edited, err := syncFingerprint(galePath, "testhost", "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}

	check := syncNeeded(galeDir, edited, stampTime.Add(time.Minute))
	if !check.Needed {
		t.Error("an edited manifest was withheld by the retry backoff: " +
			"the backoff must rate-limit retries of the SAME inputs only")
	}
}

// Missing and unreadable are different cases and must not collapse
// into one. Both sync — fail toward work — but the reason a user is
// shown differs, and so does what it tells them to look at.
func TestSyncNeededDistinguishesMissingFromUnreadableState(t *testing.T) {
	galeDir, _, fp := syncStateFixture(t)

	missing := syncNeeded(galeDir, fp, stampTime)
	if !missing.Needed {
		t.Error("no stamp at all was read as a completed sync")
	}

	if err := os.WriteFile(filepath.Join(galeDir, syncStateFile),
		[]byte("schema = \"not an integer\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	unreadable := syncNeeded(galeDir, fp, stampTime)
	if !unreadable.Needed {
		t.Error("an unparsable stamp was read as a completed sync")
	}

	if missing.Reason == unreadable.Reason {
		t.Errorf("missing and unparsable report the same reason %q; "+
			"on-disk state carries meaning and the two are not the "+
			"same condition", missing.Reason)
	}
}

// Rule 0. A complete stamp cannot vouch for an environment that has no
// active generation — `gale gc` or a hand-deleted .gale leaves exactly
// that, and the packages must come back.
func TestSyncNeededWhenNoGenerationExists(t *testing.T) {
	galeDir, _, fp := syncStateFixture(t)

	stamp(t, galeDir, fp, true)
	if err := os.Remove(filepath.Join(galeDir, "current")); err != nil {
		t.Fatal(err)
	}

	if check := syncNeeded(galeDir, fp, stampTime); !check.Needed {
		t.Errorf("no current generation, but sync was skipped: the "+
			"project has nothing on PATH (%+v)", check)
	}
}

// An absent lock and an empty one are different states, and a
// fingerprint that conflates them cannot tell "unlocked" from "locked
// to nothing" — the .gale-deps.toml lesson.
func TestSyncFingerprintDistinguishesAbsentAndEmptyLock(t *testing.T) {
	tmp := t.TempDir()
	galePath := filepath.Join(tmp, "gale.toml")
	if err := os.WriteFile(galePath,
		[]byte("[packages]\n  jq = \"1.7\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	absent, err := syncFingerprint(galePath, "testhost", "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "gale.lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	empty, err := syncFingerprint(galePath, "testhost", "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}

	if absent == empty {
		t.Error("an absent lock and an empty lock hash the same: " +
			"deleting the lock would not re-sync")
	}
}

// The stamp names packages, so the failed set has to survive the
// outcome slice. A sync that fails to resolve and one that fails to
// install are both incomplete.
func TestFailedPackageNamesCoversResolveAndInstall(t *testing.T) {
	got := failedPackageNames([]syncOutcome{
		{name: "jq", version: "1.7", upToDate: true},
		{name: "python", version: "3.13.1", installErr: os.ErrPermission},
		{name: "ripgrep", version: "14.1.0", resolveErr: os.ErrNotExist},
	})
	want := []string{"python@3.13.1", "ripgrep@14.1.0"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v, want %v", got, want)
			break
		}
	}
}

// --dry-run must leave no trace. Recording from a run that installed
// nothing would stamp the project complete without doing the work.
func TestSyncStateNotWrittenOnDryRun(t *testing.T) {
	galeDir, _, fp := syncStateFixture(t)

	if err := recordSyncOutcome(syncOutcomeRecord{
		galeDir:     galeDir,
		fingerprint: fp,
		complete:    true,
		dryRun:      true,
		now:         stampTime,
	}); err != nil {
		t.Fatalf("recordSyncOutcome: %v", err)
	}

	if _, err := os.Stat(filepath.Join(galeDir, syncStateFile)); !os.IsNotExist(err) {
		t.Errorf("--dry-run wrote a sync stamp (err=%v)", err)
	}
}
