package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/registry"
)

// newSearchTestRegistry returns a registry backed by an
// in-memory TSV index served by an httptest server. The
// server is closed via t.Cleanup.
func newSearchTestRegistry(
	t *testing.T, index string,
) *registry.Registry {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, index)
		}))
	t.Cleanup(srv.Close)
	return &registry.Registry{BaseURL: srv.URL}
}

// TestSearchNoMatchExitsNonZero pins audit
// RO-J:exit-codes/0004: when no packages match, search must
// return a non-zero exit. Peer not-found commands (info,
// which, verify, audit) all do; search was the lone outlier.
// A shell pipeline `gale search foo && install-helper` should
// not run the helper when foo doesn't exist.
func TestSearchNoMatchExitsNonZero(t *testing.T) {
	reg := newSearchTestRegistry(t,
		"jq\tJSON processor\nripgrep\tFast grep\n")

	var stdout bytes.Buffer
	err := runSearch(&stdout, reg, "zzzznotexist")
	if err == nil {
		t.Fatal("expected error for no match, got nil")
	}
	if stdout.Len() != 0 {
		t.Errorf(
			"no-match should leave stdout empty, got: %q",
			stdout.String())
	}
	if !strings.Contains(err.Error(), "no packages found") {
		t.Errorf(
			"expected error message to mention 'no packages "+
				"found', got: %v", err)
	}
}

// TestSearchEmptyQueryRejected pins the same finding's second
// half: `gale search ""` should refuse the empty query rather
// than exit 0 after iterating zero results.
func TestSearchEmptyQueryRejected(t *testing.T) {
	reg := newSearchTestRegistry(t, "jq\tJSON processor\n")

	var stdout bytes.Buffer
	err := runSearch(&stdout, reg, "")
	if err == nil {
		t.Fatal("expected error for empty query, got nil")
	}
}

// TestSearchMatchExitsZero verifies the happy path stays
// happy: a matching query returns nil and writes results to
// stdout.
func TestSearchMatchExitsZero(t *testing.T) {
	reg := newSearchTestRegistry(t,
		"jq\tJSON processor\nripgrep\tFast grep\n")

	var stdout bytes.Buffer
	if err := runSearch(&stdout, reg, "jq"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stdout.String(), "jq") {
		t.Errorf("expected jq in stdout, got: %q",
			stdout.String())
	}
}
