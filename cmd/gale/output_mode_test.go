package main

import (
	"testing"
)

func TestResolveOutputModeTTYAuto(t *testing.T) {
	mode := resolveOutputMode(outputModeInput{tty: true})

	if !mode.color {
		t.Error("color = false, want true for tty auto mode")
	}
	if !mode.steps {
		t.Error("steps = false, want true for tty auto mode")
	}
	if !mode.progress {
		t.Error("progress = false, want true for tty auto mode")
	}
	if mode.quiet {
		t.Error("quiet = true, want false for tty auto mode")
	}
	if mode.errorFormat != "text" {
		t.Errorf("errorFormat = %q, want text", mode.errorFormat)
	}
}

func TestResolveOutputModeNonTTYAuto(t *testing.T) {
	mode := resolveOutputMode(outputModeInput{tty: false})

	if mode.color {
		t.Error("color = true, want false for non-tty auto mode")
	}
	if mode.steps {
		t.Error("steps = true, want false for non-tty auto mode")
	}
	if mode.progress {
		t.Error("progress = true, want false for non-tty auto mode")
	}
}

func TestResolveOutputModePlainDisablesInteractiveOutput(t *testing.T) {
	mode := resolveOutputMode(outputModeInput{tty: true, plain: true})

	if mode.color {
		t.Error("color = true, want false in plain mode")
	}
	if mode.steps {
		t.Error("steps = true, want false in plain mode")
	}
	if mode.progress {
		t.Error("progress = true, want false in plain mode")
	}
}

func TestResolveOutputModeQuietSuppressesNonEssentialOutput(t *testing.T) {
	mode := resolveOutputMode(outputModeInput{tty: true, quiet: true})

	if !mode.quiet {
		t.Fatal("quiet = false, want true")
	}
	if mode.steps {
		t.Error("steps = true, want false in quiet mode")
	}
	if mode.progress {
		t.Error("progress = true, want false in quiet mode")
	}
}

func TestResolveOutputModeNoColorOverridesTTY(t *testing.T) {
	mode := resolveOutputMode(outputModeInput{tty: true, noColor: true})

	if mode.color {
		t.Error("color = true, want false when --no-color is set")
	}
}

func TestResolveOutputModeErrorFormatJSON(t *testing.T) {
	mode := resolveOutputMode(outputModeInput{tty: true, errorFormat: "json"})

	if mode.errorFormat != "json" {
		t.Errorf("errorFormat = %q, want json", mode.errorFormat)
	}
}

// TestVerboseFlagIsWiredToOutputMode verifies that the global
// --verbose / -v flag is consumed by currentOutputMode().
//
// Bug 0018: the verbose package-level var is declared in
// help.go but never passed to resolveOutputMode, so
// currentOutputMode() returns the same value regardless of
// whether --verbose was set.
//
// The fix must add a verbose field to outputModeInput, pass
// the verbose global through, and have resolveOutputMode
// return a distinct outputMode when verbose is true.
func TestVerboseFlagIsWiredToOutputMode(t *testing.T) {
	savedVerbose := verbose
	defer func() { verbose = savedVerbose }()

	verbose = false
	modeOff := currentOutputMode()

	verbose = true
	modeOn := currentOutputMode()

	if modeOff == modeOn {
		t.Error("currentOutputMode() returns the same value regardless of " +
			"the verbose flag — --verbose/-v must affect the output mode")
	}
}

// TestNoColorEnvDisablesColorOnTTY verifies that setting the
// NO_COLOR environment variable to a non-empty value disables
// color even on a TTY. Spec: https://no-color.org.
func TestNoColorEnvDisablesColorOnTTY(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	in := outputModeInput{tty: true}
	applyNoColorEnv(&in)
	mode := resolveOutputMode(in)
	if mode.color {
		t.Error("color = true, want false when NO_COLOR is set")
	}
}

// TestNoColorEnvEmptyIsIgnored verifies that an empty NO_COLOR
// does NOT disable color (per the no-color.org spec: only a
// non-empty value disables color).
func TestNoColorEnvEmptyIsIgnored(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	in := outputModeInput{tty: true}
	applyNoColorEnv(&in)
	mode := resolveOutputMode(in)
	if !mode.color {
		t.Error("color = false, want true when NO_COLOR is empty")
	}
}

// TestCurrentOutputModeReadsNoColorEnv verifies the end-to-end
// path: currentOutputMode() must honor NO_COLOR, not just the
// --no-color flag.
func TestCurrentOutputModeReadsNoColorEnv(t *testing.T) {
	savedNoColor := noColor
	defer func() { noColor = savedNoColor }()
	noColor = false

	t.Setenv("NO_COLOR", "1")
	mode := currentOutputMode()
	if mode.color {
		t.Error("currentOutputMode().color = true, want false when " +
			"NO_COLOR is set in the environment")
	}
}

// TestResolveOutputModeVerboseDistinguishable verifies that
// resolveOutputMode itself treats verbose=true differently
// from verbose=false, independent of the global package
// variable. This confirms the plumbing inside
// resolveOutputMode, not just currentOutputMode's call site.
func TestResolveOutputModeVerboseDistinguishable(t *testing.T) {
	off := resolveOutputMode(outputModeInput{tty: true, verbose: false})
	on := resolveOutputMode(outputModeInput{tty: true, verbose: true})

	if off == on {
		t.Error("resolveOutputMode with verbose=true must return a " +
			"different outputMode than verbose=false")
	}
}
