package generation

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// BinCollision records one bin/ basename that two packages both
// provide. Existing is the package that claimed the name first;
// Incoming is the one refused it.
type BinCollision struct {
	Bin      string
	Existing string
	Incoming string
}

// BinCollisionError reports every bin/ basename in a generation that
// more than one package provides. The generation is refused whole:
// picking a winner by sort order is how gh#190 put the wrong binary
// on PATH silently, and dropping only the contested name would remove
// a binary the user already had.
type BinCollisionError struct {
	Collisions []BinCollision
}

// Error names each collision and prints the [bin] stanza that
// resolves it. The remedy is part of the message because no command
// can clear this state: gc and `doctor --repair` rebuild the same
// generation from the same config and hit the same refusal.
func (e *BinCollisionError) Error() string {
	var b strings.Builder
	b.WriteString("executable name collision")
	if len(e.Collisions) != 1 {
		b.WriteString("s")
	}
	b.WriteString(": ")

	parts := make([]string, 0, len(e.Collisions))
	for _, c := range e.Collisions {
		parts = append(parts, fmt.Sprintf(
			"%s is provided by %s and %s", c.Bin, c.Existing, c.Incoming,
		))
	}
	b.WriteString(strings.Join(parts, "; "))
	b.WriteString(
		"\nname the winner in gale.toml and run the command again " +
			"(gale gc and gale doctor --repair rebuild the same " +
			"generation, so neither clears this):\n\n  [bin]\n",
	)
	for _, c := range e.Collisions {
		if seenBin(e.Collisions, c) {
			continue
		}
		fmt.Fprintf(&b, "  %s = %q\n", c.Bin, c.Existing)
	}
	return b.String()
}

// seenBin reports whether an earlier collision already covers c's
// basename, so a name contested by three packages prints one
// suggested stanza line rather than one per loser.
func seenBin(all []BinCollision, c BinCollision) bool {
	for _, other := range all {
		if other == c {
			return false
		}
		if other.Bin == c.Bin {
			return true
		}
	}
	return false
}

// BinArbiter decides which package owns each bin/ basename in a
// generation and records the names more than one package provides.
//
// It is exported so a check over an already-built generation (gh#197)
// applies this rule rather than restating it. Restating it is how the
// two would drift: a doctor that disagrees with the rebuild reports
// collisions gale accepts, or misses ones it refuses.
type BinArbiter struct {
	overrides  map[string]string
	owner      map[string]string
	collisions []BinCollision
}

// NewBinArbiter returns an arbiter honoring overrides, a map of
// basename → winning package read from gale.toml's [bin] table. A nil
// map means no overrides.
func NewBinArbiter(overrides map[string]string) *BinArbiter {
	return &BinArbiter{
		overrides: overrides,
		owner:     make(map[string]string, len(overrides)),
	}
}

// Claim reports whether pkg's bin/<name> belongs in the generation.
//
// An override settles the name outright: the package it names links,
// every other one is skipped, and nothing is recorded. Without one,
// the first claimant links and a second claim records a collision —
// the caller surfaces it through Err once every package has been
// offered its entries, so a user with three collisions fixes them in
// one pass instead of three rebuilds.
func (a *BinArbiter) Claim(pkg, name string) bool {
	if winner, ok := a.overrides[name]; ok {
		return winner == pkg
	}
	if prev, ok := a.owner[name]; ok {
		a.collisions = append(a.collisions, BinCollision{
			Bin:      name,
			Existing: prev,
			Incoming: pkg,
		})
		return false
	}
	a.owner[name] = pkg
	return true
}

// Collisions returns every collision claimed so far, sorted by name,
// then by the package that lost it, so the result never varies with
// map iteration order. The slice is a copy: the caller cannot mutate
// the arbiter's bookkeeping through it.
//
// Exported for the callers that report collisions instead of refusing
// them (gh#219). Only bin/ refuses.
func (a *BinArbiter) Collisions() []BinCollision {
	out := slices.Clone(a.collisions)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Bin != out[j].Bin {
			return out[i].Bin < out[j].Bin
		}
		return out[i].Incoming < out[j].Incoming
	})
	return out
}

// Err returns a *BinCollisionError covering every collision claimed
// so far, or nil when there were none. Collisions come from
// Collisions, so the error never varies with map iteration order.
func (a *BinArbiter) Err() error {
	if len(a.collisions) == 0 {
		return nil
	}
	return &BinCollisionError{Collisions: a.Collisions()}
}
