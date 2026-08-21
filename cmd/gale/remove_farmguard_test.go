package main

import (
	"os"
	"path/filepath"
	"testing"
)

// bytes.Equal(nil, []byte{}) is true, so a snapshot carrying only
// content cannot tell "no file" from "an empty file". A
// compare-and-swap built on one would treat a concurrent writer's
// empty file as still-ours and delete it.
func TestFileSnapshotDistinguishesAbsentFromEmpty(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent")
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	absentSnap, err := readFileSnapshot(missing)
	if err != nil {
		t.Fatal(err)
	}
	emptySnap, err := readFileSnapshot(empty)
	if err != nil {
		t.Fatal(err)
	}
	if absentSnap.Same(emptySnap) {
		t.Error("an absent file and an empty one compare equal: a " +
			"compare-and-swap on this would delete a concurrent " +
			"writer's empty file believing it was still ours")
	}
	if !absentSnap.Same(FileSnapshot{}) {
		t.Error("the zero snapshot must mean absent")
	}
}
