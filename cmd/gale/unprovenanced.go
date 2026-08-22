package main

import (
	"errors"
	"fmt"

	"github.com/kelp/gale/internal/provenance"
)

// errRaceLostToProvenance reports a directory that stopped being
// replaceable between the decision and the lock, because another
// gale provenanced it. Retrying is the whole remedy, which is why it
// is distinct from a conflict: nothing is wrong with the store.
var errRaceLostToProvenance = errors.New(
	"the directory was provenanced by another run",
)

// errCandidateUnprovenanced reports a rebuilt artifact that carries
// no record of its own, so replacing with it would trade one
// unattested directory for another.
var errCandidateUnprovenanced = errors.New(
	"the rebuilt artifact carries no provenance",
)

// stillUnprovenanced re-establishes, inside the commit locks, the
// classification a replacement made outside them.
//
// The four answers are four different situations and must not
// collapse into one. Only a VALID record means another run got
// there first, which is a race the user retries. A record that
// exists and does not validate is an integrity failure exactly as
// it is in lockRoot, and reporting it as a lost race would tell the
// user to retry into the same corrupt state forever. Anything else,
// a permission problem or an I/O error, is returned as itself,
// because reading it as either would decide the fate of a directory
// on the strength of a file that could not be opened.
func stillUnprovenanced(dir, name, full string) error {
	_, err := provenance.ReadUnverified(dir)
	switch {
	case errors.Is(err, provenance.ErrAbsent):
		return nil
	case err == nil:
		return fmt.Errorf(
			"%s gained provenance while %s@%s was being rebuilt, so it is "+
				"no longer the unprovenanced directory a replacement may "+
				"overwrite: %w",
			dir, name, full, errRaceLostToProvenance,
		)
	case errors.Is(err, provenance.ErrInvalid):
		return fmt.Errorf("replacing %s@%s: %w", name, full, err)
	default:
		return fmt.Errorf(
			"reading provenance for %s@%s: %w", name, full, err,
		)
	}
}
