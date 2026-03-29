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

var (
	ghAvailable bool
	ghOnce      sync.Once
	disabled    bool
)

// resetAvailable clears the cached availability check.
// Used by tests to reset state between runs.
func resetAvailable() {
	ghOnce = sync.Once{}
	disabled = false
}

// Disable forces Available to return false. Used by
// tests that don't want attestation verification.
func Disable() {
	disabled = true
}

// Enable re-enables attestation checking after Disable.
func Enable() {
	disabled = false
}

// Available reports whether the gh CLI is on PATH
// and attestation checking is enabled.
func Available() bool {
	if disabled {
		return false
	}
	ghOnce.Do(func() {
		_, err := lookPath("gh")
		ghAvailable = err == nil
	})
	return ghAvailable
}

// VerifyFile runs gh attestation verify on a local
// file against the given GitHub repo. Returns nil on
// success, error with gh output on failure.
func VerifyFile(filePath, repo string) error {
	return runVerify(filePath, repo)
}

// VerifyOCI runs gh attestation verify on an OCI image
// URI against the given GitHub repo.
func VerifyOCI(ociURI, repo string) error {
	return runVerify(ociURI, repo)
}

func runVerify(subject, repo string) error {
	if !Available() {
		return fmt.Errorf("gh CLI not found")
	}

	ghPath, _ := lookPath("gh")
	cmd := exec.Command(ghPath, "attestation", "verify",
		subject, "--repo", repo)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("attestation verification failed: %s",
			string(out))
	}
	return nil
}
