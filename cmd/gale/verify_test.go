package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
