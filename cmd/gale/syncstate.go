package main

// The sync completion stamp (gh#186).
//
// Activation used to decide whether to sync by comparing mtimes in
// shell: `[ ! -L .gale/current ] || [ gale.toml -nt .gale/current ]`.
// A partial sync deliberately rebuilds the generation so the packages
// that did install stay usable (issue #20), and the swap gives
// `current` a now-mtime. From the next activation on the comparison is
// false, forever, and the packages that failed are never retried —
// with nothing printed.
//
// The shell cannot fix this, because the fact it needs is not on the
// filesystem: whether the last sync finished or gave up. So sync
// records that itself, and `gale sync --if-needed` reads it back.
//
// Two properties keep the stamp from becoming the next stall:
//
//   - The inputs are hashed, not stat'd. A `git checkout` that
//     rewrites mtimes changes nothing, and tests are deterministic.
//   - A failed sync is retried at most once per syncRetryInterval per
//     (manifest, lock, host, platform). A permanently broken package
//     costs one file read and one warning per activation, not a build.

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/kelp/gale/internal/atomicfile"
	"github.com/kelp/gale/internal/output"
)

const (
	// syncStateFile is the stamp's basename. It is written beside the
	// generation it describes — <project>/.gale for a project, ~/.gale
	// globally — so it is per-scope by construction, never in the
	// store, and never in git.
	syncStateFile = "sync-state.toml"

	// syncStateSchema is the stamp's format version. A stamp written
	// by a newer gale reads as unusable rather than as a completed
	// sync, which costs one extra sync instead of a silent skip.
	syncStateSchema = 1

	syncStatusComplete   = "complete"
	syncStatusIncomplete = "incomplete"
)

// syncRetryInterval bounds automatic retries of a sync that failed on
// unchanged inputs. It is deliberately a constant and not a config
// key: 10 minutes is short enough that a transient outage clears on
// its own and long enough that a broken recipe cannot turn every `cd`
// into a source build. A user-typed `gale sync` ignores it entirely.
const syncRetryInterval = 10 * time.Minute

// syncState is the on-disk stamp.
//
// Inputs is a content hash rather than a set of mtimes, and Failed
// names the packages so the next activation can say what is missing
// without re-resolving anything.
type syncState struct {
	Schema     int       `toml:"schema"`
	Status     string    `toml:"status"`
	RecordedAt time.Time `toml:"recorded_at"`
	Inputs     string    `toml:"inputs"`
	Failed     []string  `toml:"failed,omitempty"`
}

// syncCheck is syncNeeded's answer.
type syncCheck struct {
	// Needed reports whether an automatic sync should do work.
	Needed bool
	// Reason names the rule that decided, for verbose diagnostics.
	Reason string
	// Notice is the one line shown to the user when work is withheld
	// inside the retry interval. Empty in every other case, so a
	// clean activation stays silent.
	Notice string
}

// syncOutcomeRecord is recordSyncOutcome's input. A struct rather than
// positional parameters because complete and dryRun are both bool: a
// transposed pair would compile and stamp a dry run as a completed
// sync.
type syncOutcomeRecord struct {
	galeDir     string
	fingerprint string
	complete    bool
	failed      []string
	dryRun      bool
	now         time.Time
}

// syncStatePath is the stamp's path inside galeDir.
func syncStatePath(galeDir string) string {
	return filepath.Join(galeDir, syncStateFile)
}

// syncFingerprint hashes everything that decides whether a recorded
// sync outcome still describes this project: the manifest's bytes, the
// lock's bytes or its absence, the host, and the platform.
//
// The gale binary's own version is deliberately absent. `gale env
// --check` already refuses an environment a new build will not honor,
// and folding the version in here would force every project to re-sync
// after every gale upgrade.
func syncFingerprint(galePath, host, platform string) (string, error) {
	manifest, err := os.ReadFile(galePath)
	if err != nil {
		return "", fmt.Errorf("reading manifest for fingerprint: %w", err)
	}
	lp, err := lockfilePath(galePath)
	if err != nil {
		return "", fmt.Errorf("locating lockfile for fingerprint: %w", err)
	}
	lock, lockErr := os.ReadFile(lp)
	if lockErr != nil && !errors.Is(lockErr, fs.ErrNotExist) {
		return "", fmt.Errorf("reading lockfile for fingerprint: %w", lockErr)
	}

	h := sha256.New()
	hashField(h, []byte("gale-sync-state-v1"))
	hashField(h, manifest)
	// An absent lock and an empty one are different states, and the
	// difference is exactly "this project is unlocked" versus "this
	// project locks nothing" — the .gale-deps.toml lesson. A tag, not
	// just the bytes, keeps them apart.
	if lockErr != nil {
		hashField(h, []byte("lock-absent"))
	} else {
		hashField(h, []byte("lock-present"))
		hashField(h, lock)
	}
	hashField(h, []byte(host))
	hashField(h, []byte(platform))
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// hashField feeds one length-prefixed field to h. The prefix is what
// stops two different field splits from producing one digest.
func hashField(h hash.Hash, b []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(b)))
	_, _ = h.Write(n[:]) // hash.Hash never errors
	_, _ = h.Write(b)
}

// recordSyncOutcome writes the stamp for one sync run.
//
// Called from exactly one place, a defer in runSync, so the recorded
// verdict is the command's own exit status rather than a separately
// maintained belief about it.
func recordSyncOutcome(r syncOutcomeRecord) error {
	if r.dryRun {
		// A dry run installs nothing, so it has nothing to vouch for.
		return nil
	}
	st := syncState{
		Schema:     syncStateSchema,
		Status:     syncStatusIncomplete,
		RecordedAt: r.now.UTC(),
		Inputs:     r.fingerprint,
		Failed:     r.failed,
	}
	if r.complete {
		st.Status = syncStatusComplete
		st.Failed = nil
	}

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(st); err != nil {
		return fmt.Errorf("encoding sync state: %w", err)
	}
	// atomicfile refuses a symlink at the path (gh#193), so a torn or
	// hijacked stamp is not reachable.
	if err := atomicfile.Write(syncStatePath(r.galeDir), buf.Bytes()); err != nil {
		return fmt.Errorf("writing sync state: %w", err)
	}
	return nil
}

// syncNeeded decides whether an automatic activation should sync.
//
// now is a parameter rather than a time.Now() call so the retry
// interval is testable without wall-clock.
//
// The order matters, and every branch that cannot answer the question
// falls toward doing the work: a wrong "sync" costs one sync, a wrong
// "skip" leaves a package missing from PATH with nothing said.
//
// The stamp is read before the generation is looked for, deliberately.
// A locked sync that fails leaves the generation untouched (§8), so a
// project whose FIRST sync failed has no `current` at all — and a
// missing-generation shortcut ahead of the backoff would run a full
// failing sync on every activation, which for a source build is a
// minutes-long stall on every cd. That is worse than the sticky-stale
// environment this whole mechanism exists to prevent. The recorder is
// deferred from runSync rather than from the rebuild, so the stamp is
// written whether or not a generation was ever built.
func syncNeeded(galeDir, fingerprint string, now time.Time) syncCheck {
	st, err := readSyncState(galeDir)
	switch {
	// 1a. Never stamped: a project from before this gale, or one whose
	//     first sync has not run. Covers the no-generation case too:
	//     with no stamp there is nothing to back off from.
	case errors.Is(err, fs.ErrNotExist):
		return syncCheck{
			Needed: true,
			Reason: "no sync has completed in this project",
		}
	// 1b. Stamped but unreadable — a different condition, and one the
	//     user may want to look at.
	case err != nil:
		return syncCheck{
			Needed: true,
			Reason: fmt.Sprintf("sync state unreadable: %v", err),
		}
	}

	// 2. The inputs moved. An edit reaches the packages regardless of
	//    the retry interval, which rate-limits identical work only.
	if st.Inputs != fingerprint {
		return syncCheck{
			Needed: true,
			Reason: "gale.toml or gale.lock changed since the last sync",
		}
	}

	// 3. Inside the interval after a failure: say what is missing, do
	//    no work. Ahead of the generation check, so the bound holds
	//    even when the failed sync never published one.
	if st.Status == syncStatusIncomplete &&
		now.Sub(st.RecordedAt) < syncRetryInterval {
		return syncCheck{
			Reason: "a sync failed recently on these inputs",
			Notice: incompleteNotice(st),
		}
	}

	// 4. No generation, nothing to vouch for. A stamp cannot describe
	//    an environment that gc or a deleted .gale has since removed,
	//    however cleanly the sync that wrote it went.
	if _, err := os.Lstat(filepath.Join(galeDir, "current")); err != nil {
		return syncCheck{Needed: true, Reason: "no active generation"}
	}

	// 5. The fast path this whole mechanism must not cost: silent.
	if st.Status == syncStatusComplete {
		return syncCheck{Reason: "last sync completed on these inputs"}
	}

	// 6. Incomplete and the interval elapsed — exactly one further
	//    attempt.
	return syncCheck{
		Needed: true,
		Reason: "retrying a sync that did not complete",
	}
}

// readSyncState loads and validates the stamp. A stamp whose schema or
// status it does not recognise is an error, not a default: guessing
// would let a future format read as a completed sync.
func readSyncState(galeDir string) (syncState, error) {
	data, err := os.ReadFile(syncStatePath(galeDir))
	if err != nil {
		// Returned unwrapped so the caller can tell "absent" from
		// "unreadable" with errors.Is.
		return syncState{}, err
	}
	var st syncState
	if _, err := toml.Decode(string(data), &st); err != nil {
		return syncState{}, fmt.Errorf("parsing %s: %w", syncStateFile, err)
	}
	if st.Schema != syncStateSchema {
		return syncState{}, fmt.Errorf(
			"%s: unsupported schema %d", syncStateFile, st.Schema,
		)
	}
	if st.Status != syncStatusComplete && st.Status != syncStatusIncomplete {
		return syncState{}, fmt.Errorf(
			"%s: unknown status %q", syncStateFile, st.Status,
		)
	}
	return st, nil
}

// incompleteNotice is the single line an activation prints while it is
// withholding a retry. It names the packages so the user can act
// without running anything first.
//
// It must also name the escape hatch, because this line is the only
// place it is ever said: a user-typed `gale sync` ignores the stamp
// and the interval entirely. Stating both halves — retry now, or wait
// this long — is what keeps the backoff from reading as "gale has
// given up".
func incompleteNotice(st syncState) string {
	what := "the last sync did not complete"
	if len(st.Failed) > 0 {
		what = "not installed: " + strings.Join(st.Failed, ", ")
	}
	return fmt.Sprintf(
		"%s — run 'gale sync' to retry now, or wait up to %s for the "+
			"next automatic attempt",
		what, syncRetryInterval,
	)
}

// failedPackageNames lists the packages a sync could not install, in
// the outcomes' order (sortedSyncItems makes that name order). Both
// failure modes count: a package whose recipe would not resolve is as
// absent from PATH as one whose build broke.
func failedPackageNames(outcomes []syncOutcome) []string {
	var names []string
	for _, o := range outcomes {
		if o.resolveErr == nil && o.installErr == nil {
			continue
		}
		names = append(names, o.name+"@"+o.version)
	}
	return names
}

// beginSyncStamp applies the --if-needed gate and hands back the
// recorder for this run's verdict (gh#186).
//
// withheld true means the run does no work; the caller returns without
// syncing AND without recording, because a run that did nothing must
// not reset the retry interval.
//
// The fingerprint is taken before anything installs: the inputs this
// run acted on are what a later activation compares against. When it
// cannot be computed there is nothing to gate or stamp against, so the
// recorder is a no-op and the sync runs unconditionally — the
// behaviour that predates the stamp.
func beginSyncStamp(out *output.Output, galeDir, galePath, host string) (
	record func(complete bool, failed []string), withheld bool,
) {
	fingerprint, err := syncFingerprint(galePath, host, currentPlatform())
	if err != nil {
		return func(bool, []string) {}, false
	}
	if syncWithheld(out, galeDir, fingerprint) {
		return func(bool, []string) {}, true
	}
	return func(complete bool, failed []string) {
		stampSync(out, galeDir, fingerprint, complete, failed)
	}, false
}

// syncWithheld reports whether --if-needed turns this run into a
// no-op, printing the withheld-retry notice when it does. Without the
// flag it is always false: a user-typed `gale sync` ignores the
// completion stamp and the retry interval entirely.
func syncWithheld(out *output.Output, galeDir, fingerprint string) bool {
	if !syncOnlyIfNeeded {
		return false
	}
	check := syncNeeded(galeDir, fingerprint, time.Now())
	out.Verbosef("sync --if-needed: %s", check.Reason)
	if check.Needed {
		return false
	}
	if check.Notice != "" {
		out.Warn(check.Notice)
	}
	return true
}

// stampSync records one sync's verdict. A stamp that cannot be written
// warns and nothing more: failing to record must not change the exit
// status of the sync it describes.
func stampSync(
	out *output.Output, galeDir, fingerprint string,
	complete bool, failed []string,
) {
	if err := recordSyncOutcome(syncOutcomeRecord{
		galeDir:     galeDir,
		fingerprint: fingerprint,
		complete:    complete,
		failed:      failed,
		dryRun:      dryRun,
		now:         time.Now(),
	}); err != nil {
		out.Warn(fmt.Sprintf("recording sync state: %v", err))
	}
}

// syncStateWantsWork answers syncNeeded for shell and run, which reach
// activation through syncIfNeeded rather than through the direnv hook.
// Their own gate, lockIsStale, cannot see a partial install failure:
// the lock still describes the manifest exactly, so it reports fresh
// while a package is missing from PATH.
//
// A fingerprint that cannot be computed reports work, for the same
// reason syncNeeded's unreadable branches do.
func syncStateWantsWork(warn func(string), galeDir, configPath, host string) bool {
	fingerprint, err := syncFingerprint(configPath, host, currentPlatform())
	if err != nil {
		return true
	}
	check := syncNeeded(galeDir, fingerprint, time.Now())
	if !check.Needed && check.Notice != "" {
		warn(check.Notice)
	}
	return check.Needed
}
