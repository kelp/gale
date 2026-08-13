package main

import (
	"fmt"
	"strings"
	"testing"
)

// The flat pre-enforcement lockfile schema, as a test fixture.
//
// gale no longer writes it. lockfile.Write was culled with gh#197,
// because a flat-schema writer is precisely the downgrade the v1
// guard entry exists to stop: it would convert an enforced lock back
// into one an already-shipped gale rewrites at will. Every gale
// shipped before enforcement wrote this schema though, so the
// readers, the refusals and the byte claims still have to be
// exercised against it — which means the tests spell the bytes.

// legacyLockText renders one flat-schema entry: the package name it
// is keyed by, the version it pins, and the checksum nothing ever
// verified. There is no platform dimension; that gap is what makes
// the schema unusable as an integrity lock.
func legacyLockText(name, version, sha string) string {
	return fmt.Sprintf("[packages.%s]\nversion = %q\nsha256 = %q\n",
		name, version, sha)
}

// legacyLockBody is the one-entry fixture shared by the tests that
// only need a lock this build refuses, not a particular pin.
var legacyLockBody = legacyLockText(
	"hello", "1.0-1", strings.Repeat("1a", 32),
)

// writeLegacyLock puts a one-entry legacy lock at path, creating its
// directory.
func writeLegacyLock(t *testing.T, path, name, version, sha string) {
	t.Helper()
	writeFile(t, path, legacyLockText(name, version, sha))
}
