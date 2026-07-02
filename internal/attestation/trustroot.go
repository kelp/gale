package attestation

// Trusted-root resolution for native Sigstore verification.
// Precedence: env override → TUF (cached) → embedded snapshot.

import (
	_ "embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
)

// TrustedRootEnv is the env var naming a trusted_root.json path
// override (test seam; wins unconditionally).
const TrustedRootEnv = "GALE_SIGSTORE_TRUSTED_ROOT"

// tufCacheValidityDays is how long the TUF cache is trusted before
// a network refresh (matches the gh CLI's one-day validity).
const tufCacheValidityDays = 1

// embeddedTrustedRoot is a checked-in snapshot of the production
// Sigstore trusted_root.json, generated during development from the
// TUF CDN. Refresh: regenerate embedded_trusted_root.json from
// https://tuf-repo-cdn.sigstore.dev before each release. A stale
// snapshot keeps working because runtime resolution refreshes via
// TUF first; the embedded copy serves only as the offline fallback.
//
//go:embed embedded_trusted_root.json
var embeddedTrustedRoot []byte

// trustRootSource resolves the Sigstore trusted root with
// precedence: env override → TUF (cached) → embedded snapshot.
// load is memoized; one instance is shared per verifier.
type trustRootSource struct {
	envPath  string    // captured GALE_SIGSTORE_TRUSTED_ROOT value
	cacheDir string    // TUF cache dir (prod: ~/.gale/cache/sigstore-tuf)
	tufURL   string    // prod: https://tuf-repo-cdn.sigstore.dev
	warn     io.Writer // one-time embedded-fallback warning (prod: os.Stderr)

	once sync.Once
	tr   *root.TrustedRoot
	err  error
}

// newTrustRootSource returns the production-configured source
// (reads the env var, derives cacheDir from the user home dir).
func newTrustRootSource() *trustRootSource {
	return &trustRootSource{
		envPath:  os.Getenv(TrustedRootEnv),
		cacheDir: TUFCacheDir(),
		tufURL:   tuf.DefaultMirror,
		warn:     os.Stderr,
	}
}

// TUFCacheDir returns the production TUF cache directory
// (~/.gale/cache/sigstore-tuf), falling back to a temp-dir path
// when the home dir is unknown. Exported so `gale doctor` can
// report the cache state.
func TUFCacheDir() string {
	base, err := os.UserHomeDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, ".gale", "cache", "sigstore-tuf")
}

// load resolves and memoizes the trusted root.
func (t *trustRootSource) load() (*root.TrustedRoot, error) {
	t.once.Do(func() {
		t.tr, t.err = t.resolve()
	})
	return t.tr, t.err
}

// resolve applies the resolution precedence exactly once.
func (t *trustRootSource) resolve() (*root.TrustedRoot, error) {
	if t.envPath != "" {
		return t.loadEnvPath()
	}

	tr, tufErr := t.fetchTUF()
	if tufErr == nil {
		return tr, nil
	}

	if t.warn != nil {
		fmt.Fprintf(t.warn,
			"warning: sigstore TUF refresh failed, using embedded trusted root snapshot: %v\n",
			tufErr)
	}
	tr, err := root.NewTrustedRootFromJSON(embeddedTrustedRoot)
	if err != nil {
		return nil, fmt.Errorf("parse embedded trusted root: %w", err)
	}
	return tr, nil
}

// loadEnvPath reads and parses the env-override trusted root file.
func (t *trustRootSource) loadEnvPath() (*root.TrustedRoot, error) {
	data, err := os.ReadFile(t.envPath)
	if err != nil {
		return nil, fmt.Errorf("read trusted root override %s: %w", t.envPath, err)
	}
	tr, err := root.NewTrustedRootFromJSON(data)
	if err != nil {
		return nil, fmt.Errorf("parse trusted root override %s: %w", t.envPath, err)
	}
	return tr, nil
}

// fetchTUF fetches the trusted root from the configured TUF
// repository, using the on-disk cache when still valid.
func (t *trustRootSource) fetchTUF() (*root.TrustedRoot, error) {
	opts := tuf.DefaultOptions()
	opts.RepositoryBaseURL = t.tufURL
	opts.CachePath = t.cacheDir
	opts.CacheValidity = tufCacheValidityDays

	tr, err := root.FetchTrustedRootWithOptions(opts)
	if err != nil {
		return nil, fmt.Errorf("fetch sigstore trusted root via TUF: %w", err)
	}
	return tr, nil
}
