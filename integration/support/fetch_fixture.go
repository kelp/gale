package support

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/store"
	"github.com/rogpeppe/go-internal/testscript"
)

const (
	fixtureHello    = "hello"
	fixtureHelloDep = "hello-dep"
	fixtureVerOld   = "1.0"
	fixtureVerNew   = "1.1"
)

var fixturePlatforms = []string{
	"darwin/arm64", "darwin/amd64", "linux/amd64", "linux/arm64",
}

// FixtureSHA256 is the artifact digest gale-fixture uses for
// name@version. Install never downloads it: fetch-setup stages
// the tree at FetchPath first.
func FixtureSHA256(name, version string) string {
	sum := sha256.Sum256([]byte("gale-fixture:" + name + "@" + version))
	return hex.EncodeToString(sum[:])
}

func fixtureNames() []string {
	return []string{fixtureHello, fixtureHelloDep}
}

func writeIndexRepo(ts *testscript.TestScript, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, name := range fixtureNames() {
		body, err := indexDocument(ts, name)
		if err != nil {
			return err
		}
		path := filepath.Join(dst, "index", name[:1], name+".toml")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, body, 0o644); err != nil { //nolint:gosec
			return err
		}
	}
	if err := runGit(dst, "init"); err != nil {
		return err
	}
	if err := runGit(dst, "config", "user.email", "fixture@gale.test"); err != nil {
		return err
	}
	if err := runGit(dst, "config", "user.name", "gale-fixture"); err != nil {
		return err
	}
	if err := runGit(dst, "add", "index"); err != nil {
		return err
	}
	return runGit(dst, "commit", "-m", "index")
}

func indexDocument(ts *testscript.TestScript, name string) ([]byte, error) {
	var b strings.Builder
	fmt.Fprintf(&b, `[package]
name = %q
description = "gale integration fixture"
license = "MIT"
homepage = "https://github.com/kelp/%s"
repo = "kelp/%s"
latest = %q

`, name, name, name, fixtureVerOld)
	for _, ver := range []string{fixtureVerOld, fixtureVerNew} {
		sha := FixtureSHA256(name, ver)
		tree, err := payloadTreeDigest(ts, name)
		if err != nil {
			return nil, err
		}
		for _, plat := range fixturePlatforms {
			fmt.Fprintf(&b, `[versions.%q.artifacts.%q]
url = "https://github.com/kelp/%s/releases/download/%s/%s.tar.gz"
format = "tar.gz"
sha256 = %q
tree_digest = %q
hash_source = "upstream-sha256sums"
strip = 0

[[versions.%q.artifacts.%q.files]]
src = %q
dest = "bin/%s"
mode = 0o755

`, ver, plat, name, ver, name, sha, tree, ver, plat, name, name)
		}
	}
	_ = runtime.GOOS
	return []byte(b.String()), nil
}

func payloadTreeDigest(ts *testscript.TestScript, name string) (string, error) {
	src := filepath.Join(ts.Getenv("FIXTURES"), "payloads", name)
	tmp, err := os.MkdirTemp("", "gale-fixture-tree-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if err := copyTree(src, tmp); err != nil {
		return "", err
	}
	return provenance.DigestTree(context.Background(), tmp)
}

func stageFetch(ts *testscript.TestScript, name, version string) (string, error) {
	src := filepath.Join(ts.Getenv("FIXTURES"), "payloads", name)
	if _, err := os.Stat(src); err != nil {
		return "", fmt.Errorf("payload %s: %w", name, err)
	}
	sha := FixtureSHA256(name, version)
	tree, err := payloadTreeDigest(ts, name)
	if err != nil {
		return "", err
	}
	st := store.NewStore(filepath.Join(ts.Getenv("HOME"), ".gale", "pkg"))
	dest, err := st.FetchPath(name, version, sha)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}
	if err := copyTree(src, dest); err != nil {
		return "", err
	}
	if err := provenance.WriteFetch(dest, provenance.FetchRecord{
		Name: name, Version: version, SHA256: sha,
		TreeDigest: tree, Method: provenance.MethodFetch,
	}); err != nil {
		return "", err
	}
	key := strings.ToUpper(strings.ReplaceAll(name, "-", "_")) +
		"_" + strings.ReplaceAll(version, ".", "_")
	ts.Setenv(key+"_SHA", sha)
	ts.Setenv(key+"_DIR", dest)
	return dest, nil
}

func stageFetches(ts *testscript.TestScript, args []string) error {
	names := fixtureNames()
	versions := []string{fixtureVerOld, fixtureVerNew}
	switch len(args) {
	case 0:
	case 1:
		names = []string{args[0]}
	case 2:
		names = []string{args[0]}
		versions = []string{args[1]}
	default:
		return fmt.Errorf("fetch-setup: wants 0, 1, or 2 args")
	}
	for _, name := range names {
		for _, ver := range versions {
			if _, err := stageFetch(ts, name, ver); err != nil {
				return err
			}
		}
	}
	return nil
}

func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{
		"-c", "commit.gpgsign=false",
		"-c", "core.hooksPath=/dev/null",
	}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return nil
}

func copyTree(src, dst string) error {
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
		in, err := os.Open(path) //nolint:gosec
		if err != nil {
			return err
		}
		defer in.Close()
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fi.Mode()) //nolint:gosec
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}
