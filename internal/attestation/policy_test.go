package attestation

import "testing"

// §7d: the identity policy lives in gale. The gale package is a
// required declaration, so its policy must always resolve.
func TestPolicyForGale(t *testing.T) {
	repo, ok := PolicyFor("gale")
	if !ok {
		t.Fatal("gale has no attestation policy; it is a required declaration")
	}
	if repo != "kelp/gale" {
		t.Errorf("repo = %q, want kelp/gale", repo)
	}
}

// An unknown package has no policy: a declared attestation for
// it must be refused by callers, never verified against a guess.
func TestPolicyForUnknown(t *testing.T) {
	if _, ok := PolicyFor("not-a-package"); ok {
		t.Error("unknown package resolved a policy")
	}
}

// GitHubIssuer is the OIDC issuer every GitHub Artifact
// Attestation carries; the lock records it alongside the repo.
func TestGitHubIssuerConstant(t *testing.T) {
	if GitHubIssuer != githubOIDCIssuer {
		t.Errorf("GitHubIssuer = %q, want the sigstore.go constant", GitHubIssuer)
	}
}
