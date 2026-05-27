package timing

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/kelp/gale/internal/output"
)

func setTestOutput(t *testing.T, verbose bool) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	o := output.NewWithOptions(&buf, output.Options{Verbose: verbose})
	SetOutput(o)
	t.Cleanup(func() { SetOutput(nil) })
	return &buf
}

func TestPhaseSilentWhenNoOutput(t *testing.T) {
	SetOutput(nil)
	defer SetOutput(nil)

	done := Phase("anything")
	done() // must not panic
}

func TestPhaseSilentWhenVerboseOff(t *testing.T) {
	buf := setTestOutput(t, false)

	done := Phase("fetch")
	done()

	if got := buf.String(); got != "" {
		t.Errorf("output = %q, want empty when verbose off", got)
	}
}

func TestPhaseEmitsLineWhenVerbose(t *testing.T) {
	buf := setTestOutput(t, true)

	done := Phase("ghcr-token")
	done()

	got := buf.String()
	if !strings.Contains(got, "[timing]") {
		t.Errorf("output = %q, want [timing] prefix", got)
	}
	if !strings.Contains(got, "ghcr-token") {
		t.Errorf("output = %q, want phase label", got)
	}
	if !strings.Contains(got, "elapsed=") {
		t.Errorf("output = %q, want elapsed= field", got)
	}
}

func TestPhaseRecordsNonZeroElapsed(t *testing.T) {
	buf := setTestOutput(t, true)

	done := Phase("sleepy")
	time.Sleep(5 * time.Millisecond)
	done()

	got := buf.String()
	if strings.Contains(got, "elapsed=0ms") {
		t.Errorf("output = %q, want non-zero elapsed", got)
	}
}

func TestPhaseDoneIsIdempotent(t *testing.T) {
	buf := setTestOutput(t, true)

	done := Phase("once")
	done()
	done() // second call should be no-op

	got := buf.String()
	if n := strings.Count(got, "[timing]"); n != 1 {
		t.Errorf("got %d [timing] lines, want 1", n)
	}
}
