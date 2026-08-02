package depsmeta

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/kelp/gale/internal/store"
)

// State is what an authoritative reader can say about one store
// directory's recorded dependencies.
//
// Three states rather than two, because two of the questions asked of
// this file are decisions about destroying or trusting bytes, and for
// those "no dependencies" and "I cannot tell" are opposite answers.
// Read collapses them by design (a missing file decodes to an empty
// Metadata) and Has cannot see the difference at all, since os.Stat
// follows symlinks and reports nothing about the target's type.
type State int

const (
	// StateAbsent means no file. Not an empty closure: nothing was
	// recorded, so nothing is known.
	StateAbsent State = iota
	// StateUnusable means present and not trustworthy — unreadable,
	// not a regular file, or not strictly parseable. The closure is
	// unknown, which for a destructive decision is a refusal.
	StateUnusable
	// StateRecorded means a regular file that parsed strictly. Its
	// dep list is authoritative, including when empty: a valid empty
	// file is an explicitly recorded empty closure.
	StateRecorded
)

func (s State) String() string {
	switch s {
	case StateAbsent:
		return "absent"
	case StateUnusable:
		return "unusable"
	case StateRecorded:
		return "recorded"
	default:
		return "State(?)"
	}
}

// ReadStrict reads a store directory's dependency metadata with no
// tolerance, for callers whose answer must not rest on a reader whose
// leniency was designed for a different question.
//
// os.Lstat, never os.Stat: a symlink here is either an escape from the
// directory the caller named or a path whose target it never
// inspected, and Stat reports a dangling one as absent. That trap has
// already cost this repo once, in the staging-dir provenance path.
//
// Strict parsing rejects unknown fields and semantically empty
// entries. It verifies the metadata's REPRESENTATION, not its
// authenticity: nothing here attests that the file describes the
// directory it sits in, which matters because the callers that need
// this reader are usually looking at a directory nothing has attested.
func ReadStrict(dir string) ([]ResolvedDep, State) {
	path := filepath.Join(dir, File)
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, StateAbsent
		}
		return nil, StateUnusable
	}
	if !fi.Mode().IsRegular() {
		return nil, StateUnusable
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, StateUnusable
	}
	deps, ok := ParseStrict(data)
	if !ok {
		return nil, StateUnusable
	}
	return deps, StateRecorded
}

// ParseStrict decodes metadata bytes, rejecting anything it cannot
// model exactly. Separate from ReadStrict so callers that already hold
// the bytes, or that need a different policy for an absent file, share
// one parser rather than one being subtly laxer than the other.
func ParseStrict(data []byte) ([]ResolvedDep, bool) {
	var md Metadata
	meta, err := toml.Decode(string(data), &md)
	if err != nil || len(meta.Undecoded()) > 0 {
		return nil, false
	}
	for _, d := range md.Deps {
		// Both halves are joined onto the store root by every consumer
		// of this list, so a value that is not a single path component
		// walks out of the store. Rejecting it here rather than at each
		// join keeps the guarantee with the reader that promises it.
		if !store.SafeComponent(d.Name) || !store.SafeComponent(d.Version) {
			return nil, false
		}
		// One reading only. "@" separates a name from a version in
		// every identity gale spells, and a version that already
		// carries a revision suffix is read as-is by the closure walk
		// while the provenance path appends Revision — so "1.7-2" with
		// revision 1 means both "1.7-2" and "1.7-2-1" depending on who
		// is looking. A record that authorizes a destructive decision
		// cannot be ambiguous.
		if strings.Contains(d.Name, "@") ||
			store.HasNumericRevisionSuffix(d.Version) {
			return nil, false
		}
		if d.Revision < 0 {
			return nil, false
		}
	}
	return md.Deps, true
}
