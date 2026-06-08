package registry

// TakeMispinned returns the accumulated mispinned package names (sorted
// and deduped) and clears the recorded state.
//
// STUB: real implementation drains thread-safe package-level state.
func TakeMispinned() []string {
	return nil
}

// MispinSummary formats a one-line summary of mispinned packages, or ""
// for empty input.
//
// STUB: real implementation builds the summary line.
func MispinSummary(names []string) string {
	return ""
}
