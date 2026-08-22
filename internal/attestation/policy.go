package attestation

// The attestation identity policy required by §7d of the fetch
// plan: the expected issuer, SAN, and source repo live in gale,
// not in the index. An index entry may declare that an artifact
// is attested; which identity must have produced it is decided
// here. A declaration without an entry below is refused at
// resolve time — verifying a bundle with no identity policy is
// not a control.

// PolicyFor returns the GitHub repository ("owner/name") whose
// Actions workflow must have attested artifacts of the named
// package. The second result reports whether a policy exists;
// a false means a declared attestation cannot be verified and
// callers must refuse.
func PolicyFor(name string) (string, bool) {
	repo, ok := attestationPolicy[name]
	return repo, ok
}

// attestationPolicy maps a package name to the source repo its
// attestations must carry. Required declarations ship here even
// before the index flips: the policy is what makes a future
// declaration verifiable.
var attestationPolicy = map[string]string{
	"gale": "kelp/gale",
}
