// Package support holds the integration-test harness:
// a fake GHCR blob server, a synthetic Sigstore fixture
// for minting real attestation bundles, a fixture
// tarball builder, and the testscript commands that
// glue them into .txtar scripts.
package support

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kelp/gale/internal/attestation/sigstoretest"
	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/rogpeppe/go-internal/testscript"
)

// Payload is a pre-built fixture tarball served by the
// fake GHCR.
type Payload struct {
	Name        string // fixture payload name (e.g. "hello")
	TarballPath string // absolute path to the .tar.zst on disk
	SHA256      string // hex sha256 of the tarball
}

// Payloads maps payload name to its metadata.
type Payloads struct {
	Map     map[string]*Payload
	tmpRoot string
}

// BuildPayloads walks fixturesRoot/payloads/ and builds
// a tar.zst archive per subdir. Each archive is named
// <name>.tar.zst under tmpRoot and is registered in the
// returned Payloads struct. Called once per test run.
func BuildPayloads(fixturesRoot, tmpRoot string) (*Payloads, error) {
	payloadsDir := filepath.Join(fixturesRoot, "payloads")
	entries, err := os.ReadDir(payloadsDir)
	if err != nil {
		return nil, fmt.Errorf("read payloads dir: %w", err)
	}
	if err := os.MkdirAll(tmpRoot, 0o755); err != nil {
		return nil, err
	}
	p := &Payloads{Map: make(map[string]*Payload), tmpRoot: tmpRoot}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		src := filepath.Join(payloadsDir, name)
		dst := filepath.Join(tmpRoot, name+".tar.zst")
		if err := download.CreateTarZstd(src, dst); err != nil {
			return nil, fmt.Errorf("build %s: %w", name, err)
		}
		sum, err := hashFile(dst)
		if err != nil {
			return nil, fmt.Errorf("hash %s: %w", name, err)
		}
		p.Map[name] = &Payload{
			Name:        name,
			TarballPath: dst,
			SHA256:      sum,
		}
		envNames = append(envNames, EnvNameForSHA(name))
	}
	return p, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// FakeGHCR serves fixture tarballs over HTTP so install
// scenarios exercise the real download/extract path
// without touching real GHCR.
type FakeGHCR struct {
	URL      string
	server   *httptest.Server
	payloads *Payloads

	mu      sync.Mutex
	serve   map[string]string // URL path → payload name
	content map[string][]byte // URL path → raw bytes (for index.tsv etc.)
}

// StartFakeGHCR launches an httptest server. The server
// serves every registered payload at the URL declared via
// Register(path, name). By default every payload is
// registered under /blobs/<name>/any.
func StartFakeGHCR(t *testing.T, payloads *Payloads) *FakeGHCR {
	t.Helper()
	fg := &FakeGHCR{
		payloads: payloads,
		serve:    make(map[string]string),
		content:  make(map[string][]byte),
	}
	fg.server = httptest.NewServer(http.HandlerFunc(fg.handle))
	fg.URL = fg.server.URL
	// Default routes:
	//   /blobs/<name>/1.0-<rev>/<platform>  — prebuilt binary
	//     archive (served as .tar.zst). Both rev=1 and rev=2
	//     are wired so revision-bump scenarios don't have to
	//     re-register.
	//   /blobs/source/<name>  — source tarball (same .tar.zst
	//     payload; ExtractSource accepts .tar.zst). Used by
	//     build-from-source scenarios.
	for name := range payloads.Map {
		// Source URLs preserve the .tar.zst extension so
		// build.sourceExtension picks the right decompressor.
		fg.Register("/blobs/source/"+name+".tar.zst", name)
		for _, rev := range []string{"1", "2"} {
			for _, plat := range []string{
				"darwin-arm64", "linux-amd64", "linux-arm64",
			} {
				fg.Register(
					fmt.Sprintf("/blobs/%s/1.0-%s/%s", name, rev, plat),
					name,
				)
			}
		}
	}
	t.Cleanup(fg.Close)
	return fg
}

func (fg *FakeGHCR) Close() {
	fg.server.Close()
}

func (fg *FakeGHCR) Register(urlPath, payloadName string) {
	fg.mu.Lock()
	defer fg.mu.Unlock()
	fg.serve[urlPath] = payloadName
}

// RegisterContent serves raw bytes at urlPath. Used for
// non-tarball responses like index.tsv or .versions files.
func (fg *FakeGHCR) RegisterContent(urlPath string, body []byte) {
	fg.mu.Lock()
	defer fg.mu.Unlock()
	fg.content[urlPath] = body
}

func (fg *FakeGHCR) handle(w http.ResponseWriter, r *http.Request) {
	fg.mu.Lock()
	body, hasBody := fg.content[r.URL.Path]
	name, ok := fg.serve[r.URL.Path]
	fg.mu.Unlock()
	if hasBody {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(body)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	p := fg.payloads.Map[name]
	if p == nil {
		http.Error(w, "payload missing", http.StatusInternalServerError)
		return
	}
	f, err := os.Open(p.TarballPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = io.Copy(w, f)
}

// AttestReferrer registers an OCI referrers index, a referrer
// manifest, and a Sigstore bundle blob for the package's image
// manifest under the kelp/gale-recipes/<name> repository path. The
// bundle is a REAL signed sigstoretest bundle: its subject digest
// equals the returned image manifest digest, and its certificate
// identity (SAN + SourceRepositoryURI) names identityRepo
// ("owner/name"). Pass a repo other than the one gale verifies
// against (kelp/gale-recipes) to mint a bundle that must fail
// verification on identity mismatch.
//
// It returns the manifest digest the caller records in the lockfile
// (so gale derives the same referrers URL and verifies the same
// subject) plus the trusted_root.json bytes the bundle chains to,
// for GALE_SIGSTORE_TRUSTED_ROOT.
//
// The OCI bytes are minimal but shaped exactly like what gale parses:
//   - referrers index: {"manifests":[{digest, artifactType:
//     "application/vnd.dev.sigstore.bundle.v0.3+json"}]}
//   - referrer manifest: {"layers":[{digest: <bundle blob digest>}]}
//   - bundle blob: the signed Sigstore bundle JSON.
func (fg *FakeGHCR) AttestReferrer(name, identityRepo string) (string, []byte, error) {
	repoPath := "kelp/gale-recipes/" + name
	base := "/v2/" + repoPath

	// The image manifest bytes are synthetic: gale only uses the
	// digest to build the referrers URL and as the attestation
	// subject, never re-fetching the image manifest on the verify
	// path. Make them deterministic from the package name.
	manifestBytes := []byte("image-manifest:" + name)
	manifestDigest := "sha256:" + hexSHA(manifestBytes)

	bundle, trustedRoot, err := mintBundle(manifestBytes, identityRepo)
	if err != nil {
		return "", nil, err
	}
	bundleDigest := "sha256:" + hexSHA(bundle)
	fg.RegisterContent(base+"/blobs/"+bundleDigest, bundle)

	refManifest, err := json.Marshal(map[string]any{
		"layers": []map[string]string{{"digest": bundleDigest}},
	})
	if err != nil {
		return "", nil, fmt.Errorf("marshal referrer manifest: %w", err)
	}
	refDigest := "sha256:" + hexSHA(refManifest)
	fg.RegisterContent(base+"/manifests/"+refDigest, refManifest)

	index, err := json.Marshal(map[string]any{
		"manifests": []map[string]string{{
			"digest":       refDigest,
			"artifactType": "application/vnd.dev.sigstore.bundle.v0.3+json",
			"mediaType":    "application/vnd.oci.image.manifest.v1+json",
		}},
	})
	if err != nil {
		return "", nil, fmt.Errorf("marshal referrers index: %w", err)
	}
	fg.RegisterContent(base+"/referrers/"+manifestDigest, index)

	return manifestDigest, trustedRoot, nil
}

// mintBundle creates a fresh ephemeral Sigstore fixture and signs a
// bundle over subject whose certificate identity names identityRepo.
// It returns the bundle JSON plus the fixture's trusted_root.json. A
// fresh fixture per call keeps parallel testscripts independent —
// generation is in-memory and takes ~10ms.
func mintBundle(subject []byte, identityRepo string) (bundleJSON, trustedRoot []byte, err error) {
	fx, err := sigstoretest.New()
	if err != nil {
		return nil, nil, fmt.Errorf("sigstore fixture: %w", err)
	}
	trustedRoot, err = fx.TrustedRootJSON()
	if err != nil {
		return nil, nil, fmt.Errorf("trusted root json: %w", err)
	}
	opts := sigstoretest.GitHubOpts(subject)
	opts.SourceRepositoryURI = "https://github.com/" + identityRepo
	opts.SAN = opts.SourceRepositoryURI +
		"/.github/workflows/build.yml@refs/heads/main"
	bundleJSON, err = fx.SignedBundle(opts)
	if err != nil {
		return nil, nil, fmt.Errorf("mint signed bundle: %w", err)
	}
	return bundleJSON, trustedRoot, nil
}

func hexSHA(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// setLockManifestDigest rewrites the lockfile so pkg carries the given
// manifest_digest, leaving its other fields untouched.
func setLockManifestDigest(lockPath, pkg, manifestDigest string) error {
	lf, err := lockfile.Read(lockPath)
	if err != nil {
		return fmt.Errorf("read lockfile: %w", err)
	}
	entry, ok := lf.Packages[pkg]
	if !ok {
		return fmt.Errorf("package %q not in lockfile %s", pkg, lockPath)
	}
	entry.ManifestDigest = manifestDigest
	lf.Packages[pkg] = entry
	if err := lockfile.Write(lockPath, lf); err != nil {
		return fmt.Errorf("write lockfile: %w", err)
	}
	return nil
}

// --- testscript commands ---

// CmdFixture is the "gale-fixture" script command.
//
// Subcommands:
//
//	gale-fixture recipes <dst>
//	    Copy fixtures/recipes/* to <dst>, expanding
//	    placeholders (__GHCR_URL__, __<NAME>_PAYLOAD_SHA__,
//	    etc.) against the current script environment.
//
//	gale-fixture register-blob <url-path> <payload-name>
//	    Ask the fake GHCR to serve <payload-name> at
//	    <url-path>. Overrides the default route.
func CmdFixture(ts *testscript.TestScript, neg bool, args []string) {
	if neg {
		ts.Fatalf("gale-fixture does not support negation")
	}
	if len(args) == 0 {
		ts.Fatalf("gale-fixture: missing subcommand")
	}
	switch args[0] {
	case "recipes":
		if len(args) != 2 {
			ts.Fatalf("gale-fixture recipes: needs <dst>")
		}
		dst := ts.MkAbs(args[1])
		if err := copyRecipes(ts, dst); err != nil {
			ts.Fatalf("gale-fixture recipes: %v", err)
		}
	case "render":
		if len(args) != 3 {
			ts.Fatalf("gale-fixture render: needs <template-rel-path> <dst>")
		}
		src := filepath.Join(ts.Getenv("FIXTURES"), args[1])
		dst := ts.MkAbs(args[2])
		if err := renderFile(ts, src, dst); err != nil {
			ts.Fatalf("gale-fixture render: %v", err)
		}
	case "register-blob":
		if len(args) != 3 {
			ts.Fatalf("gale-fixture register-blob: needs <url-path> <payload>")
		}
		ghcr, _ := ts.Value("ghcr").(*FakeGHCR)
		if ghcr == nil {
			ts.Fatalf("no ghcr in env")
		}
		ghcr.Register(args[1], args[2])
	case "serve-file":
		if len(args) != 3 {
			ts.Fatalf("gale-fixture serve-file: needs <url-path> <src-file>")
		}
		ghcr, _ := ts.Value("ghcr").(*FakeGHCR)
		if ghcr == nil {
			ts.Fatalf("no ghcr in env")
		}
		body, err := os.ReadFile(ts.MkAbs(args[2])) //nolint:gosec
		if err != nil {
			ts.Fatalf("gale-fixture serve-file: %v", err)
		}
		ghcr.RegisterContent(args[1], body)
	default:
		ts.Fatalf("gale-fixture: unknown subcommand %q", args[0])
	}
}

// CmdAttestReferrer is the "gale-attest-referrer" script command.
//
// Usage:
//
//	gale-attest-referrer <package> <lockfile> <identity-repo>
//
// It wires the fake GHCR to serve an OCI referrers index, a referrer
// manifest, and a real signed Sigstore bundle for <package> whose
// certificate identity names <identity-repo> (pass anything other
// than kelp/gale-recipes to mint a bundle that must fail
// verification on identity mismatch). It rewrites <lockfile> so the
// package carries the matching manifest_digest and writes the
// fixture's trusted root to $WORK/trusted_root.json. Scripts then
// point GALE_SIGSTORE_TRUSTED_ROOT at that file and set
// GALE_SIGSTORE_TEST_NO_SCT=1 (synthetic fixtures cannot mint SCTs)
// so `gale verify` runs native in-process verification entirely
// against the fake registry — no real network, no GitHub token.
func CmdAttestReferrer(ts *testscript.TestScript, neg bool, args []string) {
	if neg {
		ts.Fatalf("gale-attest-referrer does not support negation")
	}
	if len(args) != 3 {
		ts.Fatalf("gale-attest-referrer: needs <package> <lockfile> <identity-repo>")
	}
	ghcr, _ := ts.Value("ghcr").(*FakeGHCR)
	if ghcr == nil {
		ts.Fatalf("gale-attest-referrer: no ghcr in env")
	}
	manifestDigest, trustedRoot, err := ghcr.AttestReferrer(args[0], args[2])
	if err != nil {
		ts.Fatalf("gale-attest-referrer: %v", err)
	}
	// G306 — a world-readable trust root fixture inside the
	// script work dir; not sensitive material.
	rootPath := ts.MkAbs("trusted_root.json")
	if err := os.WriteFile(rootPath, trustedRoot, 0o644); err != nil { //nolint:gosec
		ts.Fatalf("gale-attest-referrer: write trusted root: %v", err)
	}
	if err := setLockManifestDigest(ts.MkAbs(args[1]), args[0], manifestDigest); err != nil {
		ts.Fatalf("gale-attest-referrer: %v", err)
	}
}

// --- helpers ---

// renderFile reads a single template file, substitutes
// placeholders, and writes it to dst (stripping a trailing
// .tmpl suffix from dst if present).
func renderFile(ts *testscript.TestScript, src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	data = substitute(ts, data)
	dst = strings.TrimSuffix(dst, ".tmpl")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	// G703 — dst is a fixture path the test scaffolding
	// constructs; not user input.
	return os.WriteFile(dst, data, 0o644) //nolint:gosec
}

// copyRecipes walks $FIXTURES/recipes/ and writes each
// file into dst, substituting placeholders.
func copyRecipes(ts *testscript.TestScript, dst string) error {
	src := filepath.Join(ts.Getenv("FIXTURES"), "recipes")
	return filepath.Walk(src, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if fi.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		// G122/G703 — src and target are fixture paths the
		// test scaffolding constructs from $FIXTURES; the
		// Walk callback runs over a tree we just laid down.
		data, err := os.ReadFile(path) //nolint:gosec
		if err != nil {
			return err
		}
		data = substitute(ts, data)
		// Strip .tmpl suffix if present.
		target = strings.TrimSuffix(target, ".tmpl")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, fi.Mode()) //nolint:gosec
	})
}

func substitute(ts *testscript.TestScript, data []byte) []byte {
	s := string(data)
	// Known placeholders.
	s = strings.ReplaceAll(s, "__GHCR_URL__", ts.Getenv("GHCR_URL"))
	// Payload SHAs: __<NAME>_PAYLOAD_SHA__
	// These are injected into the script env by setupScript.
	for _, kv := range scriptEnvList(ts) {
		if !strings.HasSuffix(kv.key, "_PAYLOAD_SHA") {
			continue
		}
		s = strings.ReplaceAll(s, "__"+kv.key+"__", kv.val)
	}
	return []byte(s)
}

type envKV struct{ key, val string }

// scriptEnvList returns every payload SHA env var seen in
// the script environment. Payload env names are registered
// by BuildPayloads into envNames.
func scriptEnvList(ts *testscript.TestScript) []envKV {
	out := make([]envKV, 0, len(envNames))
	for _, k := range envNames {
		if v := ts.Getenv(k); v != "" {
			out = append(out, envKV{k, v})
		}
	}
	return out
}

// envNames lists the per-payload SHA env var names
// (e.g. HELLO_PAYLOAD_SHA). Populated by BuildPayloads.
var envNames []string

// EnvNameForSHA converts a payload name to its env var.
func EnvNameForSHA(payloadName string) string {
	out := make([]byte, 0, len(payloadName)+len("_PAYLOAD_SHA"))
	for i := 0; i < len(payloadName); i++ {
		c := payloadName[i]
		switch {
		case c >= 'a' && c <= 'z':
			out = append(out, c-32)
		case c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	out = append(out, "_PAYLOAD_SHA"...)
	return string(out)
}
