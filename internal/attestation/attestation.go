package attestation

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// DefaultRepo is the GitHub repository where recipe
// binaries are built and attested.
const DefaultRepo = "kelp/gale-recipes"

// lookPath is the function used to find gh on PATH.
// Overridden in tests.
var lookPath = exec.LookPath

// warnWriter receives the one-time "attestation disabled"
// warning emitted when gh is missing or too old. Defaults
// to stderr; overridden in tests.
var warnWriter io.Writer = os.Stderr

// Verifier checks Sigstore attestations for files.
type Verifier interface {
	// Available reports whether attestation verification
	// can run. The first time it returns false in a process
	// it also emits a warning to stderr explaining why —
	// silently skipping attestation would hide a real
	// degradation of the supply-chain guarantee.
	Available() bool
	// UnavailableReason returns a human-readable
	// explanation of why Available returned false. Empty
	// when Available is true.
	UnavailableReason() string
	VerifyFile(filePath, repo string) error
}

// GHVerifier implements Verifier using the gh CLI.
type GHVerifier struct {
	probeOnce sync.Once
	available bool
	reason    string
	warnOnce  sync.Once
}

// NewVerifier returns a Verifier backed by the gh CLI.
func NewVerifier() Verifier {
	return &GHVerifier{}
}

// Available reports whether a usable gh CLI is locatable
// and supports the "attestation" subcommand. Emits a
// one-time stderr warning on the first false result so
// the user always sees that attestation verification was
// skipped — never silently.
func (v *GHVerifier) Available() bool {
	v.probeOnce.Do(v.probe)
	if !v.available {
		v.warnOnce.Do(func() {
			fmt.Fprintf(warnWriter,
				"warning: attestation verification disabled: %s\n",
				v.reason)
		})
	}
	return v.available
}

// UnavailableReason returns why Available is false, or
// "" when attestation is available.
func (v *GHVerifier) UnavailableReason() string {
	v.probeOnce.Do(v.probe)
	return v.reason
}

// probe locates gh and confirms it supports the
// "attestation" subcommand (added in gh 2.49.0). Runs at
// most once per verifier.
func (v *GHVerifier) probe() {
	ghPath, err := findGh()
	if err != nil {
		v.reason = "gh CLI not found; install with " +
			"`gale install gh` or see https://cli.github.com"
		return
	}
	// `gh attestation --help` exits 0 on a current gh and
	// non-zero with "unknown command" on gh < 2.49.0. We
	// don't care about the output text — only the exit
	// status — which keeps this resilient to future help
	// wording changes.
	cmd := exec.Command(ghPath, "attestation", "--help")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		v.reason = fmt.Sprintf(
			"gh at %s lacks 'attestation' subcommand "+
				"(need gh >= 2.49.0); install a current gh "+
				"with `gale install gh`", ghPath,
		)
		return
	}
	v.available = true
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

// findGh locates the gh CLI, preferring gale's bundled
// ~/.gale/current/bin/gh over the system PATH. Why: an
// older gh earlier on PATH (system packages still ship
// 2.46.x in many distros) lacks the "attestation"
// subcommand added in gh 2.49.0, which would otherwise
// downgrade binary installs to source builds. Gale's
// own gh recipe is kept current.
func findGh() (string, error) {
	if home, err := os.UserHomeDir(); err == nil {
		bundled := filepath.Join(
			home, ".gale", "current", "bin", "gh",
		)
		if info, err := os.Stat(bundled); err == nil && !info.IsDir() {
			return bundled, nil
		}
	}
	return lookPath("gh")
}

func runVerify(subject, repo string) error {
	ghPath, err := findGh()
	if err != nil {
		return fmt.Errorf("gh CLI not found")
	}

	if !strings.HasPrefix(subject, "oci://") {
		if info, err := os.Stat(subject); err == nil && info.IsDir() {
			return fmt.Errorf(
				"attestation subject is a directory, expected a file: %s",
				subject,
			)
		}
	}

	cmd := exec.Command(ghPath, "attestation", "verify",
		subject, "--repo", repo)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, `unknown command "attestation"`) {
			return fmt.Errorf(
				"gh at %s lacks 'attestation' (need gh >= 2.49.0); "+
					"install a current gh with: gale install gh",
				ghPath,
			)
		}
		return fmt.Errorf("attestation verification failed: %s", msg)
	}
	return nil
}
