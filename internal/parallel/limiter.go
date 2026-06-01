package parallel

// Limiter bounds the number of concurrent holders. Acquire blocks
// until a slot is free; Release frees one. A nil *Limiter is an
// unbounded no-op, so call sites can opt out without nil checks.
type Limiter struct {
	sem chan struct{}
}

// NewLimiter returns a limiter that allows at most n concurrent
// holders. If n < 1 the limiter is unbounded: Acquire and Release
// are no-ops.
func NewLimiter(n int) *Limiter {
	if n < 1 {
		return &Limiter{}
	}
	return &Limiter{sem: make(chan struct{}, n)}
}

// Cap reports the configured concurrency ceiling. A nil or
// unbounded limiter reports 0 (no bound). Used by callers and
// tests that need to verify the limiter was sized as configured.
func (l *Limiter) Cap() int {
	if l == nil {
		return 0
	}
	return cap(l.sem)
}

// Acquire takes a slot, blocking until one is available. A nil
// limiter or an unbounded limiter returns immediately.
func (l *Limiter) Acquire() {
	if l == nil || l.sem == nil {
		return
	}
	l.sem <- struct{}{}
}

// Release returns a slot. A nil limiter or an unbounded limiter is
// a no-op.
func (l *Limiter) Release() {
	if l == nil || l.sem == nil {
		return
	}
	<-l.sem
}
