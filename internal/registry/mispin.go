package registry

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Package-level accumulator for packages whose .versions index
// mispins a commit, forcing a ref-tip binary fallback. This mirrors
// the timing package's package-level state pattern. The
// dependency-closure resolver calls FetchRecipe from multiple
// goroutines, so all access is mutex-guarded.
var (
	mispinMu    sync.Mutex
	mispinSeen  = map[string]bool{}
	mispinNames []string
)

// recordMispin records that name took the ref-tip binary fallback
// because its pinned commit lacked the matching binary. Duplicate
// names are recorded once. Safe for concurrent use.
func recordMispin(name string) {
	mispinMu.Lock()
	defer mispinMu.Unlock()
	if mispinSeen[name] {
		return
	}
	mispinSeen[name] = true
	mispinNames = append(mispinNames, name)
}

// TakeMispinned returns the accumulated mispinned package names
// (sorted and deduped) and clears the recorded state. Returns
// nil/empty when nothing accumulated.
func TakeMispinned() []string {
	mispinMu.Lock()
	defer mispinMu.Unlock()
	if len(mispinNames) == 0 {
		return nil
	}
	out := make([]string, len(mispinNames))
	copy(out, mispinNames)
	mispinNames = nil
	mispinSeen = map[string]bool{}
	sort.Strings(out)
	return out
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
