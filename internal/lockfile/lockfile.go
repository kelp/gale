package lockfile

import (
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
)

// LockedPackage represents a pinned package in the flat
// pre-enforcement lockfile. Nothing writes this schema any more; it
// survives because every gale shipped before enforcement did, so a
// lock on disk today may still be in it.
type LockedPackage struct {
	Version        string `toml:"version"`
	SHA256         string `toml:"sha256,omitempty"`
	ManifestDigest string `toml:"manifest_digest,omitempty"`
}

// LockFile represents a gale.lock file in the flat pre-enforcement
// schema. Load returns it as View.Legacy for KindLegacy.
type LockFile struct {
	Packages map[string]LockedPackage `toml:"packages"`
}

// decodeLegacy decodes the flat pre-enforcement schema. A file with
// no [packages] table yields an empty map rather than a nil one, so
// callers may write into the result without checking.
//
// Read-only by design. This package writes the enforced schema and
// nothing else: a flat-schema writer would convert an enforced lock
// back into one an already-shipped gale rewrites at will, which is
// the downgrade guardKey exists to stop.
func decodeLegacy(data []byte) (*LockFile, error) {
	var lf LockFile
	if _, err := toml.Decode(string(data), &lf); err != nil {
		return nil, fmt.Errorf("parsing lock file: %w", err)
	}
	if lf.Packages == nil {
		lf.Packages = make(map[string]LockedPackage)
	}
	return &lf, nil
}

// Stale reports whether the flat schema's entries disagree with the
// gale.toml packages.
//
// Counting entries is sound only for this schema, where nothing ever
// wrote an entry for a transitive dependency. The enforced schema
// records the whole closure, so it compares its declared roots
// instead; see CheckDeclared.
func (lf *LockFile) Stale(tomlPackages map[string]string) bool {
	if len(tomlPackages) != len(lf.Packages) {
		return true
	}
	for name, version := range tomlPackages {
		locked, ok := lf.Packages[name]
		if !ok || !VersionMatches(locked.Version, version) {
			return true
		}
	}
	return false
}

// VersionMatches reports whether the locked version
// (potentially canonical form like "1.8.1-1") matches the
// toml version (bare form like "1.8.1"). They match if the
// strings are equal, or if the locked version is
// "<toml>-<N>" for any all-digit suffix N > 0 (canonical
// revision form). Revision 0 is not a valid canonical
// revision and is not treated as a match.
func VersionMatches(locked, toml string) bool {
	if locked == toml {
		return true
	}
	// Canonical form: "<version>-<revision>" where revision
	// is a positive integer. Strip the last "-<N>" suffix
	// and compare the base to toml.
	if i := strings.LastIndex(locked, "-"); i >= 0 {
		base := locked[:i]
		suffix := locked[i+1:]
		allDigits := len(suffix) > 0
		for _, c := range suffix {
			if c < '0' || c > '9' {
				allDigits = false
				break
			}
		}
		if allDigits && suffix != "0" && base == toml {
			return true
		}
	}
	return false
}
