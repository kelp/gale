package main

import "testing"

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
