package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestHelpWritesToStdout verifies that --help output goes to
// stdout, not stderr. Cobra's own source documents this:
// help text is not an error, so it belongs on stdout where
// users can pipe it into `less` or `grep`.
func TestHelpWritesToStdout(t *testing.T) {
	cmd := &cobra.Command{
		Use:   "fakecmd",
		Short: "fake test command",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}

	var stdout, stderr bytes.Buffer
	reset := addTempRootCommand(t, cmd)
	defer reset()
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"fakecmd", "--help"})

	if err := executeRoot(); err != nil {
		t.Fatalf("--help returned error: %v", err)
	}

	if stdout.Len() == 0 {
		t.Errorf("stdout is empty; --help output should land on stdout")
	}
	if strings.Contains(stderr.String(), "USAGE") {
		t.Errorf("stderr = %q, want help text on stdout not stderr",
			stderr.String())
	}
}

// TestHelpIncludesGlobalFlags verifies the custom help template
// surfaces persistent (global) flags from ancestor commands. The
// previous template only printed LocalFlags(), so every subcommand
// hid --no-color, --plain, --verbose, --quiet, --dry-run, and
// --error-format.
func TestHelpIncludesGlobalFlags(t *testing.T) {
	cmd := &cobra.Command{
		Use:   "fakecmd",
		Short: "fake test command",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}

	var stdout, stderr bytes.Buffer
	reset := addTempRootCommand(t, cmd)
	defer reset()
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"fakecmd", "--help"})

	if err := executeRoot(); err != nil {
		t.Fatalf("--help returned error: %v", err)
	}

	got := stdout.String() + stderr.String()
	for _, flag := range []string{
		"--no-color", "--plain", "--verbose",
		"--quiet", "--dry-run", "--error-format",
	} {
		if !strings.Contains(got, flag) {
			t.Errorf("help output missing global flag %q\nfull output:\n%s",
				flag, got)
		}
	}
}
