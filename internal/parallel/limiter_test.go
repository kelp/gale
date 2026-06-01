package parallel

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A Limiter caps the number of concurrent holders. With NewLimiter(2)
// and 30 goroutines contending, peak in-flight must never exceed 2,
// and every goroutine must run.
func TestLimiterBoundsConcurrency(t *testing.T) {
	const limit = 2
	const goroutines = 30

	l := NewLimiter(limit)

	var inFlight, peak, ran atomic.Int64
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			l.Acquire()
			defer l.Release()

			ran.Add(1)
			cur := inFlight.Add(1)
			for {
				p := peak.Load()
				if cur <= p || peak.CompareAndSwap(p, cur) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			inFlight.Add(-1)
		}()
	}
	wg.Wait()

	if peak.Load() > int64(limit) {
		t.Errorf("peak in-flight = %d, want <= %d", peak.Load(), limit)
	}
	if ran.Load() != int64(goroutines) {
		t.Errorf("ran = %d, want %d", ran.Load(), goroutines)
	}
}
