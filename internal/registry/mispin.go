package registry

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// recorder is a concurrency-safe accumulator for package names.
// It records each name at most once (deduplication) and drains
// the full list on take(). Both record and take are safe for
// concurrent use from multiple goroutines.
type recorder struct {
	mu    sync.Mutex
	seen  map[string]bool
	names []string
}

// record adds name to the accumulator. Duplicate names are
// recorded once.
func (r *recorder) record(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = map[string]bool{}
	}
	if r.seen[name] {
		return
	}
	r.seen[name] = true
	r.names = append(r.names, name)
}

// take returns the accumulated names (sorted and deduped) and
// clears the recorded state. Returns nil when nothing accumulated.
func (r *recorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.names) == 0 {
		return nil
	}
	out := make([]string, len(r.names))
	copy(out, r.names)
	r.names = nil
	r.seen = map[string]bool{}
	sort.Strings(out)
	return out
}

// Package-level accumulators for mispinned and version-skewed
// packages. The dependency-closure resolver calls FetchRecipe
// from multiple goroutines, so all access is mutex-guarded via
// the recorder type.
var (
	mispins recorder
	skews   recorder
)

// recordMispin records that name took the ref-tip binary fallback
// because its pinned commit lacked the matching binary. Duplicate
// names are recorded once. Safe for concurrent use.
func recordMispin(name string) { mispins.record(name) }

// TakeMispinned returns the accumulated mispinned package names
// (sorted and deduped) and clears the recorded state. Returns
// nil/empty when nothing accumulated.
func TakeMispinned() []string { return mispins.take() }

// recordSkew records that name fell back to the main-tip recipe
// because its resolved-latest version had no binary. Duplicate
// names are recorded once. Safe for concurrent use.
func recordSkew(name string) { skews.record(name) }

// TakeSkewed returns the accumulated skewed package names (sorted
// and deduped) and clears the recorded state. A skew is distinct
// from a mispin: it fires when the resolved-latest version has no
// binary at its pinned commit AND none at ref-tip, forcing a fall
// back to the legacy main-tip recipe. Returns nil/empty when
// nothing accumulated.
func TakeSkewed() []string { return skews.take() }

// SkewSummary formats a one-line summary of skewed packages, or "" for
// empty input.
func SkewSummary(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"%d package(s) resolved to a .versions version with no "+
			"binary; installed main's shipped version instead: %s",
		len(names), strings.Join(names, ", "),
	)
}

// MispinSummary formats a one-line summary of mispinned packages, or
// "" for empty input.
func MispinSummary(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"%d package(s) have a mispinned .versions index; "+
			"using ref-tip binaries instead: %s",
		len(names), strings.Join(names, ", "),
	)
}
