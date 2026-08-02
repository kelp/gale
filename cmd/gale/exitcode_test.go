package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/activation"
	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/lockplan"
	"github.com/kelp/gale/internal/provenance"
)

// TestExitCodeFor pins the taxonomy CI scripts branch on. The split
// that matters is 3 against 4: code 3 means something disagreed with
// bytes the lock names and deserves a human, while code 4 means the
// lock needs regenerating, which a pipeline can often do itself.
// Every case wraps the sentinel, because callers add context with
// %w and the classifier must traverse the chain rather than compare.
// exitCodeCase is one error and the exit code it must classify as.
type exitCodeCase struct {
	name string
	err  error
	want int
}

// exitCodeCases is a function rather than an inline literal so the test
// body stays short enough to read as assertions. It is split by class
// for length alone; the classification comments stay with their cases.
func exitCodeCases() []exitCodeCase {
	return append(exitCodeLockCases(), exitCodeStoreCases()...)
}

// exitCodeLockCases covers the errors that describe the lockfile
// itself: its schema, its contents, and the farm guard that fails
// closed on another scope's copy of it.
func exitCodeLockCases() []exitCodeCase {
	return []exitCodeCase{
		{
			name: "success",
			err:  nil,
			want: 0,
		},
		{
			name: "ordinary failure",
			err:  errors.New("connection refused"),
			want: exitFailure,
		},
		{
			name: "legacy schema",
			err:  fmt.Errorf("sync: %w", lockfile.ErrLegacySchema),
			want: exitLockUnusable,
		},
		{
			name: "unknown schema version",
			err:  fmt.Errorf("sync: %w", lockfile.ErrUnknownVersion),
			want: exitLockUnusable,
		},
		{
			name: "unknown field",
			err:  fmt.Errorf("sync: %w", lockfile.ErrUnknownField),
			want: exitLockUnusable,
		},
		{
			name: "malformed downgrade guard",
			err:  fmt.Errorf("sync: %w", lockfile.ErrDowngradeGuard),
			want: exitLockUnusable,
		},
		{
			name: "cross-project farm conflict",
			err:  fmt.Errorf("sync: %w", farm.ErrClaimConflict),
			want: exitLockIntegrity,
		},
		{
			// The guard fails closed on a claimant whose lock is
			// unusable, wrapping that lock's sentinel. The refusal
			// is still the farm guard's: regenerating the
			// INITIATING scope's lock cannot fix another scope's
			// file, so the pipeline-actionable class 4 would
			// mislead. Class 3 (a human decides) wins.
			name: "farm conflict over an unreadable claimant",
			err: fmt.Errorf("sync: %w",
				fmt.Errorf("%w: cannot read the closure of project "+
					"/b: %w", farm.ErrClaimConflict,
					lockfile.ErrMalformed)),
			want: exitLockIntegrity,
		},
		{
			name: "unserializable locked closure",
			err:  fmt.Errorf("gate: %w", lockgraph.ErrMissingDep),
			want: exitLockUnusable,
		},
		{
			name: "cyclic locked closure",
			err:  fmt.Errorf("gate: %w", lockgraph.ErrCycle),
			want: exitLockUnusable,
		},
	}
}

// exitCodeStoreCases covers the errors that describe the store beside
// the lock: a directory's provenance record, what a recipe declares
// for it, and the generation built from it.
func exitCodeStoreCases() []exitCodeCase {
	return []exitCodeCase{
		{
			name: "provenance disagrees with the lock",
			err:  fmt.Errorf("gate: %w", provenance.ErrInvalid),
			want: exitLockIntegrity,
		},
		{
			// Absent provenance under a lock arrives wrapped in
			// ErrInvalid, so it must classify as an integrity conflict
			// rather than falling through to an ordinary failure.
			name: "no provenance where the lock names bytes",
			err:  fmt.Errorf("%w: %w", provenance.ErrInvalid, provenance.ErrAbsent),
			want: exitLockIntegrity,
		},
		{
			// Section 8's table names a graph_digest mismatch in the
			// integrity row. The lock asserts a digest its own contents
			// do not produce, which is a disagreement about recorded
			// bytes rather than a file to regenerate and retry.
			name: "graph digest disagrees with the locked closure",
			err:  fmt.Errorf("gate: %w", lockplan.ErrDigestMismatch),
			want: exitLockIntegrity,
		},
		{
			// An artifact outside the persisted format cannot be
			// modeled, which is the class every other unmodelable lock
			// carries; without the case it fell through to exit 1.
			name: "malformed lock artifact",
			err:  fmt.Errorf("plan: %w", lockplan.ErrMalformedArtifact),
			want: exitLockUnusable,
		},
		{
			// `gale lock` found an installed artifact whose recorded
			// hash disagrees with the recipe's declared one. Section 8
			// puts an artifact SHA or manifest digest disagreement in
			// the integrity row: the directory is validly provenanced,
			// so nothing here is regenerable and a human decides which
			// side is wrong.
			name: "installed provenance disagrees with the recipe",
			err:  fmt.Errorf("lock: %w", errRecipeDisagrees),
			want: exitLockIntegrity,
		},
		{
			// An occupied canonical directory with no record at all.
			// Section 8's "store-dir provenance conflict": the store
			// holds bytes for an identity that nothing attests, and
			// rewriting the lock cannot change that, so the
			// pipeline-actionable class 4 would send a script round a
			// loop it can never exit.
			name: "occupied store dir with no provenance",
			err:  fmt.Errorf("lock: %w", errUnprovenancedStoreDir),
			want: exitLockIntegrity,
		},
		{
			// The lock modeled the node fine; the recipe on disk no
			// longer backs it. Section 8's class-4 rows all describe
			// something MISSING from the lock, which regenerating fixes.
			// Here gale.toml and gale.lock agree and a recipe fetched
			// for a pinned version-revision says something else, so
			// regenerating would ratify the recipe that moved. That is
			// the substitution #182 exists to close.
			name: "recipe sha256 disagrees with the locked node",
			err: fmt.Errorf("plan: %w",
				fmt.Errorf("%w: jq recipe sha256 abc, lock says def",
					lockplan.ErrRecipeMismatch)),
			want: exitLockIntegrity,
		},
		{
			// Same sentinel, and deliberately the case that reads most
			// like class 4's "missing platform entry". It is not: a
			// lock with no entry for this platform fails earlier as
			// ErrMissingArtifact. Reaching here means the lock
			// affirmatively claims the platform and the recipe denies
			// it, which is the recipe moving, not the lock lacking.
			name: "recipe denies a platform the lock claims",
			err: fmt.Errorf("plan: %w",
				fmt.Errorf("%w: jq is locked for linux/amd64, which the recipe does not support",
					lockplan.ErrRecipeMismatch)),
			want: exitLockIntegrity,
		},
		{
			// Design §13's cross-scope veto: another project's lock
			// requires different bytes at the same store path. Not
			// class 4 — regenerating the initiating scope's lock cannot
			// change what another scope requires, so a pipeline that
			// retried on it would loop forever.
			name: "another scope requires different bytes",
			err:  fmt.Errorf("migrate: %w", errScopeDisagrees),
			want: exitLockIntegrity,
		},
		{
			// The same conflict as the row above, seen from inside the
			// scope holding the operation: a package it actively loads
			// records the artifact a refresh would replace. §13 exempts
			// the initiating scope from the cross-scope veto, so this is
			// the only refusal that covers it, and it must not land in a
			// different class than its twin. Re-pinning is the remedy,
			// which no pipeline can do on its own.
			name: "a dependent in this scope records the target",
			err:  fmt.Errorf("refresh: %w", errDependentRecord),
			want: exitLockIntegrity,
		},
		{
			// The initiating scope's form of the legacy closure veto.
			// checkScopeClosure raises errScopeDisagrees for any other
			// scope in exactly this state, so classifying this one lower
			// would give one condition two shell meanings depending on
			// which scope happened to hold it. Regenerating a lock does
			// not make unreadable dependency metadata readable.
			name: "this scope's closure could not be read",
			err:  fmt.Errorf("refresh: %w", errScopeClosureUnreadable),
			want: exitLockIntegrity,
		},
		{
			name: "activation drift",
			err:  fmt.Errorf("gate: %w", activation.ErrDrift),
			want: exitActivationDrift,
		},
	}
}

// A recipe mismatch that also wraps an inner error must still classify
// as integrity. validateMethod's trust-policy branch double-wraps
// ("%w: %s: %w"), so both sentinels are visible to errors.Is. Nothing
// class-4 hides in there today, which is exactly why this is pinned:
// the ordering that makes it safe is invisible until something inside
// a wrapped error starts carrying a sentinel of its own.
func TestExitCodeForRecipeMismatchWinsOverAWrappedInnerError(t *testing.T) {
	err := fmt.Errorf(
		"%w: jq: %w",
		lockplan.ErrRecipeMismatch,
		fmt.Errorf("stale: %w", lockfile.ErrStaleLock),
	)
	if got := exitCodeFor(err); got != exitLockIntegrity {
		t.Errorf("double-wrapped recipe mismatch: got exit %d, want %d",
			got, exitLockIntegrity)
	}
}

func TestExitCodeFor(t *testing.T) {
	tests := exitCodeCases()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitCodeFor(tt.err); got != tt.want {
				t.Errorf("exitCodeFor(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

// TestExitCodeForMalformedLock reads a genuinely malformed
// lockfile rather than wrapping a sentinel by hand. Section 8 puts
// any present-but-unparseable lock in class 4, and a hand-wrapped
// error would not have caught that ReadV1's parse failures once
// carried no sentinel at all and fell through to exit 1.
func TestExitCodeForMalformedLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gale.lock")
	if err := os.WriteFile(path, []byte("version = 1\n[targets\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	_, err := lockfile.ReadV1(path)
	if err == nil {
		t.Fatal("ReadV1 accepted malformed TOML")
	}
	if got := exitCodeFor(err); got != exitLockUnusable {
		t.Errorf("exitCodeFor(%v) = %d, want %d", err, got, exitLockUnusable)
	}
}

// TestExitCodeValues is a compatibility golden. These numbers are a
// shell API: docs/ci-cd.md tells users to branch on them, so
// changing one breaks pipelines and must be a deliberate edit here
// rather than a side effect of touching the classifier.
func TestExitCodeValues(t *testing.T) {
	tests := []struct {
		name string
		got  int
		want int
	}{
		{"ordinary failure", exitFailure, 1},
		{"lock integrity violation", exitLockIntegrity, 3},
		{"lock unusable", exitLockUnusable, 4},
		{"activation drift", exitActivationDrift, 5},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %d, want %d", tt.name, tt.got, tt.want)
		}
	}
}

// TestExitCodeForDeepestClassWins: a lock error wrapped by an
// ordinary one still exits with the lock code. Otherwise the class
// a pipeline branches on would depend on how many layers of
// fmt.Errorf a call path happened to add.
func TestExitCodeForDeepestClassWins(t *testing.T) {
	err := fmt.Errorf("installing jq: %w",
		fmt.Errorf("reading lock: %w", lockfile.ErrDowngradeGuard))
	if got := exitCodeFor(err); got != exitLockUnusable {
		t.Errorf("exitCodeFor = %d, want %d", got, exitLockUnusable)
	}
}

// Design §8 puts an artifact SHA mismatch in the integrity class, and
// under a lock that is precisely what a failed artifact verification
// is: bytes that disagree with the hash the lock names. The installer
// wrapped the download error as "locked binary install", which carried
// no sentinel, so the single most important detection in #182 exited 1
// — indistinguishable from a build error or a refused connection.
//
// A pipeline branches on this number. Class 1 says "retry or fix the
// build"; class 3 says "stop, an artifact is not what was locked".
func TestExitCodeForLockedArtifactMismatch(t *testing.T) {
	err := fmt.Errorf("locked binary install of jq@1.7-1: %w",
		fmt.Errorf("%w: %w", provenance.ErrInvalid,
			fmt.Errorf("verify: %w", download.ErrSHA256Mismatch)))

	if got := exitCodeFor(err); got != exitLockIntegrity {
		t.Errorf("locked artifact mismatch: got exit %d, want %d",
			got, exitLockIntegrity)
	}
}

// An ordinary failure under a lock stays ordinary (acceptance 11): a
// network error is not an integrity violation, and classifying it as
// one would train users to ignore the code that means tampering.
func TestExitCodeForLockedNetworkFailure(t *testing.T) {
	err := fmt.Errorf("locked binary install of jq@1.7-1: %w",
		errors.New("connection refused"))

	if got := exitCodeFor(err); got != exitFailure {
		t.Errorf("locked network failure: got exit %d, want %d",
			got, exitFailure)
	}
}
