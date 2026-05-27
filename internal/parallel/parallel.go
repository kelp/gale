// Package parallel runs a per-input function under a bounded
// worker pool and returns results in the original input order.
// Used by sync, outdated, and sbom to fan out per-package work
// (recipe fetch, install, version probe) instead of serialising
// the inner loop.
//
// Semantics:
//
//   - Results are returned at the same index as their input.
//   - Errors are positional, not aggregated — caller decides
//     what counts as fatal.
//   - One worker failing does NOT cancel peers. Callers that
//     want short-circuit behaviour cancel the supplied context.
//   - In-flight workers are not killed; they receive the
//     cancelled context via their fn argument.
//   - maxWorkers <= 0 is normalised to 1.
package parallel

import (
	"context"
	"sync"
)

// Map runs fn for each input concurrently, capped at maxWorkers
// in flight. Returns a result slice and an error slice, both
// indexed parallel to inputs.
func Map[T, R any](ctx context.Context, inputs []T, maxWorkers int, fn func(context.Context, T) (R, error)) ([]R, []error) {
	n := len(inputs)
	results := make([]R, n)
	errs := make([]error, n)
	if n == 0 {
		return results, errs
	}
	if maxWorkers < 1 {
		maxWorkers = 1
	}

	// Buffered semaphore controls concurrent worker count.
	sem := make(chan struct{}, maxWorkers)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range inputs {
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			results[idx], errs[idx] = fn(ctx, inputs[idx])
		}(i)
	}
	wg.Wait()
	return results, errs
}

// ForEach is Map for callers that don't need a result slice
// (e.g. installers that report success via side effects).
func ForEach[T any](ctx context.Context, inputs []T, maxWorkers int, fn func(context.Context, T) error) []error {
	_, errs := Map(ctx, inputs, maxWorkers, func(c context.Context, v T) (struct{}, error) {
		return struct{}{}, fn(c, v)
	})
	return errs
}
