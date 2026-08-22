package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kelp/gale/internal/index"
	"github.com/kelp/gale/internal/output"
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

// TestOutdatedUnreachableIndexFailsRun pins §15.16: index fetch
// errors are errors. outdated resolves through the index client
// with no cache behind it, so an unreachable index must fail the
// run instead of serving a stale answer or silently passing.
func TestOutdatedUnreachableIndexFailsRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := home
	if err := os.WriteFile(filepath.Join(proj, "gale.toml"),
		[]byte("[packages]\njust = \"1.56.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(proj)

	var buf bytes.Buffer
	out := output.NewWithOptions(&buf, output.Options{})

	// A directory that is not a git checkout cannot yield an
	// index HEAD.
	src := index.Source{Dir: filepath.Join(t.TempDir(), "nope")}
	err := runOutdated(context.Background(), src, out)
	if err == nil {
		t.Fatal("unreachable index produced no error: outdated has " +
			"no business succeeding without the index")
	}
	if !strings.Contains(err.Error(), "index") {
		t.Errorf("err = %v, want it to name the index", err)
	}
}

// TestCheckOutdatedReportsEveryFailure pins the per-package
// contract: one bad entry does not poison the rest of the run,
// every failure is recorded, and none is answered from a cache.
func TestCheckOutdatedReportsEveryFailure(t *testing.T) {
	var (
		mu    sync.Mutex
		calls []string
	)
	latest := func(name string) (string, error) {
		mu.Lock()
		calls = append(calls, name)
		mu.Unlock()
		if name == "missing" {
			return "", fmt.Errorf("index/j/missing.toml: %w",
				errors.New("not found"))
		}
		return "1.0", nil // same version → not outdated
	}

	pkgs := map[string]string{
		"a": "1.0", "missing": "1.0", "b": "1.0",
	}
	var buf bytes.Buffer
	out := output.NewWithOptions(&buf, output.Options{})
	result := checkOutdated(pkgs, latest, out)

	if len(calls) != 3 {
		t.Errorf("expected all 3 packages probed, got %d calls: %v",
			len(calls), calls)
	}
	if result.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", result.Skipped)
	}
	if len(result.Errors) != 1 || !strings.Contains(
		result.Errors[0].Error(), "missing",
	) {
		t.Errorf("Errors = %v, want the missing entry named",
			result.Errors)
	}
}
