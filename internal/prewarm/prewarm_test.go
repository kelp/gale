package prewarm_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/prewarm"
	"github.com/kelp/gale/internal/recipe"
)

// TestPrewarmCallsResolverForEachDep verifies that PrewarmRecipeDeps
// calls the resolver once for every dep in the slice.
func TestPrewarmCallsResolverForEachDep(t *testing.T) {
	t.Parallel()

	var counter atomic.Int32
	resolver := installer.RecipeResolver(func(name string) (*recipe.Recipe, error) {
		counter.Add(1)
		return nil, nil
	})

	deps := []string{"a", "b", "c"}
	prewarm.PrewarmRecipeDeps(context.Background(), deps, resolver)

	got := counter.Load()
	if got != 3 {
		t.Errorf("resolver called %d times; want 3", got)
	}
}

// TestPrewarmRunsConcurrently verifies that PrewarmRecipeDeps resolves
// deps in parallel: the peak in-flight count must reach at least 2
// when all resolvers sleep briefly.
func TestPrewarmRunsConcurrently(t *testing.T) {
	t.Parallel()

	var inFlight atomic.Int32
	var peak atomic.Int32

	resolver := installer.RecipeResolver(func(name string) (*recipe.Recipe, error) {
		current := inFlight.Add(1)
		// Update peak with a compare-and-swap loop.
		for {
			p := peak.Load()
			if current <= p {
				break
			}
			if peak.CompareAndSwap(p, current) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		inFlight.Add(-1)
		return nil, nil
	})

	deps := []string{"w", "x", "y", "z"}
	prewarm.PrewarmRecipeDeps(context.Background(), deps, resolver)

	got := peak.Load()
	if got < 2 {
		t.Errorf("peak in-flight goroutines = %d; want >= 2 (serial execution detected)", got)
	}
}

// TestPrewarmSwallowsResolverErrors verifies that PrewarmRecipeDeps
// calls the resolver for every dep even when the resolver always
// returns an error, and that it does not panic or propagate errors.
func TestPrewarmSwallowsResolverErrors(t *testing.T) {
	t.Parallel()

	var counter atomic.Int32
	resolver := installer.RecipeResolver(func(name string) (*recipe.Recipe, error) {
		counter.Add(1)
		return nil, fmt.Errorf("boom")
	})

	deps := []string{"x", "y", "z"}
	// Must not panic; reaching the assertion below proves it returned normally.
	prewarm.PrewarmRecipeDeps(context.Background(), deps, resolver)

	got := counter.Load()
	if got != 3 {
		t.Errorf("resolver called %d times despite errors; want 3", got)
	}
}
