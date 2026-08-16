package provenance

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/kelp/gale/internal/lockgraph"
)

// Pre-change trace (tier 2 — persisted bind format).
//
// Invariant: two extracted trees with the same regular-file set
// (path, 0777 mode, content) produce the same sha256:… digest. A
// symlink, hardlink, special mode bit, non-regular node, or nested
// .gale-*.toml refuses. Root-level .gale-*.toml sidecars are not
// in the bind.
//
// Pipeline: none. Library only. Admission / installer recompute /
// gale verify are later milestones.
//
// Caller grep: no callers yet. lockfile.V2Artifact.TreeDigest is
// an unvalidated string. provenance.Record.GraphDigest stays
// required.
//
// Command surface: none.
//
// Test anchors: this file. Package-layer is correct.
//
// Blast radius: every future lock and index entry binds the
// wrong tree, or verify blesses a tree it should refuse.

// goldenTreeDigest is DigestTree of bin/jq (0755, "hello\n") and
// README (0644, "hi\n"), computed outside Go from gale-tree-v1.
// README sorts first. A change here invalidates every lock and
// index entry that will record tree_digest.
const goldenTreeDigest = "sha256:08275bdcfe3015489198b6fad78c140a1ad2e050968aa38183f0cce7a54ff97f"

// emptyTreeDigest is DigestTree of a directory with no contributing
// files: sha256 of "gale-tree-v1\n" alone.
const emptyTreeDigest = "sha256:ff598f702b203cbea9052de90e2fc63a919da2a26d8db46e6189ed1f24ed31b5"

func writeFileMode(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	// WriteFile applies umask; the digest binds on-disk Perm().
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

// writeGoldenTree creates bin/jq first so a walk-order digest
// (depth-first, bin before README) fails the golden.
func writeGoldenTree(t *testing.T, dir string) {
	t.Helper()
	writeFileMode(t, filepath.Join(dir, "bin", "jq"), "hello\n", 0o755)
	writeFileMode(t, filepath.Join(dir, "README"), "hi\n", 0o644)
}

func digestTree(t *testing.T, dir string) string {
	t.Helper()
	got, err := DigestTree(context.Background(), dir)
	if err != nil {
		t.Fatalf("DigestTree: %v", err)
	}
	if !lockgraph.IsDigest(got) {
		t.Fatalf("DigestTree = %q, want sha256:<64 hex>", got)
	}
	return got
}

func TestDigestTreeGolden(t *testing.T) {
	dir := t.TempDir()
	writeGoldenTree(t, dir)
	got := digestTree(t, dir)
	if got != goldenTreeDigest {
		t.Errorf("digest = %s, want %s", got, goldenTreeDigest)
	}
}

func TestDigestTreeEmpty(t *testing.T) {
	got := digestTree(t, t.TempDir())
	if got != emptyTreeDigest {
		t.Errorf("empty digest = %s, want %s", got, emptyTreeDigest)
	}
}

func TestDigestTreeCreateOrderIndependent(t *testing.T) {
	a := t.TempDir()
	writeFileMode(t, filepath.Join(a, "bin", "jq"), "hello\n", 0o755)
	writeFileMode(t, filepath.Join(a, "README"), "hi\n", 0o644)

	b := t.TempDir()
	writeFileMode(t, filepath.Join(b, "README"), "hi\n", 0o644)
	writeFileMode(t, filepath.Join(b, "bin", "jq"), "hello\n", 0o755)

	if digestTree(t, a) != digestTree(t, b) {
		t.Errorf("create order changed digest: %s vs %s", digestTree(t, a), digestTree(t, b))
	}
}

func TestDigestTreeSensitivity(t *testing.T) {
	base := t.TempDir()
	writeGoldenTree(t, base)
	want := digestTree(t, base)

	cases := []struct {
		name string
		edit func(dir string)
	}{
		{"content", func(dir string) {
			writeFileMode(t, filepath.Join(dir, "README"), "ho\n", 0o644)
		}},
		{"mode", func(dir string) {
			if err := os.Chmod(filepath.Join(dir, "README"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"path", func(dir string) {
			if err := os.Rename(filepath.Join(dir, "README"), filepath.Join(dir, "READ ME")); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeGoldenTree(t, dir)
			tc.edit(dir)
			got := digestTree(t, dir)
			if got == want {
				t.Errorf("digest unchanged after %s edit", tc.name)
			}
		})
	}
}

func TestDigestTreeEmptyDirsDoNotContribute(t *testing.T) {
	dir := t.TempDir()
	writeGoldenTree(t, dir)
	before := digestTree(t, dir)
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "also", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := digestTree(t, dir); got != before {
		t.Errorf("empty dirs changed digest: %s vs %s", got, before)
	}
}

func TestDigestTreeRootSidecarsExcluded(t *testing.T) {
	dir := t.TempDir()
	writeGoldenTree(t, dir)
	before := digestTree(t, dir)
	for _, name := range []string{
		".gale-provenance.toml",
		".gale-deps.toml",
		".gale-foo.toml",
	} {
		writeFileMode(t, filepath.Join(dir, name), "sidecar\n", 0o644)
	}
	if got := digestTree(t, dir); got != before {
		t.Errorf("root .gale-*.toml changed digest: %s vs %s", got, before)
	}
}

func TestDigestTreeOrdinaryTomlIncluded(t *testing.T) {
	dir := t.TempDir()
	writeGoldenTree(t, dir)
	before := digestTree(t, dir)
	writeFileMode(t, filepath.Join(dir, "foo.toml"), "pkg\n", 0o644)
	if got := digestTree(t, dir); got == before {
		t.Error("ordinary foo.toml did not change digest")
	}
}

func TestDigestTreeNestedGaleTomlRefused(t *testing.T) {
	dir := t.TempDir()
	writeGoldenTree(t, dir)
	writeFileMode(t, filepath.Join(dir, "bin", ".gale-foo.toml"), "evil\n", 0o644)
	_, err := DigestTree(context.Background(), dir)
	if !errors.Is(err, ErrForbiddenEntry) {
		t.Errorf("err = %v, want ErrForbiddenEntry", err)
	}
}

func TestDigestTreeRootSidecarSymlinkRefused(t *testing.T) {
	dir := t.TempDir()
	writeGoldenTree(t, dir)
	target := filepath.Join(dir, "README")
	link := filepath.Join(dir, ".gale-provenance.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, err := DigestTree(context.Background(), dir)
	if !errors.Is(err, ErrForbiddenEntry) {
		t.Errorf("err = %v, want ErrForbiddenEntry", err)
	}
}

func TestDigestTreeSymlinkRefused(t *testing.T) {
	dir := t.TempDir()
	writeGoldenTree(t, dir)
	if err := os.Symlink("README", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	_, err := DigestTree(context.Background(), dir)
	if !errors.Is(err, ErrForbiddenEntry) {
		t.Errorf("err = %v, want ErrForbiddenEntry", err)
	}
}

func TestDigestTreeHardlinkRefused(t *testing.T) {
	dir := t.TempDir()
	writeGoldenTree(t, dir)
	if err := os.Link(filepath.Join(dir, "README"), filepath.Join(dir, "also")); err != nil {
		t.Fatal(err)
	}
	_, err := DigestTree(context.Background(), dir)
	if !errors.Is(err, ErrForbiddenEntry) {
		t.Errorf("err = %v, want ErrForbiddenEntry", err)
	}
}

func TestDigestTreeExternalHardlinkRefused(t *testing.T) {
	dir := t.TempDir()
	writeGoldenTree(t, dir)
	outside := filepath.Join(t.TempDir(), "out")
	writeFileMode(t, outside, "x\n", 0o644)
	if err := os.Link(outside, filepath.Join(dir, "in")); err != nil {
		t.Fatal(err)
	}
	_, err := DigestTree(context.Background(), dir)
	if !errors.Is(err, ErrForbiddenEntry) {
		t.Errorf("err = %v, want ErrForbiddenEntry", err)
	}
}

func TestDigestTreeFifoRefused(t *testing.T) {
	dir := t.TempDir()
	writeGoldenTree(t, dir)
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	_, err := DigestTree(context.Background(), dir)
	if !errors.Is(err, ErrForbiddenEntry) {
		t.Errorf("err = %v, want ErrForbiddenEntry", err)
	}
}

func TestDigestTreeSetuidRefused(t *testing.T) {
	dir := t.TempDir()
	writeGoldenTree(t, dir)
	path := filepath.Join(dir, "bin", "jq")
	if err := os.Chmod(path, 0o755|os.ModeSetuid); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSetuid == 0 {
		t.Skip("filesystem stripped setuid")
	}
	_, err = DigestTree(context.Background(), dir)
	if !errors.Is(err, ErrForbiddenEntry) {
		t.Errorf("err = %v, want ErrForbiddenEntry", err)
	}
}

func TestDigestTreeMissingDir(t *testing.T) {
	_, err := DigestTree(context.Background(), filepath.Join(t.TempDir(), "missing"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want fs.ErrNotExist", err)
	}
}

func TestDigestTreeNotDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	writeFileMode(t, path, "x\n", 0o644)
	_, err := DigestTree(context.Background(), path)
	if err == nil {
		t.Fatal("DigestTree(file) = nil, want error")
	}
	if errors.Is(err, ErrForbiddenEntry) {
		return
	}
	// A regular-file root is not a tree; any error is enough as
	// long as it is not a successful digest.
}

func TestDigestTreeCanceledContext(t *testing.T) {
	dir := t.TempDir()
	writeGoldenTree(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := DigestTree(ctx, dir)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestDigestTreeCanceledMidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	// 32MiB is enough that a canceled ctx is observed during copy
	// on this machine; the copy loop checks ctx every chunk.
	chunk := make([]byte, 1024*1024)
	for range 32 {
		if _, err := f.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(2 * time.Millisecond)
		cancel()
	}()
	_, err = DigestTree(ctx, dir)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestDigestTreeSymlinkedRoot(t *testing.T) {
	realDir := t.TempDir()
	writeGoldenTree(t, realDir)
	link := filepath.Join(t.TempDir(), "root")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	got := digestTree(t, link)
	if got != goldenTreeDigest {
		t.Errorf("symlinked root digest = %s, want %s", got, goldenTreeDigest)
	}
}

func TestDigestTreeNewlineFilenameRefused(t *testing.T) {
	dir := t.TempDir()
	writeGoldenTree(t, dir)
	writeFileMode(t, filepath.Join(dir, "a\nb"), "x\n", 0o644)
	_, err := DigestTree(context.Background(), dir)
	if !errors.Is(err, ErrForbiddenEntry) {
		t.Errorf("err = %v, want ErrForbiddenEntry", err)
	}
}

func TestDigestTreeDotDotInNameAllowed(t *testing.T) {
	dir := t.TempDir()
	writeFileMode(t, filepath.Join(dir, "foo..bar"), "x\n", 0o644)
	if _, err := DigestTree(context.Background(), dir); err != nil {
		t.Errorf("foo..bar: %v", err)
	}
}
