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

// Error names each collision and tells the user to remove one
// package. Leftover [bin] overlays do not settle a collision.
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
	b.WriteString("; remove one of those packages")
	return b.String()
}

// BinArbiter decides which package owns each bin/ basename in a
// generation and records the names more than one package provides.
//
// It is exported so a check over an already-built generation (gh#197)
// applies this rule rather than restating it. Restating it is how the
// two would drift: a doctor that disagrees with the rebuild reports
// collisions gale accepts, or misses ones it refuses.
type BinArbiter struct {
	owner      map[string]string
	collisions []BinCollision
}

// NewBinArbiter returns an arbiter. A second package that ships the
// same basename is a collision. There is no overlay that names a
// winner.
func NewBinArbiter() *BinArbiter {
	return &BinArbiter{
		owner: make(map[string]string),
	}
}

// Claim reports whether pkg's bin/<name> belongs in the generation.
//
// The first claimant links. A second claim records a collision —
// the caller surfaces it through Err once every package has been
// offered its entries, so a user with three collisions fixes them in
// one pass instead of three rebuilds.
func (a *BinArbiter) Claim(pkg, name string) bool {
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
