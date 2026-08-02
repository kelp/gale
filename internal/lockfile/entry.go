package lockfile

import "fmt"

// Entry is one package as a read-only consumer needs it, answered the
// same way from either schema. It is deliberately not the whole node:
// audit, verify, and sbom want the recorded identity and hashes, and
// giving them the dependency edges too would invite each to grow its
// own traversal.
type Entry struct {
	// Version is the canonical version-revision under v1, and
	// whatever the flat schema recorded otherwise.
	Version        string
	SHA256         string
	ManifestDigest string
	// Method is empty for a legacy entry. The flat schema recorded no
	// method, and inventing one would let a caller believe a legacy
	// lock says how the bytes were produced.
	Method string
}

// Entry looks up one package by name.
//
// ok is false, with no error, for the two ordinary negatives: no lock
// at all, and a lock that does not carry this package. Those are states
// a command reports, not failures.
//
// Only roots are addressable by name, which is what the legacy schema
// always held: nothing ever wrote a lock entry for a transitive
// dependency. A v1 lock does record them, but keyed by identity, and
// several versions of one name can coexist there, so answering by name
// from the node table would have to pick one.
//
// host and platform are the caller's, because both are dimensions the
// flat schema did not have. A v1 lock written on darwin therefore
// answers differently when read on linux, and a root belonging to
// another machine's selector is not visible here. Both are the
// intended semantics of the enforced schema rather than accidents of
// this lookup.
func (v *View) Entry(name, host, platform string) (Entry, bool, error) {
	switch v.Kind {
	case KindAbsent:
		return Entry{}, false, nil
	case KindLegacy:
		p, ok := v.Legacy.Packages[name]
		if !ok {
			return Entry{}, false, nil
		}
		return Entry{
			Version:        p.Version,
			SHA256:         p.SHA256,
			ManifestDigest: p.ManifestDigest,
		}, true, nil
	case KindV1:
		return v.V1.entry(name, host, platform)
	default:
		return Entry{}, false, fmt.Errorf("unhandled lockfile kind %s", v.Kind)
	}
}

// entry resolves one root through the effective root set and its
// current-platform artifact.
func (lf *V1) entry(name, host, platform string) (Entry, bool, error) {
	roots, err := lf.EffectiveRoots(host)
	if err != nil {
		return Entry{}, false, err
	}
	id, ok := roots[name]
	if !ok {
		return Entry{}, false, nil
	}
	_, version, err := ParseIdentity(id)
	if err != nil {
		return Entry{}, false, err
	}
	pkg, ok := lf.Packages[id]
	if !ok {
		return Entry{}, false, fmt.Errorf("%w: %s", ErrMissingNode, id)
	}
	art, ok := pkg.Artifacts[platform]
	if !ok {
		// Reported rather than folded into ok=false: "locked, but not
		// for this platform" and "not locked" have different remedies,
		// and a caller told only the second prints "reinstall it",
		// which does not fix a lock that never described this platform.
		return Entry{}, false, fmt.Errorf(
			"%w: %s has no artifact for %s", ErrMissingArtifact, id, platform,
		)
	}
	return Entry{
		Version:        version,
		SHA256:         art.SHA256,
		ManifestDigest: art.ManifestDigest,
		Method:         art.Method,
	}, true, nil
}
