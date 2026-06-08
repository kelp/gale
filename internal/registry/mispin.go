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

// Package-level accumulator for version-skewed packages, drained
// independently from the mispin accumulator. A skew fires when the
// resolved-latest version has no binary at its pinned commit AND none
// at ref-tip, forcing a fall back to the legacy main-tip recipe.
var (
	skewMu    sync.Mutex
	skewSeen  = map[string]bool{}
	skewNames []string
)

// recordSkew records that name fell back to the main-tip recipe because
// its resolved-latest version had no binary. Duplicate names are
// recorded once. Safe for concurrent use.
func recordSkew(name string) {
	skewMu.Lock()
	defer skewMu.Unlock()
	if skewSeen[name] {
		return
	}
	skewSeen[name] = true
	skewNames = append(skewNames, name)
}

// TakeSkewed returns the accumulated skewed package names (sorted and
// deduped) and clears the recorded state. A skew is distinct from a
// mispin: it fires when the resolved-latest version has no binary at
// its pinned commit AND none at ref-tip, forcing a fall back to the
// legacy main-tip recipe. Returns nil/empty when nothing accumulated.
func TakeSkewed() []string {
	skewMu.Lock()
	defer skewMu.Unlock()
	if len(skewNames) == 0 {
		return nil
	}
	out := make([]string, len(skewNames))
	copy(out, skewNames)
	skewNames = nil
	skewSeen = map[string]bool{}
	sort.Strings(out)
	return out
}

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
