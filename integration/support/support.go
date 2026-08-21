// Package support holds the integration-test harness:
// a fixture tarball builder and the testscript commands
// that glue them into .txtar scripts. Live scripts use
// gale-fixture index / fetch-setup.
package support

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kelp/gale/internal/download"
	"github.com/rogpeppe/go-internal/testscript"
)

// Payload is a pre-built fixture tarball.
type Payload struct {
	TarballPath string // absolute path to the .tar.gz on disk
	SHA256      string // hex sha256 of the tarball
}

// Payloads maps payload name to its metadata.
type Payloads struct {
	Map map[string]*Payload
}

// BuildPayloads walks fixturesRoot/payloads/ and builds
// a tar.gz archive per subdir. Each archive is named
// <name>.tar.gz under tmpRoot and is registered in the
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
	p := &Payloads{Map: make(map[string]*Payload)}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		src := filepath.Join(payloadsDir, name)
		dst := filepath.Join(tmpRoot, name+".tar.gz")
		if err := createTarGz(src, dst); err != nil {
			return nil, fmt.Errorf("build %s: %w", name, err)
		}
		sum, err := download.HashFile(context.Background(), dst)
		if err != nil {
			return nil, fmt.Errorf("hash %s: %w", name, err)
		}
		p.Map[name] = &Payload{
			TarballPath: dst,
			SHA256:      sum,
		}
	}
	return p, nil
}

func createTarGz(srcDir, dst string) error {
	f, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create archive: %w", err)
	}
	defer f.Close()
	gw := gzip.NewWriter(f)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rf, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, rf)
		closeErr := rf.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

// --- testscript commands ---

// CmdFixture is the "gale-fixture" script command.
//
// Subcommands:
//
//	gale-fixture recipes <dst>
//	    Copy fixtures/recipes/* to <dst>, expanding
//	    leftover placeholders (__GHCR_URL__,
//	    __<NAME>_PAYLOAD_SHA__, etc.) against the
//	    current script environment.
//
//	gale-fixture render <template-rel-path> <dst>
//	    Read a template from $FIXTURES/<template-rel-path>,
//	    substitute placeholders, write to <dst> (strips
//	    .tmpl suffix if present).
//
//	gale-fixture index <dst>
//	    Write a git checkout of index/hello and
//	    index/hello-dep (versions 1.0 and 1.1) and
//	    commit it. --index must point at this repo.
//
//	gale-fixture fetch-setup [name [version]]
//	    Stage fetch trees + sidecars under
//	    $HOME/.gale/pkg/fetch so install/sync skip HTTP.
func CmdFixture(ts *testscript.TestScript, neg bool, args []string) {
	if neg {
		ts.Fatalf("gale-fixture does not support negation")
	}
	if len(args) == 0 {
		ts.Fatalf("gale-fixture: missing subcommand")
	}
	payloads, _ := ts.Value("payloads").(*Payloads)
	if payloads == nil {
		ts.Fatalf("gale-fixture: no payloads in env")
	}
	switch args[0] {
	case "recipes":
		if len(args) != 2 {
			ts.Fatalf("gale-fixture recipes: needs <dst>")
		}
		dst := ts.MkAbs(args[1])
		if err := copyRecipes(ts, dst, payloads); err != nil {
			ts.Fatalf("gale-fixture recipes: %v", err)
		}
	case "render":
		if len(args) != 3 {
			ts.Fatalf("gale-fixture render: needs <template-rel-path> <dst>")
		}
		src := filepath.Join(ts.Getenv("FIXTURES"), args[1])
		dst := ts.MkAbs(args[2])
		if err := renderFile(ts, src, dst, payloads); err != nil {
			ts.Fatalf("gale-fixture render: %v", err)
		}
	case "index":
		if len(args) != 2 {
			ts.Fatalf("gale-fixture index: needs <dst>")
		}
		if err := writeIndexRepo(ts, ts.MkAbs(args[1])); err != nil {
			ts.Fatalf("gale-fixture index: %v", err)
		}
	case "fetch-setup":
		if err := stageFetches(ts, args[1:]); err != nil {
			ts.Fatalf("gale-fixture fetch-setup: %v", err)
		}
	default:
		ts.Fatalf("gale-fixture: unknown subcommand %q", args[0])
	}
}

// --- helpers ---

// renderFile reads a single template file, substitutes
// placeholders, and writes it to dst (stripping a trailing
// .tmpl suffix from dst if present).
func renderFile(ts *testscript.TestScript, src, dst string, payloads *Payloads) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	data = substitute(ts, data, payloads)
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
func copyRecipes(ts *testscript.TestScript, dst string, payloads *Payloads) error {
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
		data = substitute(ts, data, payloads)
		// Strip .tmpl suffix if present.
		target = strings.TrimSuffix(target, ".tmpl")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, fi.Mode()) //nolint:gosec
	})
}

func substitute(ts *testscript.TestScript, data []byte, payloads *Payloads) []byte {
	return substituteData(ts.Getenv, data, payloads)
}

// substituteData replaces known placeholders in data.
// getenv is called to look up env var values. payloads
// provides the set of payload SHA env var names.
func substituteData(getenv func(string) string, data []byte, payloads *Payloads) []byte {
	s := string(data)
	// Known placeholders.
	s = strings.ReplaceAll(s, "__GHCR_URL__", getenv("GHCR_URL"))
	// Payload SHAs: __<NAME>_PAYLOAD_SHA__
	// These are injected into the script env by setupScript.
	for name := range payloads.Map {
		k := EnvNameForSHA(name)
		if v := getenv(k); v != "" {
			s = strings.ReplaceAll(s, "__"+k+"__", v)
		}
	}
	return []byte(s)
}

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
