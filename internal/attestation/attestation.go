package attestation

import (
	"fmt"
	"os/exec"
	"sync"
)

// DefaultRepo is the GitHub repository where recipe
// binaries are built and attested.
const DefaultRepo = "kelp/gale-recipes"

// lookPath is the function used to find gh on PATH.
// Overridden in tests.
var lookPath = exec.LookPath

// Verifier checks Sigstore attestations for files.
type Verifier interface {
	Available() bool
	VerifyFile(filePath, repo string) error
}

// GHVerifier implements Verifier using the gh CLI.
type GHVerifier struct {
	once      sync.Once
	available bool
}

// NewVerifier returns a Verifier backed by the gh CLI.
func NewVerifier() Verifier {
	return &GHVerifier{}
}

// Available reports whether the gh CLI is on PATH.
func (v *GHVerifier) Available() bool {
	v.once.Do(func() {
		_, err := lookPath("gh")
		v.available = err == nil
	})
	return v.available
}

// VerifyFile runs gh attestation verify on a local
// file against the given GitHub repo. Returns nil on
// success, error with gh output on failure.
func (v *GHVerifier) VerifyFile(filePath, repo string) error {
	return runVerify(filePath, repo)
}

// VerifyOCI runs gh attestation verify on an OCI image
// URI against the given GitHub repo.
func VerifyOCI(ociURI, repo string) error {
	return runVerify(ociURI, repo)
}

func runVerify(subject, repo string) error {
	ghPath, err := lookPath("gh")
	if err != nil {
		return fmt.Errorf("gh CLI not found")
	}

	cmd := exec.Command(ghPath, "attestation", "verify",
		subject, "--repo", repo)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("attestation verification failed: %s",
			string(out))
	}
	return nil
}
