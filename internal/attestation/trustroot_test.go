package attestation

// Tests for trustRootSource: trusted-root resolution precedence
// (env override → TUF → embedded snapshot), error reporting for an
// unreadable env path, and the production constructor's env/cache
// wiring. Fixture trust material comes from sigstoretest; nothing
// here touches the network except the deliberately unreachable TUF
// URLs in the precedence and embedded-fallback tests.

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/attestation/sigstoretest"
)

// trustedRootFile writes a fresh fixture trusted_root.json to a
// temp file and returns its path, for use as an envPath override.
func trustedRootFile(t *testing.T) string {
	t.Helper()
	fx, err := sigstoretest.New()
	if err != nil {
		t.Fatalf("new fixture: %v", err)
	}
	trJSON, err := fx.TrustedRootJSON()
	if err != nil {
		t.Fatalf("trusted root JSON: %v", err)
	}
	return writeTempFile(t, trJSON)
}

func TestTrustRootLoadEnvOverride(t *testing.T) {
	// Precedence proof: the TUF config points at an unreachable
	// repo with an empty cache. Env-first resolution never touches
	// TUF, so load succeeds AND no fallback warning is emitted. An
	// inverted implementation (TUF first, env fallback) would fail
	// TUF and write the embedded-fallback warning.
	var buf bytes.Buffer
	src := &trustRootSource{
		envPath:  trustedRootFile(t),
		cacheDir: t.TempDir(),
		tufURL:   "http://127.0.0.1:1",
		warn:     &buf,
	}

	tr, err := src.load()
	if err != nil {
		t.Fatalf("load with env override: %v", err)
	}
	if tr == nil {
		t.Fatal("load returned nil trusted root")
	}
	if buf.Len() != 0 {
		t.Fatalf("env-override load wrote a warning: %q", buf.String())
	}
}

func TestTrustRootLoadEnvPathUnreadable(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing", "trusted_root.json")
	src := &trustRootSource{envPath: missing}

	_, err := src.load()
	if err == nil {
		t.Fatal("load succeeded, want error for unreadable env path")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("error %q does not mention path %q", err, missing)
	}
}

func TestTrustRootTUFFailureFallsBackToEmbedded(t *testing.T) {
	var buf bytes.Buffer
	src := &trustRootSource{
		cacheDir: t.TempDir(),
		tufURL:   "http://127.0.0.1:1",
		warn:     &buf,
	}

	tr, err := src.load()
	if err != nil {
		t.Fatalf("load with unreachable TUF repo: %v", err)
	}
	if tr == nil {
		t.Fatal("load returned nil trusted root")
	}
	if !strings.Contains(buf.String(), "embedded") {
		t.Fatalf("warning %q does not mention embedded fallback", buf.String())
	}

	warned := buf.String()
	tr2, err := src.load()
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if tr2 != tr {
		t.Fatal("second load returned a different trusted root, want memoized instance")
	}
	if got := buf.String(); got != warned {
		t.Fatalf("second load duplicated warning: %q -> %q", warned, got)
	}
}

func TestNewTrustRootSourceReadsEnvAndCacheDir(t *testing.T) {
	want := filepath.Join(t.TempDir(), "trusted_root.json")
	t.Setenv(TrustedRootEnv, want)

	src := newTrustRootSource()
	if src.envPath != want {
		t.Fatalf("envPath = %q, want %q", src.envPath, want)
	}
	wantCache := filepath.Join(".gale", "cache", "sigstore-tuf")
	if !strings.Contains(src.cacheDir, wantCache) {
		t.Fatalf("cacheDir = %q, want it to contain %q", src.cacheDir, wantCache)
	}
	const wantTUF = "https://tuf-repo-cdn.sigstore.dev"
	if src.tufURL != wantTUF {
		t.Fatalf("tufURL = %q, want %q", src.tufURL, wantTUF)
	}
	if src.warn == nil {
		t.Fatal("warn writer is nil, want a default (stderr)")
	}
}
