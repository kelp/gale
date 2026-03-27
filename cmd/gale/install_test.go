package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePackageArg(t *testing.T) {
	tests := []struct {
		input       string
		wantName    string
		wantVersion string
	}{
		{"jq", "jq", ""},
		{"python@3.11", "python", "3.11"},
		{"node@20", "node", "20"},
		{"ripgrep@latest", "ripgrep", "latest"},
		{"@invalid", "@invalid", ""},
	}

	for _, tt := range tests {
		name, version := parsePackageArg(tt.input)
		if name != tt.wantName {
			t.Errorf("parsePackageArg(%q) name = %q, want %q",
				tt.input, name, tt.wantName)
		}
		if version != tt.wantVersion {
			t.Errorf("parsePackageArg(%q) version = %q, want %q",
				tt.input, version, tt.wantVersion)
		}
	}
}

func TestCheckVersionMatch(t *testing.T) {
	tests := []struct {
		name        string
		requested   string
		actual      string
		wantErr     bool
		errContains string
	}{
		{"empty requested uses recipe version", "", "1.7.1", false, ""},
		{"latest uses recipe version", "latest", "1.7.1", false, ""},
		{"matching version passes", "1.7.1", "1.7.1", false, ""},
		{
			"mismatched version errors", "2.0.0", "1.7.1",
			true, "version mismatch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkVersionMatch(tt.requested, tt.actual)
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.errContains != "" && err != nil &&
				!strings.Contains(err.Error(), tt.errContains) {
				t.Errorf("error %q should contain %q",
					err.Error(), tt.errContains)
			}
		})
	}
}

func TestValidateInstallFlags(t *testing.T) {
	tests := []struct {
		name    string
		global  bool
		project bool
		wantErr bool
	}{
		{"neither flag is ok", false, false, false},
		{"global only is ok", true, false, false},
		{"project only is ok", false, true, false},
		{"both flags errors", true, true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateInstallFlags(tt.global, tt.project)
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestResolveScope(t *testing.T) {
	// Create a temp dir with a gale.toml for project detection.
	tmp := t.TempDir()
	galePath := filepath.Join(tmp, "gale.toml")
	if err := os.WriteFile(galePath, []byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		global     bool
		project    bool
		cwd        string
		isTTY      bool
		input      string
		wantGlobal bool
	}{
		{
			"-g flag forces global",
			true, false, tmp, false, "", true,
		},
		{
			"-p flag forces project",
			false, true, tmp, false, "", false,
		},
		{
			"no flags no gale.toml defaults global",
			false, false, t.TempDir(), false, "", true,
		},
		{
			"no flags with gale.toml non-TTY defaults global",
			false, false, tmp, false, "", true,
		},
		{
			"TTY prompt answer g means global",
			false, false, tmp, true, "g\n", true,
		},
		{
			"TTY prompt answer p means project",
			false, false, tmp, true, "p\n", false,
		},
		{
			"TTY prompt empty defaults global",
			false, false, tmp, true, "\n", true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var reader io.Reader
			if tt.input != "" {
				reader = strings.NewReader(tt.input)
			}
			got := resolveScope(tt.global, tt.project,
				tt.cwd, tt.isTTY, reader)
			if got != tt.wantGlobal {
				t.Errorf("resolveScope() = %v, want %v",
					got, tt.wantGlobal)
			}
		})
	}
}
