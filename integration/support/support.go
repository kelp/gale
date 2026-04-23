// Package support holds the integration-test harness:
// a fake GHCR blob server, a mock gh CLI, fixture
// tarball builder, and the testscript commands that
// glue them into .txtar scripts.
package support

import (
	"crypto/sha256"
	"encoding/hex"
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

	mu    sync.Mutex
	serve map[string]string // URL path → payload name
}

// StartFakeGHCR launches an httptest server. The server
// serves every registered payload at the URL declared via
// Register(path, name). By default every payload is
// registered under /blobs/<name>/any.
func StartFakeGHCR(t *testing.T, payloads *Payloads) *FakeGHCR {
	t.Helper()
	fg := &FakeGHCR{payloads: payloads, serve: make(map[string]string)}
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
					name)
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

func (fg *FakeGHCR) handle(w http.ResponseWriter, r *http.Request) {
	fg.mu.Lock()
	name, ok := fg.serve[r.URL.Path]
	fg.mu.Unlock()
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

// FakeGH is the mocked gh CLI used for attestation paths.
// Scripts rewrite its behavior via gale-gh-returns.
type FakeGH struct {
	Dir    string // prepend to PATH; contains the gh binary
	state  string // script reads exit code/stdout/stderr from here
}

// WriteFakeGH creates a shell script named "gh" in a new
// dir. Default state: exit 0 with no output.
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
	body := "#!/bin/sh\n" +
		"state=\"" + state + "\"\n" +
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
	default:
		ts.Fatalf("gale-fixture: unknown subcommand %q", args[0])
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
		fmt.Sscanf(v, "%d", &exitCode)
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
	return os.WriteFile(dst, data, 0o644)
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
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		data = substitute(ts, data)
		// Strip .tmpl suffix if present.
		target = strings.TrimSuffix(target, ".tmpl")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, fi.Mode())
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
