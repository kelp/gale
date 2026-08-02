package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/lockfile"
)

func TestVerifyBlobURL(t *testing.T) {
	t.Setenv("GALE_GHCR_URL", "")
	const sha = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	got := verifyBlobURL("hello", sha)
	want := "https://ghcr.io/v2/kelp/gale-recipes/hello/blobs/sha256:" + sha
	if got != want {
		t.Fatalf("verifyBlobURL = %q, want %q", got, want)
	}

	t.Setenv("GALE_GHCR_URL", "http://127.0.0.1:5555")
	got = verifyBlobURL("hello", sha)
	want = "http://127.0.0.1:5555/v2/kelp/gale-recipes/hello/blobs/sha256:" + sha
	if got != want {
		t.Fatalf("verifyBlobURL (override) = %q, want %q", got, want)
	}
}

func TestVerifyArchiveDigest(t *testing.T) {
	dir := t.TempDir()
	content := []byte("gale archive bytes")
	path := filepath.Join(dir, "archive.tar.zst")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write temp archive: %v", err)
	}
	good := fmt.Sprintf("%x", sha256.Sum256(content))

	tests := []struct {
		name    string
		path    string
		wantSHA string
		wantErr string
	}{
		{
			name:    "match",
			path:    path,
			wantSHA: good,
			wantErr: "",
		},
		{
			name:    "mismatch",
			path:    path,
			wantSHA: strings.Repeat("0", 64),
			wantErr: "downloaded archive sha256 mismatch",
		},
		{
			name:    "missing file",
			path:    filepath.Join(dir, "nope.tar.zst"),
			wantSHA: good,
			wantErr: "hashing downloaded archive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := verifyArchiveDigest(tt.path, tt.wantSHA)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

// TestCheckPrebuilt pins that `gale verify` refuses a locked source
// artifact before it touches the network.
//
// Sigstore attestation covers a prebuilt binary published by
// gale-recipes CI. A source artifact's recorded hash is the output of
// a local build, so there is no GHCR blob behind it and no bundle to
// fetch: verification would fail after a token exchange and an HTTP
// round trip, with an error about a missing blob rather than about the
// thing that is actually wrong.
//
// The legacy schema recorded no method, so it cannot answer the
// question and keeps its existing behavior. Only the enforced schema
// makes the refusal possible.
func TestCheckPrebuilt(t *testing.T) {
	tests := []struct {
		name    string
		entry   lockfile.Entry
		wantErr bool
	}{
		{
			name:  "locked binary verifies",
			entry: lockfile.Entry{Version: "1.8.1-1", Method: "binary"},
		},
		{
			name:    "locked source is refused",
			entry:   lockfile.Entry{Version: "1.8.1-1", Method: "source"},
			wantErr: true,
		},
		{
			name:  "legacy entry records no method",
			entry: lockfile.Entry{Version: "1.8.1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkPrebuilt("jq", tt.entry)
			if tt.wantErr {
				if err == nil {
					t.Fatal("checkPrebuilt accepted a source artifact")
				}
				// The message has to name the package and say why, since
				// it is the whole output the user gets.
				for _, want := range []string{"jq", "source"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error does not mention %q: %v", want, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("checkPrebuilt: %v", err)
			}
		})
	}
}
