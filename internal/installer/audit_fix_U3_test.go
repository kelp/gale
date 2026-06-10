package installer

import (
	"archive/tar"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
	"github.com/klauspost/compress/zstd"
)

// u3TarEntry is one ordered entry for createU3TarZstd. Order
// matters for the gh#41 tests: a valid entry must extract
// before a poison entry aborts the stream.
type u3TarEntry struct {
	name    string
	content string
	mode    int64
}

func createU3TarZstd(t *testing.T, entries []u3TarEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pkg.tar.zst")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create tar.zst: %v", err)
	}
	zw, err := zstd.NewWriter(f)
	if err != nil {
		t.Fatalf("create zstd writer: %v", err)
	}
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     e.name,
			Mode:     e.mode,
			Size:     int64(len(e.content)),
		}); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if _, err := tw.Write([]byte(e.content)); err != nil {
			t.Fatalf("write tar content: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zstd writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
	return path
}

// TestExtractBuildInPlace_FailedExtractionNotInstalled is the
// regression test for gh#41 (source path): the in-place source
// install extracted directly into the live canonical store dir,
// so a crash or error mid-extraction left a partial dir that
// IsInstalled treated as a healthy install forever.
//
// The archive's second entry is a path-traversal poison that
// aborts extraction after the first entry was written. RED
// (pre-fix): the canonical dir holds the partial bin/tool and
// IsInstalled reports true. GREEN: extraction happens in a
// transient staging sibling; the canonical dir never sees
// partial content.
func TestExtractBuildInPlace_FailedExtractionNotInstalled(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := filepath.Join(galeDir, "pkg")
	s := store.NewStore(storeRoot)

	storeDir, err := s.Create("u3src", "1.0-1")
	if err != nil {
		t.Fatalf("create store dir: %v", err)
	}

	archive := createU3TarZstd(t, []u3TarEntry{
		{name: "bin/tool", content: "#!/bin/sh\necho hi\n", mode: 0o755},
		{name: "../evil", content: "x", mode: 0o644},
	})
	result := &build.BuildResult{Archive: archive}

	if err := extractBuild(result, storeDir, nil); err == nil {
		t.Fatal("expected extraction error from poisoned archive")
	}
	if s.IsInstalled("u3src", "1.0-1") {
		t.Fatal("partially extracted store dir reported as " +
			"installed after failed install (gh#41)")
	}
}

// TestInstallBinaryToInPlace_FixupFailureNotInstalled is the
// regression test for gh#41 (binary path): installBinaryTo
// renamed the staged extraction into the canonical store dir
// BEFORE running the fixup pipeline. A crash or error between
// the rename and fixup completion left a broken-but-non-empty
// dir that IsInstalled treated as healthy, so retries returned
// MethodCached and never repaired it.
//
// An unreadable .pc file makes FixupPkgConfig fail
// deterministically. RED (pre-fix): the failure happens after
// the rename, leaving the canonical dir populated and
// IsInstalled true. GREEN: fixups run in the staging dir before
// the rename, so a fixup failure leaves no installed content.
func TestInstallBinaryToInPlace_FixupFailureNotInstalled(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("mode-0 files are readable as root")
	}
	galeDir := t.TempDir()
	storeRoot := filepath.Join(galeDir, "pkg")
	s := store.NewStore(storeRoot)

	extractDir, err := s.Create("u3bin", "1.0-1")
	if err != nil {
		t.Fatalf("create store dir: %v", err)
	}

	archive := createU3TarZstd(t, []u3TarEntry{
		{name: "bin/tool", content: "#!/bin/sh\necho hi\n", mode: 0o755},
		// Unreadable .pc file: FixupPkgConfig fails reading it.
		{name: "lib/pkgconfig/poison.pc", content: "prefix=/x\n", mode: 0},
	})
	hash := hashFile(t, archive)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, archive)
		},
	))
	defer srv.Close()

	bin := &recipe.Binary{
		URL:    srv.URL + "/pkg.tar.zst",
		SHA256: hash,
		Trust:  recipe.TrustSHA256Only,
	}

	err = installBinaryTo(
		bin, extractDir, extractDir, "u3bin", "1.0",
		nil, nil, true, nil,
	)
	if err == nil {
		t.Fatal("expected fixup-pipeline failure")
	}
	if s.IsInstalled("u3bin", "1.0-1") {
		t.Fatal("half-finalized store dir reported as " +
			"installed after fixup failure (gh#41)")
	}
}

// TestExtractBuildInPlace_FarmConflictFails is the regression
// test for gh#42: the in-place source path swallowed
// farm.Populate conflicts (warning to stderr, install reported
// success) while the binary and staged paths treat the same
// conflict as fatal. A basename conflict means two packages
// claim the same versioned dylib — rpath-linked binaries could
// silently resolve the wrong package's library.
//
// RED (pre-fix): extractBuild returns nil despite the conflict.
// GREEN: the conflict fails the install on every path.
func TestExtractBuildInPlace_FarmConflictFails(t *testing.T) {
	var lib string
	switch runtime.GOOS {
	case "darwin":
		lib = "libu3clash.1.dylib"
	case "linux":
		lib = "libu3clash.so.1"
	default:
		t.Skip("farm only supports darwin/linux")
	}

	galeDir := t.TempDir()
	storeRoot := filepath.Join(galeDir, "pkg")
	s := store.NewStore(storeRoot)

	// Package A already owns the farm entry for the dylib.
	aDir, err := s.Create("u3liba", "1.0-1")
	if err != nil {
		t.Fatalf("create store dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(aDir, "lib"), 0o755); err != nil {
		t.Fatalf("create lib dir: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(aDir, "lib", lib), []byte("A"), 0o644,
	); err != nil {
		t.Fatalf("write dylib: %v", err)
	}
	if err := farm.Populate(aDir, farm.Dir(galeDir)); err != nil {
		t.Fatalf("seed farm: %v", err)
	}

	// Package B ships the same versioned dylib basename.
	bDir, err := s.Create("u3libb", "1.0-1")
	if err != nil {
		t.Fatalf("create store dir: %v", err)
	}
	archive := createU3TarZstd(t, []u3TarEntry{
		{name: "lib/" + lib, content: "B", mode: 0o644},
	})
	result := &build.BuildResult{Archive: archive}

	if err := extractBuild(result, bDir, nil); err == nil {
		t.Fatal("in-place source install swallowed the farm " +
			"basename conflict that binary and staged installs " +
			"treat as fatal (gh#42)")
	}
}
