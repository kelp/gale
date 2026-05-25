package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestListRejectsExtraArgWithCleanError pins RO-K-6: `gale list
// nosuchpkg` (and friends) should produce the cobra "accepts 0
// arg(s), received N" message rather than the confusing
// "unknown command "nosuchpkg" for "gale list"" — these
// commands take no positional args at all, they don't have
// hidden subcommands.
func TestListRejectsExtraArgWithCleanError(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{"list", []string{"list", "nosuchpkg"}},
		{"doctor", []string{"doctor", "nosuchpkg"}},
		{"generations", []string{"generations", "nosuchpkg"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldArgs := rootCmd.Flags().Args()
			oldOut := rootCmd.OutOrStdout()
			oldErr := rootCmd.ErrOrStderr()
			t.Cleanup(func() {
				rootCmd.SetArgs(oldArgs)
				rootCmd.SetOut(oldOut)
				rootCmd.SetErr(oldErr)
			})

			var stderr bytes.Buffer
			rootCmd.SetErr(&stderr)
			rootCmd.SetOut(&stderr)
			rootCmd.SetArgs(tc.argv)

			err := executeRoot()
			if err == nil {
				t.Fatalf("expected error for %v", tc.argv)
			}
			msg := err.Error()
			if strings.Contains(msg, "unknown command") {
				t.Errorf("got cobra default 'unknown command' message; "+
					"expected an 'accepts ... arg(s)' shape: %v", msg)
			}
			// Positive shape: cobra's ExactArgs / MaximumNArgs
			// message always contains "accepts" and "received".
			if !strings.Contains(msg, "accepts") ||
				!strings.Contains(msg, "received") {
				t.Errorf("error %q does not look like an arg-count "+
					"complaint", msg)
			}
		})
	}
}
