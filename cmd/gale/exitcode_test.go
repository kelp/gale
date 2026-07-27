package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/lockfile"
)

// TestExitCodeFor pins the taxonomy CI scripts branch on. The split
// that matters is 3 against 4: code 3 means something disagreed with
// bytes the lock names and deserves a human, while code 4 means the
// lock needs regenerating, which a pipeline can often do itself.
// Every case wraps the sentinel, because callers add context with
// %w and the classifier must traverse the chain rather than compare.
func TestExitCodeFor(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
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
	}
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
