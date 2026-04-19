// Package version provides gale's version comparison rules,
// shared by `gale update` and `gale outdated` so they agree on
// what counts as an upgrade.
//
// gale versions look like `<major>.<minor>.<patch>[-<revision>]`.
// Unlike semver, a `-<N>` suffix on the patch (with N all-digit)
// is a Debian-style revision — newer than the bare triple, not
// a pre-release. Any other suffix (e.g. `1.2.3-dev.2+abc`) is
// treated as a conventional semver pre-release and delegated to
// golang.org/x/mod/semver so dev → stable ordering keeps working.
// Non-semver strings (git hashes, "dev") are optimistic: IsNewer
// returns true so the update proceeds.
package version

import (
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

// IsNewer reports whether candidate is strictly newer than
// current. See the package comment for the ordering rules.
func IsNewer(candidate, current string) bool {
	cBase, cRev := splitRevision(candidate)
	iBase, iRev := splitRevision(current)

	cv := "v" + cBase
	iv := "v" + iBase
	if !semver.IsValid(cv) || !semver.IsValid(iv) {
		// Non-semver: can't compare, allow the update.
		return true
	}
	switch semver.Compare(cv, iv) {
	case 1:
		return true
	case -1:
		return false
	}
	// Triples are equal (including any pre-release tags).
	// Break the tie on revision.
	return cRev > iRev
}

// splitRevision peels a numeric `-<N>` suffix off the end of v
// and returns (base, revision). A missing or non-numeric
// suffix leaves v untouched and revision defaults to 1
// (matching recipe parsing: an absent or <= 0 revision means 1).
func splitRevision(v string) (base string, revision int) {
	dash := strings.LastIndexByte(v, '-')
	if dash < 0 {
		return v, 1
	}
	suffix := v[dash+1:]
	if suffix == "" {
		return v, 1
	}
	n, err := strconv.Atoi(suffix)
	if err != nil || n <= 0 {
		// Non-numeric suffix (dev.2, rc1, …) is a semver
		// pre-release; leave it attached to the base so
		// semver.Compare can order it.
		return v, 1
	}
	return v[:dash], n
}
