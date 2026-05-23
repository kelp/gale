package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/recipe"
)

// TestSummarizeOutdatedExitsNonZeroWhenAllSkipped pins
// audit/readonly/exit-codes/0002 and empty-state/0002: if every
// declared package failed to resolve, outdated must exit
// non-zero, not print "Everything is up to date."
func TestSummarizeOutdatedExitsNonZeroWhenAllSkipped(t *testing.T) {
	var buf bytes.Buffer
	out := output.NewWithOptions(&buf, output.Options{})

	err := summarizeOutdated(outdatedResult{
		Skipped: 3,
	}, out)
	if err == nil {
		t.Fatal("expected non-zero exit when all packages skipped")
	}
	if !strings.Contains(err.Error(), "3") {
		t.Errorf("error should mention skip count, got: %v", err)
	}
	if strings.Contains(buf.String(), "up to date") {
		t.Errorf("must not print 'up to date' when all skipped, got: %q",
			buf.String())
	}
}

// TestSummarizeOutdatedExitsZeroOnGenuineAllClear keeps the
// happy-path contract: nothing skipped, nothing outdated →
// success message, zero exit.
func TestSummarizeOutdatedExitsZeroOnGenuineAllClear(t *testing.T) {
	var buf bytes.Buffer
	out := output.NewWithOptions(&buf, output.Options{})

	err := summarizeOutdated(outdatedResult{}, out)
	if err != nil {
		t.Errorf("expected nil error on clean run, got: %v", err)
	}
	if !strings.Contains(buf.String(), "up to date") {
		t.Errorf("expected 'up to date' line, got: %q", buf.String())
	}
}

// TestSummarizeOutdatedPartialSkipExitsNonZero verifies the
// in-between case: some packages checked, some failed. We
// surface a non-zero exit so the partial result isn't
// mistaken for a clean signal in CI.
func TestSummarizeOutdatedPartialSkipExitsNonZero(t *testing.T) {
	var buf bytes.Buffer
	out := output.NewWithOptions(&buf, output.Options{})

	err := summarizeOutdated(outdatedResult{
		Items:   []outdatedItem{{Name: "jq"}},
		Skipped: 2,
	}, out)
	if err == nil {
		t.Fatal("expected non-zero exit on partial skip")
	}
}

// TestCheckOutdatedStopsAfterFirstTransportError pins
// audit/readonly/network-perf/0003: when the first resolver
// call fails with a transport-level error, we stop probing
// the remaining packages and report them all as skipped.
// This caps a registry-unreachable run at one timeout, not N.
func TestCheckOutdatedStopsAfterFirstTransportError(t *testing.T) {
	var calls []string
	resolver := func(name string) (*recipe.Recipe, error) {
		calls = append(calls, name)
		// Simulate connection refused on every call.
		return nil, errors.New(
			"fetch recipe: connection refused")
	}

	pkgs := map[string]string{
		"a": "1.0", "b": "1.0", "c": "1.0", "d": "1.0",
	}
	var buf bytes.Buffer
	out := output.NewWithOptions(&buf, output.Options{})
	result := checkOutdated(pkgs, resolver, out)

	if len(calls) != 1 {
		t.Errorf("expected exactly 1 resolver call after first "+
			"transport error, got %d: %v", len(calls), calls)
	}
	if result.Skipped != 4 {
		t.Errorf("Skipped = %d, want 4 (all packages)",
			result.Skipped)
	}
}

// TestCheckOutdatedContinuesPastPerPackageErrors verifies
// that a non-transport error (e.g. recipe not found in
// registry) does not poison the rest of the run. A 404 for
// one package is per-package; the loop must keep going.
func TestCheckOutdatedContinuesPastPerPackageErrors(t *testing.T) {
	var calls []string
	resolver := func(name string) (*recipe.Recipe, error) {
		calls = append(calls, name)
		if name == "missing" {
			return nil, fmt.Errorf("fetch recipe missing: HTTP 404")
		}
		// Return a recipe with the same version so it's not
		// reported as outdated.
		return &recipe.Recipe{
			Package: recipe.Package{
				Name: name, Version: "1.0", Revision: 1,
			},
		}, nil
	}

	pkgs := map[string]string{
		"a": "1.0", "missing": "1.0", "b": "1.0",
	}
	var buf bytes.Buffer
	out := output.NewWithOptions(&buf, output.Options{})
	result := checkOutdated(pkgs, resolver, out)

	if len(calls) != 3 {
		t.Errorf("expected all 3 packages probed, got %d calls: %v",
			len(calls), calls)
	}
	if result.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", result.Skipped)
	}
}

// TestIsTransportErrorDetectsCommonShapes pins the heuristic
// used by checkOutdated to short-circuit the loop. These
// strings come from net.OpError, the http stdlib timeout
// message, and our cache contract's offline error.
func TestIsTransportErrorDetectsCommonShapes(t *testing.T) {
	transport := []string{
		"dial tcp 127.0.0.1:1: connect: connection refused",
		"dial tcp: lookup nope.invalid: no such host",
		"net/http: request canceled (Client.Timeout exceeded)",
		"read tcp: i/o timeout",
		"context deadline exceeded",
		"context canceled",
		"GALE_OFFLINE=1 and no cached entry for jq.toml",
	}
	for _, s := range transport {
		if !isTransportError(errors.New(s)) {
			t.Errorf("expected transport error for: %q", s)
		}
	}

	notTransport := []string{
		"HTTP 404",
		"HTTP 500",
		"parsing recipe: bad TOML",
		"version not found in registry",
	}
	for _, s := range notTransport {
		if isTransportError(errors.New(s)) {
			t.Errorf("should NOT be transport error: %q", s)
		}
	}
}
