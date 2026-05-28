package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestHookUseReflectsActualAcceptedArgs pins audit
// RO-J:help-text/0002: `gale hook --help` should not advertise
// `<shell>` as the placeholder when `direnv` is the only
// accepted value. The Use string drives both the help line
// and the synopsis.
func TestHookUseReflectsActualAcceptedArgs(t *testing.T) {
	if strings.Contains(hookCmd.Use, "<shell>") {
		t.Errorf(
			"hookCmd.Use = %q suggests any shell name works; "+
				"only `direnv` is accepted. Use a concrete "+
				"value (e.g. `hook direnv`) instead.",
			hookCmd.Use,
		)
	}
}

// TestHookRejectsInvalidShellAtCobraLayer pins the second
// half: an unsupported shell name should be rejected at the
// cobra layer (via OnlyValidArgs) rather than falling through
// to env.GenerateHook's generic "unsupported shell" error.
// Going through cobra means the user sees the accepted values
// in the diagnostic.
func TestHookRejectsInvalidShellAtCobraLayer(t *testing.T) {
	// cobra.MatchAll(ExactArgs(1), OnlyValidArgs) returns an
	// error from Args before RunE runs. Call Args directly so
	// the test doesn't depend on full cobra dispatch.
	if hookCmd.Args == nil {
		t.Fatal("hookCmd.Args is nil — no validation in place")
	}

	cases := []string{"bash", "zsh", "fish", "tcsh"}
	for _, shell := range cases {
		t.Run(shell, func(t *testing.T) {
			err := hookCmd.Args(hookCmd, []string{shell})
			if err == nil {
				t.Errorf(
					"hookCmd.Args accepted %q — expected "+
						"rejection via OnlyValidArgs",
					shell,
				)
			}
		})
	}

	// direnv must still be accepted.
	if err := hookCmd.Args(hookCmd, []string{"direnv"}); err != nil {
		t.Errorf("hookCmd.Args rejected direnv: %v", err)
	}
}

// guard: ensure the cobra dep stays linked for the test (the
// import is otherwise unused in older Go).
var _ = cobra.Command{}
