package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ver "github.com/kelp/gale/internal/version"
)

func TestIsGitHash(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"abc1234", true},
		{"abcdef0", true},
		{"1234567", true},
		{"1.7.1", false},
		{"0.3.0", false},
		{"v2.0.0", false},
		{"abc123", false},
		{"abcdefgh", false},
		{"abc1234z", false},
		{"abcdef01234", true},
		{"abc12345678", true},
		{"", false},
		{"abc", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isGitHash(tt.input)
			if got != tt.want {
				t.Errorf(
					"isGitHash(%q) = %v, want %v",
					tt.input, got, tt.want,
				)
			}
		})
	}
}

func TestVersionIsNewer(t *testing.T) {
	tests := []struct {
		current   string
		candidate string
		want      bool
	}{
		{"0.2.0", "0.8.1", true},
		{"1.0.0", "1.0.1", true},
		{"1.0.0", "2.0.0", true},
		{"0.8.1", "0.2.0", false},
		{"2.0.0", "1.0.0", false},
		{"1.0.1", "1.0.0", false},
		{"0.8.1", "0.8.1", false},
		{"0.8.1-dev.2+47a65de", "0.8.1", true},
		{"0.8.1-dev.2", "0.8.1", true},
		{"0.8.1", "0.8.1-dev.2", false},
		{"0.8.1", "0.9.0-dev.1", true},
		{"0.8.2-dev.1", "0.8.1", false},
		{"abc1234", "0.8.1", true},
		{"0.8.1", "abc1234", true},
		{"abc1234", "def5678", true},
		{"1.2.3", "1.2.3-2", true},
		{"1.2.3-2", "1.2.3", false},
		{"1.2.3-2", "1.2.3-3", true},
		{"1.2.3-3", "1.2.3-2", false},
		{"1.2.3-2", "1.2.3-2", false},
		{"1.2.3-2", "1.2.4", true},
	}

	for _, tt := range tests {
		t.Run(tt.current+"→"+tt.candidate, func(t *testing.T) {
			got := ver.IsNewer(tt.candidate, tt.current)
			if got != tt.want {
				t.Errorf(
					"ver.IsNewer(%q, %q) = %v, want %v",
					tt.candidate, tt.current, got, tt.want,
				)
			}
		})
	}
}

func TestUpdateHasScopeFlags(t *testing.T) {
	if updateCmd.Flags().Lookup("global") == nil {
		t.Error("update is missing --global/-g flag")
	}
	if updateCmd.Flags().Lookup("project") == nil {
		t.Error("update is missing --project/-p flag")
	}
}

func TestUpdatePathFlagDescriptionDoesNotSayRebuild(t *testing.T) {
	if updateCmd.Flags().Lookup("path") != nil {
		t.Fatal("update --path must be gone")
	}
}

func TestUpdateIgnoresLeftoverPinned(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  jq = \"1.7.0\"\n\n[pinned]\n  jq = true\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	if err := os.Chdir(projDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	dryRun = true
	t.Cleanup(func() { dryRun = false })

	for _, name := range []string{"named", "bare"} {
		t.Run(name, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			oldStderr := os.Stderr
			os.Stderr = w
			t.Cleanup(func() { os.Stderr = oldStderr })

			args := []string{"jq"}
			if name == "bare" {
				args = nil
			}
			errCh := make(chan error, 1)
			go func() {
				errCh <- updateCmd.RunE(updateCmd, args)
				_ = w.Close()
			}()

			var stderr bytes.Buffer
			if _, copyErr := stderr.ReadFrom(r); copyErr != nil {
				t.Fatal(copyErr)
			}
			if err := <-errCh; err != nil {
				t.Fatalf("update: %v", err)
			}
			out := stderr.String()
			for _, sub := range []string{
				"skipping jq (pinned)",
				"gale unpin jq",
				"No packages to update",
			} {
				if strings.Contains(out, sub) {
					t.Errorf("leftover [pinned] must not skip update; stderr %q has %q", out, sub)
				}
			}
			if !strings.Contains(out, "update jq") {
				t.Errorf("want dry-run update jq, got %q", out)
			}
		})
	}
}
