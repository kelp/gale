package main

// Tests for issue #63: gale outdated always reports git-installed
// packages as outdated even when installed at the current remote HEAD.
//
// Root cause: ver.IsNewer returns true unconditionally when either
// argument is non-semver, and bare git short hashes are never valid
// semver.

import (
	"bytes"
	"testing"

	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/recipe"
)

// TestCheckOutdatedGitHashNotReportedAsOutdated is the RED test for
// issue #63. A package installed via --git stores a bare short hash
// (e.g. "abc1234") as its version. checkOutdated must not report it
// as outdated just because the hash fails semver validation.
func TestCheckOutdatedGitHashNotReportedAsOutdated(t *testing.T) {
	// Resolver returns a semver recipe version ("1.2.3-1").
	// The installed version is a bare git hash — non-semver.
	// The invariant: a read-only report command must not flag a
	// package as outdated solely because of version format mismatch.
	resolver := func(name string) (*recipe.Recipe, error) {
		return &recipe.Recipe{
			Package: recipe.Package{
				Name:     name,
				Version:  "1.2.3",
				Revision: 1,
			},
		}, nil
	}

	pkgs := map[string]string{
		"mypkg": "abc1234", // bare git short hash
	}

	var buf bytes.Buffer
	out := output.NewWithOptions(&buf, output.Options{})
	result := checkOutdated(pkgs, resolver, out)

	if len(result.Items) != 0 {
		t.Errorf(
			"git-hash-installed package reported as outdated: "+
				"got %d item(s), want 0. "+
				"Items: %v",
			len(result.Items), result.Items,
		)
	}
}

// TestCheckOutdatedGitHashSameAsLatestNotOutdated verifies that
// when both installed and latest are the same git hash, the package
// is not reported as outdated.
func TestCheckOutdatedGitHashSameAsLatestNotOutdated(t *testing.T) {
	resolver := func(name string) (*recipe.Recipe, error) {
		// Recipe version is also a git hash (edge case).
		return &recipe.Recipe{
			Package: recipe.Package{
				Name:     name,
				Version:  "abc1234",
				Revision: 1,
			},
		}, nil
	}

	pkgs := map[string]string{
		"mypkg": "abc1234",
	}

	var buf bytes.Buffer
	out := output.NewWithOptions(&buf, output.Options{})
	result := checkOutdated(pkgs, resolver, out)

	if len(result.Items) != 0 {
		t.Errorf(
			"identical git hash should not be reported as outdated: "+
				"got %d item(s), want 0",
			len(result.Items),
		)
	}
}

// TestCheckOutdatedSemverStillWorks verifies the normal (semver)
// case is unaffected by the git-hash guard.
func TestCheckOutdatedSemverStillWorks(t *testing.T) {
	resolver := func(name string) (*recipe.Recipe, error) {
		return &recipe.Recipe{
			Package: recipe.Package{
				Name:     name,
				Version:  "2.0.0",
				Revision: 1,
			},
		}, nil
	}

	pkgs := map[string]string{
		"mypkg": "1.0.0-1", // older semver version
	}

	var buf bytes.Buffer
	out := output.NewWithOptions(&buf, output.Options{})
	result := checkOutdated(pkgs, resolver, out)

	if len(result.Items) != 1 {
		t.Errorf(
			"semver outdated package not reported: "+
				"got %d item(s), want 1",
			len(result.Items),
		)
	}
}
