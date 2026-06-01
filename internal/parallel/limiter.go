package parallel

// Limiter bounds the number of concurrent holders. Acquire blocks
// until a slot is free; Release frees one. A nil *Limiter is an
// unbounded no-op, so call sites can opt out without nil checks.
type Limiter struct {
	slots chan struct{}
}

// NewLimiter returns a limiter that allows at most n concurrent
// holders.
func NewLimiter(n int) *Limiter {
	return &Limiter{}
}

// Acquire takes a slot, blocking until one is available.
func (l *Limiter) Acquire() {
	// STUB: no bounding yet (RED).
}

// Release returns a slot.
func (l *Limiter) Release() {
	// STUB: no bounding yet (RED).
}
