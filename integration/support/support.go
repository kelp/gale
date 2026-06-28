// Package support holds the integration-test harness:
// a fake GHCR blob server, a mock gh CLI, fixture
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
// manifest under the kelp/gale-recipes/<name> repository path. It
// returns the synthetic image manifest digest the caller records in
// the lockfile so `gale verify` derives the same referrers URL.
//
// The bytes are minimal but shaped exactly like what gale parses:
//   - referrers index: {"manifests":[{digest, artifactType:
//     "application/vnd.dev.sigstore.bundle.v0.3+json"}]}
//   - referrer manifest: {"layers":[{digest: <bundle blob digest>}]}
//   - bundle blob: an opaque JSON bundle handed to gh via --bundle.
func (fg *FakeGHCR) AttestReferrer(name string) (string, error) {
	repoPath := "kelp/gale-recipes/" + name
	base := "/v2/" + repoPath

	bundle := []byte(`{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json"}`)
	bundleDigest := "sha256:" + hexSHA(bundle)
	fg.RegisterContent(base+"/blobs/"+bundleDigest, bundle)

	refManifest, err := json.Marshal(map[string]any{
		"layers": []map[string]string{{"digest": bundleDigest}},
	})
	if err != nil {
		return "", fmt.Errorf("marshal referrer manifest: %w", err)
	}
	refDigest := "sha256:" + hexSHA(refManifest)
	fg.RegisterContent(base+"/manifests/"+refDigest, refManifest)

	// The image manifest digest is synthetic: gale only uses it to
	// build the referrers URL, never re-fetching the image manifest on
	// the verify path. Make it deterministic from the package name.
	manifestDigest := "sha256:" + hexSHA([]byte("image-manifest:"+name))
	index, err := json.Marshal(map[string]any{
		"manifests": []map[string]string{{
			"digest":       refDigest,
			"artifactType": "application/vnd.dev.sigstore.bundle.v0.3+json",
			"mediaType":    "application/vnd.oci.image.manifest.v1+json",
		}},
	})
	if err != nil {
		return "", fmt.Errorf("marshal referrers index: %w", err)
	}
	fg.RegisterContent(base+"/referrers/"+manifestDigest, index)

	return manifestDigest, nil
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

// FakeGH is the mocked gh CLI used for attestation paths.
// Scripts rewrite its behavior via gale-gh-returns.
type FakeGH struct {
	Dir   string // prepend to PATH; contains the gh binary
	state string // script reads exit code/stdout/stderr from here
}

// WriteFakeGH creates a shell script named "gh" in a new
// dir. Default state: exit 0 with no output. Every
// invocation appends its full argv to <dir>/log so tests
// can assert on the args gale passed.
func WriteFakeGH(t *testing.T, workDir string) *FakeGH {
	t.Helper()
	dir := filepath.Join(workDir, "fake-gh")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "state")
	if err := os.WriteFile(state, []byte("0\n\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "gh")
	// `gh attestation --help` is the availability probe in
	// internal/attestation.probe(). Always succeed for that
	// invocation so tests can drive only the verify-call
	// behaviour via gale-gh-returns. Otherwise a
	// gale-gh-returns exit=1 trips the probe first and
	// `gale verify` short-circuits to "verification
	// unavailable" before ever invoking attestation verify.
	body := "#!/bin/sh\n" +
		"state=\"" + state + "\"\n" +
		"log=\"" + filepath.Join(dir, "log") + "\"\n" +
		"printf '%s\\n' \"$*\" >> \"$log\"\n" +
		"if [ \"$1\" = \"attestation\" ] && [ \"$2\" = \"--help\" ]; then\n" +
		"  exit 0\n" +
		"fi\n" +
		"exitcode=$(sed -n '1p' \"$state\")\n" +
		"stdout=$(sed -n '2p' \"$state\")\n" +
		"stderr=$(sed -n '3p' \"$state\")\n" +
		"[ -n \"$stdout\" ] && printf '%s\\n' \"$stdout\"\n" +
		"[ -n \"$stderr\" ] && printf '%s\\n' \"$stderr\" >&2\n" +
		"exit \"${exitcode:-0}\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return &FakeGH{Dir: dir, state: state}
}

// SetState rewrites the gh script's state file.
func (g *FakeGH) SetState(exitCode int, stdout, stderr string) error {
	body := fmt.Sprintf("%d\n%s\n%s\n",
		exitCode,
		strings.ReplaceAll(stdout, "\n", " "),
		strings.ReplaceAll(stderr, "\n", " "))
	return os.WriteFile(g.state, []byte(body), 0o644)
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
//	gale-attest-referrer <package> <lockfile>
//
// It wires the fake GHCR to serve an OCI referrers index, a referrer
// manifest, and a Sigstore bundle blob for <package>, then rewrites
// <lockfile> so the package carries the matching manifest_digest. This
// drives `gale verify` down the tokenless OCI-referrer path entirely
// against the fake registry — no real network, no GitHub token.
func CmdAttestReferrer(ts *testscript.TestScript, neg bool, args []string) {
	if neg {
		ts.Fatalf("gale-attest-referrer does not support negation")
	}
	if len(args) != 2 {
		ts.Fatalf("gale-attest-referrer: needs <package> <lockfile>")
	}
	ghcr, _ := ts.Value("ghcr").(*FakeGHCR)
	if ghcr == nil {
		ts.Fatalf("gale-attest-referrer: no ghcr in env")
	}
	manifestDigest, err := ghcr.AttestReferrer(args[0])
	if err != nil {
		ts.Fatalf("gale-attest-referrer: %v", err)
	}
	if err := setLockManifestDigest(ts.MkAbs(args[1]), args[0], manifestDigest); err != nil {
		ts.Fatalf("gale-attest-referrer: %v", err)
	}
}

// CmdGHReturns is the "gale-gh-returns" script command.
//
// Usage:
//
//	gale-gh-returns exit=<int> [stdout=<line>] [stderr=<line>]
func CmdGHReturns(ts *testscript.TestScript, neg bool, args []string) {
	if neg {
		ts.Fatalf("gale-gh-returns does not support negation")
	}
	gh, _ := ts.Value("gh").(*FakeGH)
	if gh == nil {
		ts.Fatalf("gale-gh-returns: no gh in env")
	}
	m := parseKV(args)
	exitCode := 0
	if v, ok := m["exit"]; ok {
		// Sscanf failure leaves exitCode at 0 — same as
		// omitting the key. That matches the test contract:
		// "exit=" alone means success.
		_, _ = fmt.Sscanf(v, "%d", &exitCode)
	}
	if err := gh.SetState(exitCode, m["stdout"], m["stderr"]); err != nil {
		ts.Fatalf("gale-gh-returns: %v", err)
	}
}

// --- helpers ---

func parseKV(args []string) map[string]string {
	m := make(map[string]string, len(args))
	for _, a := range args {
		i := strings.IndexByte(a, '=')
		if i < 0 {
			m[a] = ""
			continue
		}
		m[a[:i]] = a[i+1:]
	}
	return m
}

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
