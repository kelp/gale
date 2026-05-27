package parallel

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Map preserves input order in the results slice, even when
// workers finish in a different order.
func TestMapResultsInInputOrder(t *testing.T) {
	inputs := []int{5, 1, 3, 4, 2}
	results, errs := Map(context.Background(), inputs, 5, func(_ context.Context, n int) (int, error) {
		// Sleep inversely to n so smaller numbers complete later.
		time.Sleep(time.Duration(10-n) * time.Millisecond)
		return n * 10, nil
	})
	for i, e := range errs {
		if e != nil {
			t.Fatalf("errs[%d] = %v, want nil", i, e)
		}
	}
	want := []int{50, 10, 30, 40, 20}
	if len(results) != len(want) {
		t.Fatalf("len(results) = %d, want %d", len(results), len(want))
	}
	for i, got := range results {
		if got != want[i] {
			t.Errorf("results[%d] = %d, want %d", i, got, want[i])
		}
	}
}

// maxWorkers caps in-flight concurrency. With workers=2 and 10
// inputs sleeping 30ms each, the run takes ~150ms (5 batches of
// 2), not ~30ms (one batch of 10).
func TestMapRespectsMaxWorkers(t *testing.T) {
	var inFlight, peak atomic.Int64
	const workers = 2
	inputs := make([]int, 10)
	for i := range inputs {
		inputs[i] = i
	}
	_, _ = Map(context.Background(), inputs, workers, func(_ context.Context, _ int) (int, error) {
		cur := inFlight.Add(1)
		// Track running peak.
		for {
			p := peak.Load()
			if cur <= p || peak.CompareAndSwap(p, cur) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		inFlight.Add(-1)
		return 0, nil
	})
	if peak.Load() > int64(workers) {
		t.Errorf("peak in-flight = %d, want <= %d", peak.Load(), workers)
	}
}

// Errors are returned per-input at the matching index — a
// failure on inputs[2] surfaces as errs[2], everything else nil.
func TestMapErrorsPositional(t *testing.T) {
	sentinel := errors.New("boom")
	inputs := []int{1, 2, 3, 4}
	_, errs := Map(context.Background(), inputs, 4, func(_ context.Context, n int) (int, error) {
		if n == 3 {
			return 0, sentinel
		}
		return n, nil
	})
	if len(errs) != 4 {
		t.Fatalf("len(errs) = %d, want 4", len(errs))
	}
	if errs[0] != nil || errs[1] != nil || errs[3] != nil {
		t.Errorf("non-failing inputs returned errors: %v", errs)
	}
	if !errors.Is(errs[2], sentinel) {
		t.Errorf("errs[2] = %v, want sentinel", errs[2])
	}
}

// One worker fails — other workers continue. Sync depends on
// this: a single recipe failure must not block the rest.
func TestMapDoesNotShortCircuitOnError(t *testing.T) {
	var completed atomic.Int64
	inputs := []int{1, 2, 3, 4, 5}
	_, _ = Map(context.Background(), inputs, 5, func(_ context.Context, n int) (int, error) {
		completed.Add(1)
		if n == 2 {
			return 0, errors.New("boom")
		}
		return n, nil
	})
	if completed.Load() != 5 {
		t.Errorf("completed = %d, want 5 (no short-circuit)", completed.Load())
	}
}

// Empty input is a valid no-op.
func TestMapEmptyInputs(t *testing.T) {
	results, errs := Map(context.Background(), []int{}, 4, func(_ context.Context, n int) (int, error) {
		return n, nil
	})
	if len(results) != 0 || len(errs) != 0 {
		t.Errorf("non-empty output for empty input: results=%v errs=%v", results, errs)
	}
}

// maxWorkers <= 0 is normalised to 1 (avoids deadlock on caller
// typo). Spec is "at least one worker."
func TestMapNonPositiveWorkersDefaultsToOne(t *testing.T) {
	var maxObserved atomic.Int64
	var inFlight atomic.Int64
	inputs := []int{1, 2, 3}
	_, _ = Map(context.Background(), inputs, 0, func(_ context.Context, _ int) (int, error) {
		cur := inFlight.Add(1)
		for {
			p := maxObserved.Load()
			if cur <= p || maxObserved.CompareAndSwap(p, cur) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		inFlight.Add(-1)
		return 0, nil
	})
	if maxObserved.Load() != 1 {
		t.Errorf("with workers=0, peak in-flight = %d, want 1", maxObserved.Load())
	}
}

// Context cancellation propagates: in-flight workers receive a
// cancelled ctx in their fn argument; already-completed work
// keeps its result. We don't kill in-flight goroutines — they
// respect ctx themselves.
func TestMapPropagatesContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	inputs := []int{1, 2, 3, 4, 5}

	var sawCancellation atomic.Bool
	go func() {
		defer wg.Done()
		_, _ = Map(ctx, inputs, 2, func(c context.Context, n int) (int, error) {
			// Workers block until cancelled.
			<-c.Done()
			sawCancellation.Store(true)
			return n, c.Err()
		})
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	wg.Wait()

	if !sawCancellation.Load() {
		t.Error("workers did not receive cancellation via ctx")
	}
}

// Reporting helper: ForEach is Map with a side-effect fn; useful
// when callers don't need a result slice. Keeps the call sites
// in sync/outdated/sbom from carrying a dummy result type.
func TestForEachRunsAllAndReturnsErrors(t *testing.T) {
	var counter atomic.Int64
	inputs := []string{"a", "b", "c"}
	errs := ForEach(context.Background(), inputs, 3, func(_ context.Context, s string) error {
		counter.Add(1)
		if s == "b" {
			return fmt.Errorf("b failed")
		}
		return nil
	})
	if counter.Load() != 3 {
		t.Errorf("counter = %d, want 3", counter.Load())
	}
	if errs[0] != nil || errs[2] != nil {
		t.Errorf("unexpected errors at 0 or 2: %v", errs)
	}
	if errs[1] == nil {
		t.Errorf("errs[1] = nil, want error")
	}
}
