package output

import (
	"bytes"
	"strings"
	"testing"
)

// --- Behavior 1: Info message ---

func TestInfoWritesToBuffer(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf, false)
	o.Info("hello")

	got := buf.String()
	if got == "" {
		t.Fatal("expected output, got empty string")
	}
}

func TestInfoContainsMessage(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf, false)
	o.Info("starting build")

	got := buf.String()
	if !strings.Contains(got, "starting build") {
		t.Errorf("output = %q, want it to contain %q",
			got, "starting build")
	}
}

// Info uses cyan (\033[36m) — chosen over blue (\033[34m) because
// cyan is more readable on dark terminal backgrounds.
func TestInfoHasColorWhenEnabled(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf, true)
	o.Info("hello")

	got := buf.String()
	if !strings.Contains(got, "\033[36m") {
		t.Errorf("output = %q, want cyan ANSI code \\033[36m", got)
	}
}

func TestInfoHasDistinctPrefix(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf, false)
	o.Info("hello")

	got := buf.String()
	// Info prefix must appear before the message text.
	msgIdx := strings.Index(got, "hello")
	if msgIdx < 1 {
		t.Errorf("output = %q, want a prefix before the message",
			got)
	}
}

// --- Behavior 2: Success message ---

func TestSuccessWritesToBuffer(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf, false)
	o.Success("done")

	got := buf.String()
	if got == "" {
		t.Fatal("expected output, got empty string")
	}
}

func TestSuccessContainsMessage(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf, false)
	o.Success("package installed")

	got := buf.String()
	if !strings.Contains(got, "package installed") {
		t.Errorf("output = %q, want it to contain %q",
			got, "package installed")
	}
}

func TestSuccessHasColorWhenEnabled(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf, true)
	o.Success("done")

	got := buf.String()
	if !strings.Contains(got, "\033[32m") {
		t.Errorf("output = %q, want green ANSI code \\033[32m", got)
	}
}

func TestSuccessPrefixDiffersFromInfo(t *testing.T) {
	var infoBuf, successBuf bytes.Buffer

	infoOut := New(&infoBuf, false)
	infoOut.Info("msg")

	successOut := New(&successBuf, false)
	successOut.Success("msg")

	infoPrefix := strings.TrimSuffix(infoBuf.String(), "msg\n")
	successPrefix := strings.TrimSuffix(successBuf.String(), "msg\n")

	if infoPrefix == successPrefix {
		t.Errorf("Info and Success should have different prefixes, "+
			"both got %q", infoPrefix)
	}
}

// --- Behavior 3: Warning message ---

func TestWarnWritesToBuffer(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf, false)
	o.Warn("caution")

	got := buf.String()
	if got == "" {
		t.Fatal("expected output, got empty string")
	}
}

func TestWarnContainsMessage(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf, false)
	o.Warn("deprecated feature")

	got := buf.String()
	if !strings.Contains(got, "deprecated feature") {
		t.Errorf("output = %q, want it to contain %q",
			got, "deprecated feature")
	}
}

func TestWarnHasColorWhenEnabled(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf, true)
	o.Warn("caution")

	got := buf.String()
	if !strings.Contains(got, "\033[33m") {
		t.Errorf("output = %q, want yellow ANSI code \\033[33m", got)
	}
}

func TestWarnPrefixDiffersFromSuccess(t *testing.T) {
	var warnBuf, successBuf bytes.Buffer

	warnOut := New(&warnBuf, false)
	warnOut.Warn("msg")

	successOut := New(&successBuf, false)
	successOut.Success("msg")

	warnPrefix := strings.TrimSuffix(warnBuf.String(), "msg\n")
	successPrefix := strings.TrimSuffix(successBuf.String(), "msg\n")

	if warnPrefix == successPrefix {
		t.Errorf("Warn and Success should have different prefixes, "+
			"both got %q", warnPrefix)
	}
}

// --- Behavior 4: Error message ---

func TestErrorWritesToBuffer(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf, false)
	o.Error("failed")

	got := buf.String()
	if got == "" {
		t.Fatal("expected output, got empty string")
	}
}

func TestErrorContainsMessage(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf, false)
	o.Error("connection refused")

	got := buf.String()
	if !strings.Contains(got, "connection refused") {
		t.Errorf("output = %q, want it to contain %q",
			got, "connection refused")
	}
}

func TestErrorHasColorWhenEnabled(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf, true)
	o.Error("failed")

	got := buf.String()
	if !strings.Contains(got, "\033[31m") {
		t.Errorf("output = %q, want red ANSI code \\033[31m", got)
	}
}

func TestErrorPrefixDiffersFromWarn(t *testing.T) {
	var errBuf, warnBuf bytes.Buffer

	errOut := New(&errBuf, false)
	errOut.Error("msg")

	warnOut := New(&warnBuf, false)
	warnOut.Warn("msg")

	errPrefix := strings.TrimSuffix(errBuf.String(), "msg\n")
	warnPrefix := strings.TrimSuffix(warnBuf.String(), "msg\n")

	if errPrefix == warnPrefix {
		t.Errorf("Error and Warn should have different prefixes, "+
			"both got %q", errPrefix)
	}
}

// --- Behavior 5: NO_COLOR support ---
// When color=false, output should contain no ANSI escape codes.

func TestInfoNoColorHasNoEscapeCodes(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf, false)
	o.Info("hello")

	got := buf.String()
	if got == "" {
		t.Fatal("expected output, got empty string")
	}
	if strings.Contains(got, "\033[") {
		t.Errorf("output = %q, want no ANSI escape sequences "+
			"when color is disabled", got)
	}
}

func TestSuccessNoColorHasNoEscapeCodes(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf, false)
	o.Success("done")

	got := buf.String()
	if got == "" {
		t.Fatal("expected output, got empty string")
	}
	if strings.Contains(got, "\033[") {
		t.Errorf("output = %q, want no ANSI escape sequences "+
			"when color is disabled", got)
	}
}

func TestWarnNoColorHasNoEscapeCodes(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf, false)
	o.Warn("caution")

	got := buf.String()
	if got == "" {
		t.Fatal("expected output, got empty string")
	}
	if strings.Contains(got, "\033[") {
		t.Errorf("output = %q, want no ANSI escape sequences "+
			"when color is disabled", got)
	}
}

func TestErrorNoColorHasNoEscapeCodes(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf, false)
	o.Error("failed")

	got := buf.String()
	if got == "" {
		t.Fatal("expected output, got empty string")
	}
	if strings.Contains(got, "\033[") {
		t.Errorf("output = %q, want no ANSI escape sequences "+
			"when color is disabled", got)
	}
}

// --- Behavior 6: Plain text in non-TTY ---
// Writing to a bytes.Buffer (non-TTY) with color=false produces
// plain text. Each method should still include a prefix and
// the message, with a trailing newline.

func TestInfoPlainTextEndsWithNewline(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf, false)
	o.Info("hello")

	got := buf.String()
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("output = %q, want trailing newline", got)
	}
}

func TestSuccessPlainTextEndsWithNewline(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf, false)
	o.Success("done")

	got := buf.String()
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("output = %q, want trailing newline", got)
	}
}

func TestWarnPlainTextEndsWithNewline(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf, false)
	o.Warn("caution")

	got := buf.String()
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("output = %q, want trailing newline", got)
	}
}

func TestErrorPlainTextEndsWithNewline(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf, false)
	o.Error("failed")

	got := buf.String()
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("output = %q, want trailing newline", got)
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
