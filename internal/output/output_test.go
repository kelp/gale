package output

import (
	"bytes"
	"strings"
	"testing"
)

// methods is the table reused by all per-behavior tests below.
var methods = []struct {
	name string
	call func(*Output, string)
	ansi string // ANSI color code for color-enabled test
}{
	{"Info", (*Output).Info, "\033[36m"},
	{"Success", (*Output).Success, "\033[32m"},
	{"Warn", (*Output).Warn, "\033[33m"},
	{"Error", (*Output).Error, "\033[31m"},
}

// TestWritesToBuffer verifies each method produces at least some output.
func TestWritesToBuffer(t *testing.T) {
	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			var buf bytes.Buffer
			o := New(&buf, false)
			m.call(o, "hello")

			if got := buf.String(); got == "" {
				t.Fatal("expected output, got empty string")
			}
		})
	}
}

// TestContainsMessage verifies the message text appears in the output.
func TestContainsMessage(t *testing.T) {
	const msg = "the test message"
	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			var buf bytes.Buffer
			o := New(&buf, false)
			m.call(o, msg)

			got := buf.String()
			if !strings.Contains(got, msg) {
				t.Errorf("output = %q, want it to contain %q", got, msg)
			}
		})
	}
}

// TestHasColorWhenEnabled verifies the expected ANSI escape code appears
// when color is enabled.
func TestHasColorWhenEnabled(t *testing.T) {
	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			var buf bytes.Buffer
			o := New(&buf, true)
			m.call(o, "hello")

			got := buf.String()
			if !strings.Contains(got, m.ansi) {
				t.Errorf("output = %q, want ANSI code %q", got, m.ansi)
			}
		})
	}
}

// TestNoColorHasNoEscapeCodes verifies that when color is disabled no
// ANSI escape sequences appear in the output.
func TestNoColorHasNoEscapeCodes(t *testing.T) {
	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			var buf bytes.Buffer
			o := New(&buf, false)
			m.call(o, "hello")

			got := buf.String()
			if got == "" {
				t.Fatal("expected output, got empty string")
			}
			if strings.Contains(got, "\033[") {
				t.Errorf("output = %q, want no ANSI escape sequences "+
					"when color is disabled", got)
			}
		})
	}
}

// TestPlainTextEndsWithNewline verifies each method appends a trailing
// newline to its output.
func TestPlainTextEndsWithNewline(t *testing.T) {
	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			var buf bytes.Buffer
			o := New(&buf, false)
			m.call(o, "hello")

			got := buf.String()
			if !strings.HasSuffix(got, "\n") {
				t.Errorf("output = %q, want trailing newline", got)
			}
		})
	}
}

// All four message types should produce distinct output for the
// same message text, confirming each has a unique prefix.
func TestAllPrefixesAreDistinct(t *testing.T) {
	methods := []struct {
		name string
		call func(*Output, string)
	}{
		{"Info", (*Output).Info},
		{"Success", (*Output).Success},
		{"Warn", (*Output).Warn},
		{"Error", (*Output).Error},
	}

	outputs := make(map[string]string)
	for _, m := range methods {
		var buf bytes.Buffer
		o := New(&buf, false)
		m.call(o, "test")
		outputs[m.name] = buf.String()
	}

	for i, a := range methods {
		for _, b := range methods[i+1:] {
			if outputs[a.name] == outputs[b.name] {
				t.Errorf("%s and %s produced identical output %q",
					a.name, b.name, outputs[a.name])
			}
		}
	}
}

// All four message types with color=true should still produce
// distinct output, confirming colored prefixes differ.
func TestAllPrefixesAreDistinctWithColor(t *testing.T) {
	methods := []struct {
		name string
		call func(*Output, string)
	}{
		{"Info", (*Output).Info},
		{"Success", (*Output).Success},
		{"Warn", (*Output).Warn},
		{"Error", (*Output).Error},
	}

	outputs := make(map[string]string)
	for _, m := range methods {
		var buf bytes.Buffer
		o := New(&buf, true)
		m.call(o, "test")
		outputs[m.name] = buf.String()
	}

	for i, a := range methods {
		for _, b := range methods[i+1:] {
			if outputs[a.name] == outputs[b.name] {
				t.Errorf("%s and %s produced identical output %q",
					a.name, b.name, outputs[a.name])
			}
		}
	}
}

func TestStepCanBeDisabled(t *testing.T) {
	var buf bytes.Buffer
	o := NewWithOptions(&buf, Options{Steps: false})
	o.Step("hidden")

	if got := buf.String(); got != "" {
		t.Errorf("output = %q, want empty when steps are disabled", got)
	}
}

func TestInfoSuppressedInQuietMode(t *testing.T) {
	var buf bytes.Buffer
	o := NewWithOptions(&buf, Options{Quiet: true})
	o.Info("hello")

	if got := buf.String(); got != "" {
		t.Errorf("output = %q, want empty in quiet mode", got)
	}
}

func TestSuccessSuppressedInQuietMode(t *testing.T) {
	var buf bytes.Buffer
	o := NewWithOptions(&buf, Options{Quiet: true})
	o.Success("done")

	if got := buf.String(); got != "" {
		t.Errorf("output = %q, want empty in quiet mode", got)
	}
}

func TestWarnStillPrintsInQuietMode(t *testing.T) {
	var buf bytes.Buffer
	o := NewWithOptions(&buf, Options{Quiet: true})
	o.Warn("careful")

	if got := buf.String(); !strings.Contains(got, "careful") {
		t.Errorf("output = %q, want warning text in quiet mode", got)
	}
}

func TestErrorStillPrintsInQuietMode(t *testing.T) {
	var buf bytes.Buffer
	o := NewWithOptions(&buf, Options{Quiet: true})
	o.Error("failed")

	if got := buf.String(); !strings.Contains(got, "failed") {
		t.Errorf("output = %q, want error text in quiet mode", got)
	}
}

// --- Behavior: verbose-only output ---
// Verbosef is the channel for diagnostic detail (timing, phase
// breakdowns, low-level fetch info) that should only appear when
// --verbose is on. Default-off keeps normal runs uncluttered.

func TestVerboseSilentByDefault(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf, false)
	o.Verbosef("phase=%s elapsed=%dms", "fetch", 42)

	if got := buf.String(); got != "" {
		t.Errorf("output = %q, want empty when verbose is off", got)
	}
}

func TestVerboseWritesWhenEnabled(t *testing.T) {
	var buf bytes.Buffer
	o := NewWithOptions(&buf, Options{Verbose: true})
	o.Verbosef("phase=%s elapsed=%dms", "fetch", 42)

	got := buf.String()
	if !strings.Contains(got, "phase=fetch") {
		t.Errorf("output = %q, want formatted message", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("output = %q, want trailing newline", got)
	}
}

func TestVerboseSuppressedInQuietMode(t *testing.T) {
	var buf bytes.Buffer
	o := NewWithOptions(&buf, Options{Verbose: true, Quiet: true})
	o.Verbosef("phase=%s", "fetch")

	if got := buf.String(); got != "" {
		t.Errorf("output = %q, want empty when quiet is on, even with verbose", got)
	}
}

func TestVerboseGetterReflectsOption(t *testing.T) {
	off := NewWithOptions(&bytes.Buffer{}, Options{})
	if off.Verbose() {
		t.Error("Verbose() = true on default Output, want false")
	}
	on := NewWithOptions(&bytes.Buffer{}, Options{Verbose: true})
	if !on.Verbose() {
		t.Error("Verbose() = false after Options{Verbose: true}, want true")
	}
}
