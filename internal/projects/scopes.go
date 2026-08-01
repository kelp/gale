package projects

import (
	"fmt"
	"path/filepath"
)

// Scope is one place on this machine that owns a gale environment: the
// global scope, or a registered project.
//
// It exists because the lock design asks several different questions
// of one set. The farm claimant guard (§4) asks what each scope's
// binaries resolve through; the migration veto (§13) asks whether any
// scope's lock names a different hash for an identity. Different
// questions, same set, and a scan that misses a scope does not fail
// loudly — it silently approves. One definition is what keeps the two
// from drifting apart.
type Scope struct {
	// Label identifies the scope to a human. Fail-closed errors name
	// it, so it has to read as a place, not an index.
	Label string
	// GaleDir is the scope's .gale directory: the global gale home
	// itself, or <project>/.gale.
	GaleDir string
	// LockPath is the scope's gale.lock. It may not exist; an absent
	// lock is a scope with nothing to say, not an error.
	LockPath string
}

// Scopes enumerates the global scope and every registered project.
//
// The global scope is always first and is never in the registry:
// registerProject skips it by design, so a registry-only walk misses
// the one scope every machine has.
//
// A registry that cannot be read is an error, never an empty set. The
// registry is the only record of which projects exist, so shrinking
// the set on a read failure would turn "I cannot tell" into "nobody
// disagrees" — the wrong answer for every caller here.
//
// A registered project whose directory is gone needs no special case:
// its lock reads as absent and its generation as empty, which is the
// correct answer for both questions above. Prune reaps the entry
// separately.
func Scopes(galeHome string) ([]Scope, error) {
	regProjects, err := List(galeHome)
	if err != nil {
		return nil, fmt.Errorf("reading the project registry: %w", err)
	}
	scopes := make([]Scope, 0, len(regProjects)+1)
	scopes = append(scopes, Scope{
		Label:    "the global scope",
		GaleDir:  galeHome,
		LockPath: filepath.Join(galeHome, "gale.lock"),
	})
	for _, proj := range regProjects {
		scopes = append(scopes, Scope{
			Label:    "project " + proj,
			GaleDir:  filepath.Join(proj, ".gale"),
			LockPath: filepath.Join(proj, "gale.lock"),
		})
	}
	return scopes, nil
}
