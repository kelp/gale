// Package timing emits phase-level elapsed-time lines when the
// global output sink has verbose mode enabled. Each call site
// looks like:
//
//	done := timing.Phase("ghcr-token")
//	defer done()
//
// With verbose off (or no output configured) calls are cheap
// no-ops. Output is one line per phase:
//
//	[timing] <label> elapsed=<duration>
package timing

import (
	"sync"
	"time"

	"github.com/kelp/gale/internal/output"
)

var (
	mu  sync.RWMutex
	out *output.Output
)

// SetOutput wires the package to an Output sink. Pass nil to
// disable. Subsystems already use this pattern (build.SetOutput,
// download.SetProgressEnabled); timing follows it so that
// cmd/gale/output_mode.go can configure all subsystems together.
func SetOutput(o *output.Output) {
	mu.Lock()
	out = o
	mu.Unlock()
}

// Phase starts a timer and returns a stop closure. Calling the
// closure emits one verbose line with elapsed time. Subsequent
// calls are no-ops, so callers can defer freely without worrying
// about double-emit.
func Phase(label string) func() {
	start := time.Now()
	var fired bool
	return func() {
		if fired {
			return
		}
		fired = true
		mu.RLock()
		o := out
		mu.RUnlock()
		if o == nil || !o.Verbose() {
			return
		}
		o.Verbosef("[timing] %s elapsed=%s", label, time.Since(start).Round(time.Millisecond))
	}
}
