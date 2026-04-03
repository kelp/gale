package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
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

func TestFormatDevVersion(t *testing.T) {
	tests := []struct {
		name     string
		describe string
		want     string
	}{
		{
			"on tag",
			"v0.2.0",
			"0.2.0",
		},
		{
			"commits ahead of tag",
			"v0.2.0-7-g5395b8f",
			"0.2.0-dev.7+5395b8f",
		},
		{
			"no tags, bare hash",
			"5395b8f",
			"0.0.0-dev+5395b8f",
		},
		{
			"one commit ahead",
			"v1.0.0-1-gabcdef0",
			"1.0.0-dev.1+abcdef0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDevVersion(tt.describe)
			if got != tt.want {
				t.Errorf("formatDevVersion(%q) = %q, want %q",
					tt.describe, got, tt.want)
			}
		})
	}
}

func TestRecipesFlagReplacesLocal(t *testing.T) {
	cmds := map[string]*cobra.Command{
		"install":  installCmd,
		"add":      addCmd,
		"update":   updateCmd,
		"sync":     syncCmd,
		"outdated": outdatedCmd,
	}

	for name, cmd := range cmds {
		t.Run(name, func(t *testing.T) {
			// --recipes must exist.
			f := cmd.Flags().Lookup("recipes")
			if f == nil {
				t.Fatalf("%s: --recipes flag not found", name)
			}
			if f.DefValue != "" {
				t.Errorf("%s: --recipes default = %q, want empty",
					name, f.DefValue)
			}
			if f.NoOptDefVal != "auto" {
				t.Errorf("%s: --recipes NoOptDefVal = %q, want %q",
					name, f.NoOptDefVal, "auto")
			}

			// --local must not exist.
			if cmd.Flags().Lookup("local") != nil {
				t.Errorf("%s: --local flag should not exist",
					name)
			}
		})
	}
}

func TestPathFlagReplacesSource(t *testing.T) {
	cmds := map[string]*cobra.Command{
		"install": installCmd,
		"update":  updateCmd,
	}

	for name, cmd := range cmds {
		t.Run(name, func(t *testing.T) {
			if cmd.Flags().Lookup("path") == nil {
				t.Errorf("%s: --path flag not found", name)
			}
			if cmd.Flags().Lookup("source") != nil {
				t.Errorf(
					"%s: --source flag should not exist",
					name)
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
