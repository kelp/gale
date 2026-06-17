package installer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/filelock"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/parallel"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
	"github.com/kelp/gale/internal/timing"
	"github.com/klauspost/compress/zstd"
)

func TestInstallFromSourceCreatesBinary(t *testing.T) {
	srcTar := createTestSourceTarGz(t)
	hash := hashFile(t, srcTar)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, srcTar)
		},
	))
	defer srv.Close()

	storeRoot := t.TempDir()
	inst := &Installer{
		Store: store.NewStore(storeRoot),
	}

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "1.0"},
		Source:  recipe.Source{URL: srv.URL + "/source.tar.gz", SHA256: hash},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin",
				"echo '#!/bin/sh\necho hello' > $PREFIX/bin/testpkg",
				"chmod +x $PREFIX/bin/testpkg",
			},
		},
	}

	result, err := inst.Install(r)
	if err != nil {
		t.Fatalf("Install error: %v", err)
	}
	if result.Method != "source" {
		t.Errorf("Method = %q, want %q", result.Method, "source")
	}

	// Verify binary exists in store. Store path uses the
	// full <version>-<revision> form.
	storeBin := filepath.Join(storeRoot, "testpkg", "1.0-1", "bin", "testpkg")
	if _, err := os.Stat(storeBin); err != nil {
		t.Errorf("binary not in store: %v", err)
	}
}

func TestInstallSkipsAlreadyInstalled(t *testing.T) {
	storeRoot := t.TempDir()

	s := store.NewStore(storeRoot)
	s.Create("testpkg", "1.0")
	binPath := filepath.Join(storeRoot, "testpkg", "1.0", "bin")
	os.MkdirAll(binPath, 0o755)

	inst := &Installer{
		Store: s,
	}

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "1.0"},
		Source:  recipe.Source{URL: "http://should-not-be-called", SHA256: "bad"},
	}

	result, err := inst.Install(r)
	if err != nil {
		t.Fatalf("Install error: %v", err)
	}
	if result.Method != "cached" {
		t.Errorf("Method = %q, want %q", result.Method, "cached")
	}
}

func TestInstallResultFields(t *testing.T) {
	srcTar := createTestSourceTarGz(t)
	hash := hashFile(t, srcTar)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, srcTar)
		},
	))
	defer srv.Close()

	storeRoot := t.TempDir()
	inst := &Installer{
		Store: store.NewStore(storeRoot),
	}

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "mypkg", Version: "2.5"},
		Source:  recipe.Source{URL: srv.URL + "/source.tar.gz", SHA256: hash},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin",
				"echo '#!/bin/sh' > $PREFIX/bin/mypkg",
				"chmod +x $PREFIX/bin/mypkg",
			},
		},
	}

	result, err := inst.Install(r)
	if err != nil {
		t.Fatalf("Install error: %v", err)
	}
	if result.Name != "mypkg" {
		t.Errorf("Name = %q, want %q", result.Name, "mypkg")
	}
	if result.Version != "2.5" {
		t.Errorf("Version = %q, want %q", result.Version, "2.5")
	}
}

func TestInstallUpgradeMovesSymlink(t *testing.T) {
	srcTar := createTestSourceTarGz(t)
	hash := hashFile(t, srcTar)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, srcTar)
		},
	))
	defer srv.Close()

	storeRoot := t.TempDir()
	inst := &Installer{
		Store: store.NewStore(storeRoot),
	}

	// Install v1.0.
	r1 := &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "1.0"},
		Source:  recipe.Source{URL: srv.URL, SHA256: hash},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin",
				"echo '#!/bin/sh\necho v1' > $PREFIX/bin/testpkg",
				"chmod +x $PREFIX/bin/testpkg",
			},
		},
	}
	_, err := inst.Install(r1)
	if err != nil {
		t.Fatalf("Install v1.0 error: %v", err)
	}

	// Install v2.0 of the same package.
	r2 := &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "2.0"},
		Source:  recipe.Source{URL: srv.URL, SHA256: hash},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin",
				"echo '#!/bin/sh\necho v2' > $PREFIX/bin/testpkg",
				"chmod +x $PREFIX/bin/testpkg",
			},
		},
	}
	result, err := inst.Install(r2)
	if err != nil {
		t.Fatalf("Install v2.0 error: %v", err)
	}
	if result.Method != "source" {
		t.Errorf("Method = %q, want %q", result.Method, "source")
	}

	// Verify both versions exist in store. Back-compat in
	// resolveVersion accepts "<v>-1" when the bare dir is
	// missing, and IsInstalled follows that path.
	if !inst.Store.IsInstalled("testpkg", "1.0-1") {
		t.Error("v1.0 not in store")
	}
	if !inst.Store.IsInstalled("testpkg", "2.0-1") {
		t.Error("v2.0 not in store")
	}
}

func TestInstallBinaryFromURL(t *testing.T) {
	// Create a tar.zst with bin/testpkg.
	binContent := "#!/bin/sh\necho from-binary"
	tarzst := createTestTarZstd(t, "bin/testpkg", binContent)
	hash := hashFile(t, tarzst)
	blobData, err := os.ReadFile(tarzst)
	if err != nil {
		t.Fatalf("read tar.zst: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Write(blobData)
		},
	))
	defer srv.Close()

	restore := download.SetHTTPClient(srv.Client())
	defer restore()

	storeRoot := t.TempDir()
	inst := &Installer{
		Store: store.NewStore(storeRoot),
	}

	blobURL := fmt.Sprintf("%s/testpkg-1.0.tar.zst", srv.URL)

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "1.0"},
		Source:  recipe.Source{URL: "http://unused", SHA256: "unused"},
		Binary: map[string]recipe.Binary{
			fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH): {
				URL:    blobURL,
				SHA256: hash,
				// Unit test uses a non-ghcr.io URL, so opt
				// out of attestation via the declared policy.
				Trust: recipe.TrustSHA256Only,
			},
		},
	}

	result, err := inst.Install(r)
	if err != nil {
		t.Fatalf("Install error: %v", err)
	}
	if result.Method != "binary" {
		t.Errorf("Method = %q, want %q",
			result.Method, "binary")
	}

	// Verify binary exists in store.
	storeBin := filepath.Join(storeRoot,
		"testpkg", "1.0-1", "bin", "testpkg")
	if _, err := os.Stat(storeBin); err != nil {
		t.Errorf("binary not in store: %v", err)
	}
}

func TestInstallResolvesBuildDeps(t *testing.T) {
	// Create a tar.zst for the dep: bin/deptool that writes
	// a marker file.
	depScript := "#!/bin/sh\necho dep-was-here > \"$1\""
	depTarzst := createTestTarZstd(t, "bin/deptool", depScript)
	depHash := hashFile(t, depTarzst)
	depData, err := os.ReadFile(depTarzst)
	if err != nil {
		t.Fatalf("read dep tar.zst: %v", err)
	}

	// Create source tar.gz for the main package.
	srcTar := createTestSourceTarGz(t)
	srcHash := hashFile(t, srcTar)

	// Serve both files.
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/dep.tar.zst":
				w.Write(depData)
			case "/source.tar.gz":
				http.ServeFile(w, r, srcTar)
			default:
				http.NotFound(w, r)
			}
		},
	))
	defer srv.Close()

	storeRoot := t.TempDir()
	inst := &Installer{
		Store: store.NewStore(storeRoot),
		Resolver: func(name string) (*recipe.Recipe, error) {
			if name == "deptool" {
				return &recipe.Recipe{
					Package: recipe.Package{
						Name: "deptool", Version: "1.0",
					},
					Source: recipe.Source{
						URL:    srv.URL + "/dep.tar.zst",
						SHA256: depHash,
					},
					Binary: map[string]recipe.Binary{
						fmt.Sprintf("%s-%s",
							runtime.GOOS, runtime.GOARCH): {
							URL:    srv.URL + "/dep.tar.zst",
							SHA256: depHash,
							Trust:  recipe.TrustSHA256Only,
						},
					},
				}, nil
			}
			return nil, fmt.Errorf("unknown dep: %s", name)
		},
	}

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "mypkg", Version: "2.0"},
		Source: recipe.Source{
			URL:    srv.URL + "/source.tar.gz",
			SHA256: srcHash,
		},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin",
				"deptool $PREFIX/bin/marker.txt",
				"echo '#!/bin/sh' > $PREFIX/bin/mypkg",
				"chmod +x $PREFIX/bin/mypkg",
			},
		},
		Dependencies: recipe.Dependencies{
			Build: []string{"deptool"},
		},
	}

	result, err := inst.Install(r)
	if err != nil {
		t.Fatalf("Install error: %v", err)
	}
	if result.Method != "source" {
		t.Errorf("Method = %q, want %q",
			result.Method, "source")
	}

	// Verify deptool was installed in store.
	depBin := filepath.Join(storeRoot,
		"deptool", "1.0-1", "bin", "deptool")
	if _, err := os.Stat(depBin); err != nil {
		t.Errorf("dep not in store: %v", err)
	}

	// Verify main package built with deptool available.
	marker := filepath.Join(storeRoot,
		"mypkg", "2.0-1", "bin", "marker.txt")
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("marker not found: %v", err)
	}
	if !strings.Contains(string(data), "dep-was-here") {
		t.Errorf("marker = %q, want dep-was-here", data)
	}
}

// --- helpers ---

func createTestSourceTarGz(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.tar.gz")

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create tar.gz: %v", err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir,
		Name:     "testpkg-1.0/",
		Mode:     0o755,
	})

	content := "placeholder"
	tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     "testpkg-1.0/README",
		Mode:     0o644,
		Size:     int64(len(content)),
	})
	tw.Write([]byte(content))

	return path
}

func hashFile(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open for hash: %v", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatalf("hash: %v", err)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func createTestTarZstd(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pkg.tar.zst")

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create tar.zst: %v", err)
	}
	defer f.Close()

	zw, err := zstd.NewWriter(f)
	if err != nil {
		t.Fatalf("create zstd writer: %v", err)
	}
	defer zw.Close()

	tw := tar.NewWriter(zw)
	defer tw.Close()

	// Create parent directory.
	tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir,
		Name:     "bin/",
		Mode:     0o755,
	})

	tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     name,
		Mode:     0o755,
		Size:     int64(len(content)),
	})
	tw.Write([]byte(content))

	return path
}

// createTarZstdWithFiles writes a tar.zst archive containing
// each (path, content) pair. Used to model archives that ship
// extra metadata (.gale-deps.toml) alongside a binary.
func createTarZstdWithFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pkg.tar.zst")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create tar.zst: %v", err)
	}
	defer f.Close()
	zw, err := zstd.NewWriter(f)
	if err != nil {
		t.Fatalf("create zstd writer: %v", err)
	}
	defer zw.Close()
	tw := tar.NewWriter(zw)
	defer tw.Close()
	for name, content := range files {
		if strings.HasSuffix(name, "/") {
			tw.WriteHeader(&tar.Header{
				Typeflag: tar.TypeDir,
				Name:     name,
				Mode:     0o755,
			})
			continue
		}
		tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     name,
			Mode:     0o755,
			Size:     int64(len(content)),
		})
		tw.Write([]byte(content))
	}
	return path
}

func TestInstallBinaryPreservesArchiveDepsMetadata(t *testing.T) {
	// Issue: the installer used to overwrite an archive's
	// .gale-deps.toml with locally-resolved versions. Now it
	// preserves whatever the archive shipped (CI's exact
	// linked versions) and only computes locally as a
	// fallback for archives built before the build-time emit.
	archiveDeps := "[[deps]]\n  name = \"openssl\"\n  version = \"3.4.1\"\n  revision = \"42\"\n"
	tarzst := createTarZstdWithFiles(t, map[string]string{
		"bin/":            "",
		"bin/testpkg":     "#!/bin/sh\necho ok",
		".gale-deps.toml": archiveDeps,
	})
	hash := hashFile(t, tarzst)
	blobData, err := os.ReadFile(tarzst)
	if err != nil {
		t.Fatalf("read tar.zst: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Write(blobData)
		},
	))
	defer srv.Close()
	restore := download.SetHTTPClient(srv.Client())
	defer restore()

	storeRoot := t.TempDir()
	inst := &Installer{Store: store.NewStore(storeRoot)}

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "1.0"},
		Source:  recipe.Source{URL: "http://unused", SHA256: "unused"},
		Binary: map[string]recipe.Binary{
			fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH): {
				URL:    fmt.Sprintf("%s/testpkg-1.0.tar.zst", srv.URL),
				SHA256: hash,
				Trust:  recipe.TrustSHA256Only,
			},
		},
	}

	if _, err := inst.Install(r); err != nil {
		t.Fatalf("Install: %v", err)
	}

	storeDeps := filepath.Join(storeRoot,
		"testpkg", "1.0-1", ".gale-deps.toml")
	got, err := os.ReadFile(storeDeps)
	if err != nil {
		t.Fatalf("read store deps file: %v", err)
	}
	if string(got) != archiveDeps {
		t.Fatalf(".gale-deps.toml was overwritten\n got: %q\nwant: %q",
			string(got), archiveDeps)
	}
}

// TestInstallBinaryWritesEmptyDepsForZeroDepRecipe pins the
// fresh-vs-legacy distinction for the staleness check. A
// zero-dep recipe whose binary archive doesn't ship a
// .gale-deps.toml must still get an (empty) metadata file
// written at install time. Without it, IsStale's
// "missing file = soft-migration stale" heuristic treats a
// fresh install as if it predated the revision system and
// `gale sync` re-installs forever. The legacy-stale path is
// reserved for installs that pre-date this metadata; a fresh
// install with known-empty deps must record that fact.
func TestInstallBinaryWritesEmptyDepsForZeroDepRecipe(t *testing.T) {
	binContent := "#!/bin/sh\necho ok"
	tarzst := createTarZstdWithFiles(t, map[string]string{
		"bin/":        "",
		"bin/testpkg": binContent,
	})
	hash := hashFile(t, tarzst)
	blobData, err := os.ReadFile(tarzst)
	if err != nil {
		t.Fatalf("read tar.zst: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Write(blobData)
		},
	))
	defer srv.Close()
	restore := download.SetHTTPClient(srv.Client())
	defer restore()

	storeRoot := t.TempDir()
	inst := &Installer{Store: store.NewStore(storeRoot)}

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "1.0"},
		Source:  recipe.Source{URL: "http://unused", SHA256: "unused"},
		Binary: map[string]recipe.Binary{
			fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH): {
				URL:    fmt.Sprintf("%s/testpkg-1.0.tar.zst", srv.URL),
				SHA256: hash,
				Trust:  recipe.TrustSHA256Only,
			},
		},
	}

	if _, err := inst.Install(r); err != nil {
		t.Fatalf("Install: %v", err)
	}

	storeDeps := filepath.Join(storeRoot,
		"testpkg", "1.0-1", ".gale-deps.toml")
	if _, err := os.Stat(storeDeps); err != nil {
		t.Fatalf(".gale-deps.toml not written for zero-dep "+
			"recipe: %v", err)
	}

	md, err := ReadDepsMetadata(filepath.Join(storeRoot,
		"testpkg", "1.0-1"))
	if err != nil {
		t.Fatalf("read deps metadata: %v", err)
	}
	if len(md.Deps) != 0 {
		t.Errorf("Deps = %v, want empty", md.Deps)
	}
}

// --- Install cached ---

func TestInstallCachedReturnsWithoutDownload(t *testing.T) {
	storeRoot := t.TempDir()
	s := store.NewStore(storeRoot)

	// Pre-install the package.
	dir, err := s.Create("mypkg", "3.0")
	if err != nil {
		t.Fatalf("create store dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatalf("create bin: %v", err)
	}

	inst := &Installer{Store: s}

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "mypkg", Version: "3.0"},
		Source:  recipe.Source{URL: "http://should-not-be-called", SHA256: "bad"},
	}

	result, err := inst.Install(r)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if result.Method != "cached" {
		t.Errorf("Method = %q, want %q", result.Method, "cached")
	}
	if result.Name != "mypkg" {
		t.Errorf("Name = %q, want %q", result.Name, "mypkg")
	}
	if result.Version != "3.0" {
		t.Errorf("Version = %q, want %q", result.Version, "3.0")
	}
}

// --- Install binary (non-GHCR) via httptest ---

func TestInstallBinaryNonGHCR(t *testing.T) {
	// Create a tar.zst with bin/tool.
	binContent := "#!/bin/sh\necho direct-binary"
	tarzst := createTestTarZstd(t, "bin/tool", binContent)
	hash := hashFile(t, tarzst)
	blobData, err := os.ReadFile(tarzst)
	if err != nil {
		t.Fatalf("read tar.zst: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Write(blobData)
		},
	))
	defer srv.Close()

	storeRoot := t.TempDir()
	inst := &Installer{
		Store: store.NewStore(storeRoot),
	}

	// Plain HTTP URL (not /v2/.../blobs/) triggers the
	// non-GHCR download path. Requires trust = "sha256-only"
	// to opt out of attestation enforcement.
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "tool", Version: "1.0"},
		Source:  recipe.Source{URL: "http://unused", SHA256: "unused"},
		Binary: map[string]recipe.Binary{
			fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH): {
				URL:    srv.URL + "/tool-1.0.tar.zst",
				SHA256: hash,
				Trust:  recipe.TrustSHA256Only,
			},
		},
	}

	result, err := inst.Install(r)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if result.Method != "binary" {
		t.Errorf("Method = %q, want %q", result.Method, "binary")
	}
	if result.SHA256 != hash {
		t.Errorf("SHA256 = %q, want %q", result.SHA256, hash)
	}

	// Verify file extracted to store.
	storeBin := filepath.Join(storeRoot,
		"tool", "1.0-1", "bin", "tool")
	got, err := os.ReadFile(storeBin)
	if err != nil {
		t.Fatalf("read store binary: %v", err)
	}
	if string(got) != binContent {
		t.Errorf("binary content = %q, want %q",
			string(got), binContent)
	}
}

// --- Install binary failure is reported via BinaryFallbackLog ---

func TestInstallBinaryFailureLoggedToFallbackWriter(t *testing.T) {
	// When a binary install fails (here: 404 from the
	// configured URL), the installer must record the
	// reason on BinaryFallbackLog so users can see why
	// they're getting a source build instead of a binary.
	srcTar := createTestSourceTarGz(t)
	srcHash := hashFile(t, srcTar)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/source.tar.gz" {
				http.ServeFile(w, r, srcTar)
				return
			}
			http.NotFound(w, r)
		},
	))
	defer srv.Close()

	var logBuf bytes.Buffer
	inst := &Installer{
		Store:             store.NewStore(t.TempDir()),
		BinaryFallbackLog: &logBuf,
	}

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "pkg", Version: "1.0"},
		Source: recipe.Source{
			URL:    srv.URL + "/source.tar.gz",
			SHA256: srcHash,
		},
		Binary: map[string]recipe.Binary{
			fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH): {
				URL:    srv.URL + "/missing-binary.tar.zst",
				SHA256: "0000000000000000000000000000000000000000000000000000000000000000",
				// This test covers the 404-fetch fallback path,
				// not the trust-policy rejection path. Opt out
				// of attestation so the fetch failure is what
				// triggers the fallback (not policy).
				Trust: recipe.TrustSHA256Only,
			},
		},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin",
				"echo '#!/bin/sh' > $PREFIX/bin/pkg",
				"chmod +x $PREFIX/bin/pkg",
			},
		},
	}

	result, err := inst.Install(r)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if result.Method != MethodSource {
		t.Fatalf("Method = %q, want source", result.Method)
	}

	got := logBuf.String()
	if got == "" {
		t.Fatal("expected fallback log to record binary " +
			"failure, got empty buffer")
	}
	if !strings.Contains(got, "pkg") ||
		!strings.Contains(got, "1.0") {
		t.Errorf("fallback log missing package identity:\n%s",
			got)
	}
	if !strings.Contains(got, "fetch") &&
		!strings.Contains(got, "404") {
		t.Errorf("fallback log missing failure reason:\n%s",
			got)
	}
}

// --- Install binary SHA256 mismatch falls back to source ---

func TestInstallBinaryBadHashFallsBackToSource(t *testing.T) {
	// Create a tar.zst to serve as the "binary" download.
	tarzst := createTestTarZstd(t, "bin/pkg", "binary")
	blobData, err := os.ReadFile(tarzst)
	if err != nil {
		t.Fatalf("read tar.zst: %v", err)
	}

	// Create source tar.gz for the fallback build.
	srcTar := createTestSourceTarGz(t)
	srcHash := hashFile(t, srcTar)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/binary.tar.zst":
				w.Write(blobData)
			case "/source.tar.gz":
				http.ServeFile(w, r, srcTar)
			default:
				http.NotFound(w, r)
			}
		},
	))
	defer srv.Close()

	storeRoot := t.TempDir()
	inst := &Installer{
		Store: store.NewStore(storeRoot),
	}

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "pkg", Version: "1.0"},
		Source: recipe.Source{
			URL:    srv.URL + "/source.tar.gz",
			SHA256: srcHash,
		},
		Binary: map[string]recipe.Binary{
			fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH): {
				URL:    srv.URL + "/binary.tar.zst",
				SHA256: "0000000000000000000000000000000000000000000000000000000000000000",
				// Isolate the bad-hash fallback path from the
				// trust-policy path.
				Trust: recipe.TrustSHA256Only,
			},
		},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin",
				"echo '#!/bin/sh' > $PREFIX/bin/pkg",
				"chmod +x $PREFIX/bin/pkg",
			},
		},
	}

	result, err := inst.Install(r)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if result.Method != "source" {
		t.Errorf("Method = %q, want %q", result.Method, "source")
	}
}

// --- InstallLocal always rebuilds ---

func TestInstallLocalRebuildsWhenAlreadyInstalled(t *testing.T) {
	storeRoot := t.TempDir()
	s := store.NewStore(storeRoot)

	// Pre-install version 1.0.
	dir, err := s.Create("localpkg", "1.0")
	if err != nil {
		t.Fatalf("create store dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatalf("create bin: %v", err)
	}

	// Create a local source directory.
	srcDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(srcDir, "README"),
		[]byte("hello"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	inst := &Installer{Store: s}

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "localpkg", Version: "1.0"},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin && echo '#!/bin/sh' > $PREFIX/bin/localpkg && chmod +x $PREFIX/bin/localpkg",
			},
		},
	}

	result, err := inst.InstallLocal(r, srcDir)
	if err != nil {
		t.Fatalf("InstallLocal: %v", err)
	}
	// Should rebuild, not return cached.
	if result.Method != "source" {
		t.Errorf("Method = %q, want %q", result.Method, "source")
	}
}

// --- InstallLocal builds from directory ---

func TestInstallLocalBuildsFromSource(t *testing.T) {
	// Create a local source directory with a placeholder file.
	sourceDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(sourceDir, "README"),
		[]byte("local source"), 0o644,
	); err != nil {
		t.Fatalf("write README: %v", err)
	}

	storeRoot := t.TempDir()
	inst := &Installer{
		Store: store.NewStore(storeRoot),
	}

	r := &recipe.Recipe{
		Package: recipe.Package{
			Name: "localbuild", Version: "0.1",
		},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin",
				"echo '#!/bin/sh\necho local' > $PREFIX/bin/localbuild",
				"chmod +x $PREFIX/bin/localbuild",
			},
		},
	}

	result, err := inst.InstallLocal(r, sourceDir)
	if err != nil {
		t.Fatalf("InstallLocal: %v", err)
	}
	if result.Method != "source" {
		t.Errorf("Method = %q, want %q", result.Method, "source")
	}
	if result.SHA256 == "" {
		t.Error("SHA256 should be populated after build")
	}

	// Verify binary extracted to store.
	storeBin := filepath.Join(storeRoot,
		"localbuild", "0.1-1", "bin", "localbuild")
	if _, err := os.Stat(storeBin); err != nil {
		t.Errorf("binary not in store: %v", err)
	}
}

func TestReplaceStoreDirPreservesExistingOnRenameFailure(t *testing.T) {
	storeRoot := t.TempDir()
	storeDir := filepath.Join(storeRoot, "pkg", "1.0")
	if err := os.MkdirAll(filepath.Join(storeDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldBin := filepath.Join(storeDir, "bin", "pkg")
	if err := os.WriteFile(oldBin, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	buildDir := filepath.Join(storeRoot, "pkg", ".build-new")
	if err := os.MkdirAll(filepath.Join(buildDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(buildDir, "bin", "pkg"), []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}

	origRename := renameDir
	renameDir = func(oldPath, newPath string) error {
		if oldPath == buildDir && newPath == storeDir {
			return fmt.Errorf("boom")
		}
		return origRename(oldPath, newPath)
	}
	defer func() { renameDir = origRename }()

	err := replaceStoreDir(storeDir, buildDir)
	if err == nil {
		t.Fatal("expected replaceStoreDir error")
	}

	data, err := os.ReadFile(oldBin)
	if err != nil {
		t.Fatalf("read old binary after failed replace: %v", err)
	}
	if string(data) != "old" {
		t.Fatalf("old binary content = %q, want %q", string(data), "old")
	}
	if _, err := os.Stat(buildDir); err != nil {
		t.Fatalf("buildDir should remain for inspection after failed replace: %v", err)
	}
}

func TestInstallLocalPreservesExistingStoreOnReplaceFailure(t *testing.T) {
	storeRoot := t.TempDir()
	s := store.NewStore(storeRoot)

	// Pre-create the canonical <version>-<revision> dir
	// that InstallLocal would target, since the test's
	// rename hook matches against newPath == storeDir.
	storeDir, err := s.Create("localpkg", "1.0-1")
	if err != nil {
		t.Fatalf("create store dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(storeDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldBin := filepath.Join(storeDir, "bin", "localpkg")
	if err := os.WriteFile(oldBin, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "README"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	inst := &Installer{Store: s}
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "localpkg", Version: "1.0"},
		Build: recipe.Build{Steps: []string{
			"mkdir -p $PREFIX/bin && echo new > $PREFIX/bin/localpkg && chmod +x $PREFIX/bin/localpkg",
		}},
	}

	origRename := renameDir
	renameDir = func(oldPath, newPath string) error {
		if strings.Contains(filepath.Base(oldPath), ".build-") && newPath == storeDir {
			return fmt.Errorf("boom")
		}
		return origRename(oldPath, newPath)
	}
	defer func() { renameDir = origRename }()

	_, err = inst.InstallLocal(r, srcDir)
	if err == nil {
		t.Fatal("expected InstallLocal error")
	}

	data, err := os.ReadFile(oldBin)
	if err != nil {
		t.Fatalf("read old binary after failed InstallLocal: %v", err)
	}
	if string(data) != "old" {
		t.Fatalf("old binary content = %q, want %q", string(data), "old")
	}
}

// --- Install SHA256 populated ---

func TestInstallResultSHA256Populated(t *testing.T) {
	binContent := "#!/bin/sh\necho sha-test"
	tarzst := createTestTarZstd(t, "bin/sha", binContent)
	hash := hashFile(t, tarzst)
	blobData, err := os.ReadFile(tarzst)
	if err != nil {
		t.Fatalf("read tar.zst: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Write(blobData)
		},
	))
	defer srv.Close()

	storeRoot := t.TempDir()
	inst := &Installer{
		Store: store.NewStore(storeRoot),
	}

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "sha", Version: "1.0"},
		Source:  recipe.Source{URL: "http://unused", SHA256: "unused"},
		Binary: map[string]recipe.Binary{
			fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH): {
				URL:    srv.URL + "/sha.tar.zst",
				SHA256: hash,
				Trust:  recipe.TrustSHA256Only,
			},
		},
	}

	result, err := inst.Install(r)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if result.SHA256 != hash {
		t.Errorf("SHA256 = %q, want %q", result.SHA256, hash)
	}
}

// --- Install ManifestDigest populated ---

// TestInstallResultManifestDigestPopulated pins that a binary
// install carries the recipe's OCI manifest digest through to
// the InstallResult, so callers (sync, install) can persist it
// in gale.lock for digest-pinned verification.
func TestInstallResultManifestDigestPopulated(t *testing.T) {
	const digest = "sha256:" +
		"4ae2cd0a430a6a2729ba33aa1cfdf1cbeff58e95b2c1fb4e6f9d8f0c8e92c4ab"

	binContent := "#!/bin/sh\necho digest-test"
	tarzst := createTestTarZstd(t, "bin/dig", binContent)
	hash := hashFile(t, tarzst)
	blobData, err := os.ReadFile(tarzst)
	if err != nil {
		t.Fatalf("read tar.zst: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Write(blobData)
		},
	))
	defer srv.Close()

	storeRoot := t.TempDir()
	inst := &Installer{
		Store: store.NewStore(storeRoot),
	}

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "dig", Version: "1.0"},
		Source:  recipe.Source{URL: "http://unused", SHA256: "unused"},
		Binary: map[string]recipe.Binary{
			fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH): {
				URL:            srv.URL + "/dig.tar.zst",
				SHA256:         hash,
				Trust:          recipe.TrustSHA256Only,
				ManifestDigest: digest,
			},
		},
	}

	result, err := inst.Install(r)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if result.ManifestDigest != digest {
		t.Errorf("ManifestDigest = %q, want %q",
			result.ManifestDigest, digest)
	}
}

// --- extractBuild ---

func TestExtractBuildExtractsArchive(t *testing.T) {
	// Create a tar.zst with known content.
	tarzst := createTestTarZstd(t, "bin/hello", "world")
	hash := hashFile(t, tarzst)

	// extractBuildTo derives storeRoot via filepath.Dir twice,
	// then storeGenLockPath walks one more level up. A bare
	// t.TempDir() works on macOS (deep /var/folders path) but
	// fails on Linux (/tmp/X/001 walks to / for the lock).
	// Mirror the production <galeDir>/pkg/<name>/<version>
	// layout.
	storeDir := filepath.Join(t.TempDir(), "pkg", "testpkg", "1.0")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatalf("mkdir storeDir: %v", err)
	}
	result := &build.BuildResult{
		Archive: tarzst,
		SHA256:  hash,
	}

	if err := extractBuild(result, storeDir, nil); err != nil {
		t.Fatalf("extractBuild: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(storeDir, "bin", "hello"))
	if err != nil {
		t.Fatalf("read extracted file: %v", err)
	}
	if string(got) != "world" {
		t.Errorf("content = %q, want %q", string(got), "world")
	}
}

func TestExtractBuildBadArchiveReturnsError(t *testing.T) {
	// See note in TestExtractBuildExtractsArchive on the
	// store-gen lock path depth.
	storeDir := filepath.Join(t.TempDir(), "pkg", "testpkg", "1.0")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatalf("mkdir storeDir: %v", err)
	}
	result := &build.BuildResult{
		Archive: "/nonexistent/archive.tar.zst",
		SHA256:  "abc",
	}

	err := extractBuild(result, storeDir, nil)
	if err == nil {
		t.Fatal("expected error for nonexistent archive")
	}
	if !strings.Contains(err.Error(), "extract build output") {
		t.Errorf("error = %q, want to contain %q",
			err.Error(), "extract build output")
	}
}

// --- isGHCR ---

func TestIsGHCR(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{
			name: "ghcr.io host",
			url:  "https://ghcr.io/v2/kelp/gale-recipes/jq/blobs/sha256:abc",
			want: true,
		},
		{
			name: "OCI blob pattern on other host",
			url:  "http://localhost:8080/v2/owner/repo/blobs/sha256:abc",
			want: false,
		},
		{
			name: "plain HTTP URL",
			url:  "https://example.com/releases/tool-1.0.tar.zst",
			want: false,
		},
		{
			name: "invalid URL",
			url:  "://bad",
			want: false,
		},
		{
			name: "v2 path without blobs",
			url:  "http://localhost/v2/owner/repo/manifests/latest",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isGHCR(tt.url)
			if got != tt.want {
				t.Errorf("isGHCR(%q) = %v, want %v",
					tt.url, got, tt.want)
			}
		})
	}
}

// --- repoFromURL ---

func TestRepoFromURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "standard GHCR blob URL",
			url:  "https://ghcr.io/v2/kelp/gale-recipes/jq/blobs/sha256:abc123",
			want: "kelp/gale-recipes/jq",
		},
		{
			name: "test server URL",
			url:  "http://localhost:8080/v2/owner/repo/blobs/sha256:def456",
			want: "owner/repo",
		},
		{
			name: "invalid URL",
			url:  "://bad",
			want: "",
		},
		{
			name: "no blobs segment",
			url:  "https://ghcr.io/v2/kelp/repo/manifests/latest",
			want: "kelp/repo/manifests/latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := repoFromURL(tt.url)
			if got != tt.want {
				t.Errorf("repoFromURL(%q) = %q, want %q",
					tt.url, got, tt.want)
			}
		})
	}
}

// --- SourceOnly skips binary and builds from source ---

func TestInstallSourceOnlySkipsBinary(t *testing.T) {
	srcTar := createTestSourceTarGz(t)
	srcHash := hashFile(t, srcTar)

	binaryRequested := false
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/binary.tar.zst" {
				binaryRequested = true
				http.Error(w, "should not be called",
					http.StatusInternalServerError)
				return
			}
			http.ServeFile(w, r, srcTar)
		},
	))
	defer srv.Close()

	storeRoot := t.TempDir()
	inst := &Installer{
		Store:      store.NewStore(storeRoot),
		SourceOnly: true,
	}

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "pkg", Version: "1.0"},
		Source: recipe.Source{
			URL:    srv.URL + "/source.tar.gz",
			SHA256: srcHash,
		},
		Binary: map[string]recipe.Binary{
			fmt.Sprintf("%s-%s",
				runtime.GOOS, runtime.GOARCH): {
				URL:    srv.URL + "/binary.tar.zst",
				SHA256: "deadbeef",
			},
		},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin",
				"echo '#!/bin/sh' > $PREFIX/bin/pkg",
				"chmod +x $PREFIX/bin/pkg",
			},
		},
	}

	result, err := inst.Install(r)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if result.Method != "source" {
		t.Errorf("Method = %q, want %q",
			result.Method, "source")
	}
	if binaryRequested {
		t.Error("binary endpoint was called despite SourceOnly")
	}
}

// --- Install with no binary section builds from source ---

func TestInstallNoBinarySectionBuildsSource(t *testing.T) {
	srcTar := createTestSourceTarGz(t)
	srcHash := hashFile(t, srcTar)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, srcTar)
		},
	))
	defer srv.Close()

	storeRoot := t.TempDir()
	inst := &Installer{
		Store: store.NewStore(storeRoot),
	}

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "srconly", Version: "1.0"},
		Source: recipe.Source{
			URL:    srv.URL + "/source.tar.gz",
			SHA256: srcHash,
		},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin",
				"echo '#!/bin/sh' > $PREFIX/bin/srconly",
				"chmod +x $PREFIX/bin/srconly",
			},
		},
	}

	result, err := inst.Install(r)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if result.Method != "source" {
		t.Errorf("Method = %q, want %q", result.Method, "source")
	}
	if result.SHA256 == "" {
		t.Error("SHA256 should be populated after source build")
	}
}

// --- BUG-2: Map aliasing in InstallBuildDeps recipe copy ---

func TestInstallBuildDepsDeepCopiesMaps(t *testing.T) {
	storeRoot := t.TempDir()
	s := store.NewStore(storeRoot)

	// Pre-populate cmake in the store so Install returns
	// "cached" without trying to download/build.
	cmakeBin := filepath.Join(storeRoot, "cmake", "1.0", "bin")
	if err := os.MkdirAll(cmakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(cmakeBin, "cmake"),
		[]byte("fake"), 0o755,
	); err != nil {
		t.Fatal(err)
	}

	inst := &Installer{
		Store: s,
		Resolver: func(name string) (*recipe.Recipe, error) {
			return &recipe.Recipe{
				Package: recipe.Package{
					Name:    name,
					Version: "1.0",
				},
			}, nil
		},
	}

	r := &recipe.Recipe{
		Package: recipe.Package{
			Name:    "myapp",
			Version: "2.0",
		},
		Build: recipe.Build{
			System: "cmake",
			Steps:  []string{"cmake ..", "make"},
			Platform: map[string]recipe.PlatformBuild{
				"darwin-arm64": {Steps: []string{"make"}},
			},
		},
		Binary: map[string]recipe.Binary{
			"darwin-arm64": {
				URL:    "https://example.com/foo.tar.zst",
				SHA256: "abc123",
			},
		},
		Dependencies: recipe.Dependencies{
			Build: []string{},
		},
	}

	_, err := inst.InstallBuildDeps(r)
	if err != nil {
		t.Fatalf("InstallBuildDeps: %v", err)
	}

	// Test the copy function directly: mutations to the
	// copy must not affect the original.
	copied := copyRecipeForDeps(r, r.Dependencies)
	copied.Build.Platform["linux-amd64"] = recipe.PlatformBuild{
		Steps: []string{"new"},
	}
	copied.Binary["linux-amd64"] = recipe.Binary{
		URL: "https://example.com/new.tar.zst",
	}

	if len(r.Build.Platform) != 1 {
		t.Errorf("original Build.Platform mutated: got %d entries, want 1",
			len(r.Build.Platform))
	}
	if len(r.Binary) != 1 {
		t.Errorf("original Binary mutated: got %d entries, want 1",
			len(r.Binary))
	}
}

// --- BUG-1: File-based locking for concurrent Install ---

func TestLockPackageSerializesConcurrentAccess(t *testing.T) {
	storeRoot := t.TempDir()

	// Acquire a lock on a package.
	unlock, err := lockPackage(storeRoot, "jq", "1.7")
	if err != nil {
		t.Fatalf("lockPackage: %v", err)
	}

	// Try to acquire the same lock in a goroutine.
	done := make(chan struct{})
	go func() {
		unlock2, err := lockPackage(storeRoot, "jq", "1.7")
		if err != nil {
			t.Errorf("second lockPackage: %v", err)
			close(done)
			return
		}
		unlock2()
		close(done)
	}()

	// The goroutine should be blocked.
	select {
	case <-done:
		t.Fatal("second lock acquired before first was released")
	default:
	}

	unlock()
	<-done
}

func TestLockPackageDifferentPackagesNotBlocked(t *testing.T) {
	storeRoot := t.TempDir()

	unlock1, err := lockPackage(storeRoot, "jq", "1.7")
	if err != nil {
		t.Fatalf("lockPackage jq: %v", err)
	}
	defer unlock1()

	unlock2, err := lockPackage(storeRoot, "fd", "9.0")
	if err != nil {
		t.Fatalf("lockPackage fd: %v", err)
	}
	defer unlock2()
}

func TestLockPackagePersistsAfterUnlock(t *testing.T) {
	storeRoot := t.TempDir()

	unlock, err := lockPackage(storeRoot, "jq", "1.7")
	if err != nil {
		t.Fatalf("lockPackage: %v", err)
	}

	lockPath := filepath.Join(storeRoot, "jq", "1.7.lock")
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("lock file should exist while held: %v", err)
	}

	unlock()

	// Lock file must persist so all contenders share the
	// same inode. Removing it causes an inode-split race.
	if _, err := os.Stat(lockPath); err != nil {
		t.Error("lock file should persist after unlock")
	}
}

// --- H7: store-gen lock serializes install vs. generation.Build ---

// TestInstallBlocksOnStoreGenLock pre-acquires the generation
// lock that a concurrent `gale sync` (via generation.Build)
// would hold, then kicks off an install. The install must wait
// until the lock is released — otherwise its store writes could
// interleave with a concurrent gen rebuild and leave the gen
// symlinking a half-extracted package. Mirrors the pattern in
// `generation_test.go:TestBuildWaitsForGenerationLock`.
func TestInstallBlocksOnStoreGenLock(t *testing.T) {
	srcTar := createTestSourceTarGz(t)
	hash := hashFile(t, srcTar)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, srcTar)
		},
	))
	defer srv.Close()

	// Place the store under a parent dir so filepath.Dir
	// (storeRoot) is a real directory that can hold the lock.
	galeDir := t.TempDir()
	storeRoot := filepath.Join(galeDir, "pkg")
	if err := os.MkdirAll(storeRoot, 0o755); err != nil {
		t.Fatalf("mkdir storeRoot: %v", err)
	}

	// Pre-acquire the lock generation.Build would hold.
	// Install must block on this lock for its store-write
	// critical section.
	lockPath := filepath.Join(galeDir, "generation.lock")
	unlock, err := filelock.Acquire(lockPath)
	if err != nil {
		t.Fatalf("Acquire store-gen lock: %v", err)
	}
	defer unlock()

	inst := &Installer{Store: store.NewStore(storeRoot)}
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "1.0"},
		Source:  recipe.Source{URL: srv.URL + "/source.tar.gz", SHA256: hash},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin",
				"echo '#!/bin/sh\necho hello' > $PREFIX/bin/testpkg",
				"chmod +x $PREFIX/bin/testpkg",
			},
		},
	}

	done := make(chan error, 1)
	go func() {
		_, err := inst.Install(r)
		done <- err
	}()

	// Install is allowed to do pre-lock work (network fetch,
	// source build). Give it time; it must stop at the
	// store-write critical section and wait for the lock.
	// A timeout here means the install *did* grab the lock
	// and finish, which is fine — we only care that the
	// lock-protected phase waited. To distinguish, confirm
	// that the store dir has NOT been populated with final
	// content while the lock is held.
	select {
	case err := <-done:
		t.Fatalf(
			"Install completed while store-gen lock held: %v", err,
		)
	case <-time.After(500 * time.Millisecond):
		// Install is blocked on the lock. Good.
	}

	// Store dir must not contain a finalized bin/ while
	// the gen lock is held — the critical section covers
	// extract + finalize.
	binPath := filepath.Join(
		storeRoot, "testpkg", "1.0-1", "bin", "testpkg",
	)
	if _, err := os.Stat(binPath); err == nil {
		t.Fatal(
			"install wrote bin/testpkg while store-gen lock held",
		)
	}

	unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Install error after lock release: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Install did not complete after lock release")
	}

	// After release, the install should have finalized.
	if _, err := os.Stat(binPath); err != nil {
		t.Errorf("binary not in store after install: %v", err)
	}
}

// TestInstallReleasesStoreGenLock confirms the install does
// not leak the store-gen lock: after a successful install,
// a subsequent Acquire on the same lock path must succeed
// without blocking. Guards against an unlock leak in the
// critical-section wrapper.
func TestInstallReleasesStoreGenLock(t *testing.T) {
	srcTar := createTestSourceTarGz(t)
	hash := hashFile(t, srcTar)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, srcTar)
		},
	))
	defer srv.Close()

	galeDir := t.TempDir()
	storeRoot := filepath.Join(galeDir, "pkg")
	if err := os.MkdirAll(storeRoot, 0o755); err != nil {
		t.Fatalf("mkdir storeRoot: %v", err)
	}

	inst := &Installer{Store: store.NewStore(storeRoot)}
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "1.0"},
		Source:  recipe.Source{URL: srv.URL + "/source.tar.gz", SHA256: hash},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin",
				"echo '#!/bin/sh\necho hello' > $PREFIX/bin/testpkg",
				"chmod +x $PREFIX/bin/testpkg",
			},
		},
	}

	if _, err := inst.Install(r); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Post-install: the store-gen lock should be free.
	// Acquire-with-timeout to surface a leaked lock.
	lockPath := filepath.Join(galeDir, "generation.lock")
	acquired := make(chan struct{})
	go func() {
		unlock, err := filelock.Acquire(lockPath)
		if err != nil {
			t.Errorf("Acquire after install: %v", err)
			close(acquired)
			return
		}
		close(acquired)
		unlock()
	}()

	select {
	case <-acquired:
		// Good: lock was free.
	case <-time.After(2 * time.Second):
		t.Fatal("store-gen lock not released after install")
	}
}

// TestInstallLocalBlocksOnStoreGenLock asserts InstallLocal
// also honors the store-gen lock. The replaceStoreDir rename
// plus farm.Populate inside extractBuild must be serialized
// with generation.Build; otherwise a local install and a
// concurrent sync can race on the farm.
func TestInstallLocalBlocksOnStoreGenLock(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := filepath.Join(galeDir, "pkg")
	if err := os.MkdirAll(storeRoot, 0o755); err != nil {
		t.Fatalf("mkdir storeRoot: %v", err)
	}

	sourceDir := t.TempDir()
	// Write a trivial source that produces bin/testpkg.
	if err := os.WriteFile(
		filepath.Join(sourceDir, "README"),
		[]byte("placeholder"), 0o644,
	); err != nil {
		t.Fatalf("write source: %v", err)
	}

	// Pre-acquire the lock.
	lockPath := filepath.Join(galeDir, "generation.lock")
	unlock, err := filelock.Acquire(lockPath)
	if err != nil {
		t.Fatalf("Acquire store-gen lock: %v", err)
	}
	defer unlock()

	inst := &Installer{Store: store.NewStore(storeRoot)}
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "1.0"},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin",
				"echo '#!/bin/sh\necho local' > $PREFIX/bin/testpkg",
				"chmod +x $PREFIX/bin/testpkg",
			},
		},
	}

	done := make(chan error, 1)
	go func() {
		_, err := inst.InstallLocal(r, sourceDir)
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf(
			"InstallLocal completed while store-gen lock held: %v",
			err,
		)
	case <-time.After(500 * time.Millisecond):
		// Install is blocked on the lock. Good.
	}

	// The canonical store dir must not be populated with
	// bin/ while the lock is held.
	binPath := filepath.Join(
		storeRoot, "testpkg", "1.0-1", "bin", "testpkg",
	)
	if _, err := os.Stat(binPath); err == nil {
		t.Fatal(
			"InstallLocal wrote bin/testpkg while store-gen lock held",
		)
	}

	unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("InstallLocal error after release: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("InstallLocal did not complete after lock release")
	}

	if _, err := os.Stat(binPath); err != nil {
		t.Errorf("binary not in store after InstallLocal: %v", err)
	}
}

// --- Reassigned supply-chain BUG-3: isGHCR credential leak ---

func TestIsGHCRRejectsNonGHCRHosts(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{
			name: "ghcr.io host",
			url:  "https://ghcr.io/v2/owner/repo/blobs/sha256:abc",
			want: true,
		},
		{
			name: "evil host with GHCR path pattern",
			url:  "https://evil.com/v2/owner/repo/blobs/sha256:abc",
			want: false,
		},
		{
			name: "subdomain of ghcr.io",
			url:  "https://sub.ghcr.io/v2/owner/repo/blobs/sha256:abc",
			want: false,
		},
		{
			name: "evil host pretending to be ghcr.io",
			url:  "https://notghcr.io/v2/owner/repo/blobs/sha256:abc",
			want: false,
		},
		{
			name: "ghcr.io non-v2 path",
			url:  "https://ghcr.io/some/other/path",
			want: true,
		},
		{
			name: "empty url",
			url:  "",
			want: false,
		},
		{
			name: "localhost with v2 blobs",
			url:  "https://localhost/v2/owner/repo/blobs/sha256:abc",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isGHCR(tt.url)
			if got != tt.want {
				t.Errorf("isGHCR(%q) = %v, want %v",
					tt.url, got, tt.want)
			}
		})
	}
}

// TestReinstallRebuildsWhenCanonicalPopulated verifies that
// Reinstall actually rebuilds even when the canonical store
// dir has content from a previous install. This is the path
// sync takes for stale packages: if Reinstall short-circuits,
// .gale-deps.toml never gets refreshed and sync loops forever
// reporting the same staleness.
func TestReinstallRebuildsWhenCanonicalPopulated(t *testing.T) {
	srcTar := createTestSourceTarGz(t)
	hash := hashFile(t, srcTar)
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, srcTar)
		},
	))
	defer srv.Close()

	storeRoot := t.TempDir()
	inst := &Installer{Store: store.NewStore(storeRoot)}

	// Pre-populate the canonical dir with stale content that
	// simulates an install from an older recipe revision. The
	// Reinstall must wipe it before writing the fresh build.
	canonicalDir := filepath.Join(storeRoot, "testpkg", "1.0-1")
	if err := os.MkdirAll(canonicalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	staleMarker := filepath.Join(canonicalDir, "STALE-MARKER")
	if err := os.WriteFile(
		staleMarker, []byte("stale"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "1.0"},
		Source: recipe.Source{
			URL: srv.URL + "/source.tar.gz", SHA256: hash,
		},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin",
				"echo '#!/bin/sh\necho fresh' > $PREFIX/bin/testpkg",
				"chmod +x $PREFIX/bin/testpkg",
			},
		},
	}

	result, err := inst.Reinstall(r)
	if err != nil {
		t.Fatalf("Reinstall error: %v", err)
	}
	if result.Method == MethodCached {
		t.Errorf("Reinstall returned MethodCached — should have " +
			"rebuilt the populated canonical dir")
	}
	// Stale marker must be gone — a real reinstall wipes the dir.
	if _, err := os.Stat(staleMarker); !os.IsNotExist(err) {
		t.Errorf("STALE-MARKER still present after Reinstall — " +
			"canonical dir was not wiped")
	}
	// Fresh binary must exist in the rebuilt canonical dir.
	freshBin := filepath.Join(canonicalDir, "bin", "testpkg")
	if _, err := os.Stat(freshBin); err != nil {
		t.Errorf("fresh binary missing: %v", err)
	}
}

func TestReinstallPreservesExistingStoreOnBuildFailure(t *testing.T) {
	srcTar := createTestSourceTarGz(t)
	hash := hashFile(t, srcTar)
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, srcTar)
		},
	))
	defer srv.Close()

	storeRoot := t.TempDir()
	inst := &Installer{Store: store.NewStore(storeRoot)}

	canonicalDir := filepath.Join(storeRoot, "testpkg", "1.0-1")
	if err := os.MkdirAll(filepath.Join(canonicalDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldBin := filepath.Join(canonicalDir, "bin", "testpkg")
	if err := os.WriteFile(oldBin, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "1.0"},
		Source: recipe.Source{
			URL: srv.URL + "/source.tar.gz", SHA256: hash,
		},
		Build: recipe.Build{Steps: []string{"exit 1"}},
	}

	_, err := inst.Reinstall(r)
	if err == nil {
		t.Fatal("expected Reinstall error")
	}
	data, readErr := os.ReadFile(oldBin)
	if readErr != nil {
		t.Fatalf("read old binary after failed Reinstall: %v", readErr)
	}
	if string(data) != "old" {
		t.Fatalf("old binary content = %q, want %q", string(data), "old")
	}
}

func TestReinstallPreservesExistingStoreOnReplaceFailure(t *testing.T) {
	srcTar := createTestSourceTarGz(t)
	hash := hashFile(t, srcTar)
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, srcTar)
		},
	))
	defer srv.Close()

	storeRoot := t.TempDir()
	inst := &Installer{Store: store.NewStore(storeRoot)}

	canonicalDir := filepath.Join(storeRoot, "testpkg", "1.0-1")
	if err := os.MkdirAll(filepath.Join(canonicalDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldBin := filepath.Join(canonicalDir, "bin", "testpkg")
	if err := os.WriteFile(oldBin, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "1.0"},
		Source: recipe.Source{
			URL: srv.URL + "/source.tar.gz", SHA256: hash,
		},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin",
				"echo '#!/bin/sh\necho fresh' > $PREFIX/bin/testpkg",
				"chmod +x $PREFIX/bin/testpkg",
			},
		},
	}

	origRename := renameDir
	renameDir = func(oldPath, newPath string) error {
		if strings.Contains(filepath.Base(oldPath), ".build-") &&
			newPath == canonicalDir {
			return fmt.Errorf("boom")
		}
		return origRename(oldPath, newPath)
	}
	defer func() { renameDir = origRename }()

	_, err := inst.Reinstall(r)
	if err == nil {
		t.Fatal("expected Reinstall error")
	}
	data, readErr := os.ReadFile(oldBin)
	if readErr != nil {
		t.Fatalf("read old binary after failed Reinstall: %v", readErr)
	}
	if string(data) != "old" {
		t.Fatalf("old binary content = %q, want %q", string(data), "old")
	}
}

func TestReinstallBlocksOnStoreGenLockBeforeReplace(t *testing.T) {
	srcTar := createTestSourceTarGz(t)
	hash := hashFile(t, srcTar)
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, srcTar)
		},
	))
	defer srv.Close()

	galeDir := t.TempDir()
	storeRoot := filepath.Join(galeDir, "pkg")
	if err := os.MkdirAll(storeRoot, 0o755); err != nil {
		t.Fatalf("mkdir storeRoot: %v", err)
	}
	canonicalDir := filepath.Join(storeRoot, "testpkg", "1.0-1")
	if err := os.MkdirAll(filepath.Join(canonicalDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldBin := filepath.Join(canonicalDir, "bin", "testpkg")
	if err := os.WriteFile(oldBin, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	unlock, err := filelock.Acquire(filepath.Join(galeDir, "generation.lock"))
	if err != nil {
		t.Fatalf("Acquire store-gen lock: %v", err)
	}
	unlockFn := unlock
	defer func() {
		if unlockFn != nil {
			unlockFn()
		}
	}()

	inst := &Installer{Store: store.NewStore(storeRoot)}
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "1.0"},
		Source: recipe.Source{
			URL: srv.URL + "/source.tar.gz", SHA256: hash,
		},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin",
				"echo '#!/bin/sh\necho fresh' > $PREFIX/bin/testpkg",
				"chmod +x $PREFIX/bin/testpkg",
			},
		},
	}

	done := make(chan error, 1)
	go func() {
		_, err := inst.Reinstall(r)
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("Reinstall completed while store-gen lock held: %v", err)
	case <-time.After(500 * time.Millisecond):
		// Reinstall is either still building its staged output
		// or blocked before replacing the canonical dir. In both
		// cases, the live store must remain untouched.
	}
	data, readErr := os.ReadFile(oldBin)
	if readErr != nil {
		t.Fatalf("read old binary while lock held: %v", readErr)
	}
	if string(data) != "old" {
		t.Fatalf("old binary content = %q, want %q", string(data), "old")
	}

	unlockFn()
	unlockFn = nil

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Reinstall error after release: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Reinstall did not complete after lock release")
	}

	data, readErr = os.ReadFile(oldBin)
	if readErr != nil {
		t.Fatalf("read new binary after Reinstall: %v", readErr)
	}
	if !strings.Contains(string(data), "fresh") {
		t.Fatalf("binary content = %q, want fresh content", string(data))
	}
}

// --- C3: non-GHCR binary without sha256-only opt-in fails ---

// TestInstallBinaryNonGHCRDefaultTrustFails ensures a recipe
// that ships a non-GHCR binary URL without explicit
// trust = "sha256-only" is rejected rather than silently
// bypassing attestation. This closes the C3 bypass at
// installer.go:324-329 where isGHCR(bin.URL) gated the
// attestation check, letting any non-GHCR URL skip it.
func TestInstallBinaryNonGHCRDefaultTrustFails(t *testing.T) {
	// Build a tar.zst and a source tarball so we can
	// confirm the installer does NOT fall back to source
	// when the binary is rejected on policy grounds — the
	// whole point is to make the error visible, not to
	// silently paper over it.
	binContent := "#!/bin/sh\necho direct-binary"
	tarzst := createTestTarZstd(t, "bin/tool", binContent)
	hash := hashFile(t, tarzst)
	blobData, err := os.ReadFile(tarzst)
	if err != nil {
		t.Fatalf("read tar.zst: %v", err)
	}

	srcTar := createTestSourceTarGz(t)
	srcHash := hashFile(t, srcTar)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/tool-1.0.tar.zst":
				w.Write(blobData)
			case "/source.tar.gz":
				http.ServeFile(w, r, srcTar)
			default:
				http.NotFound(w, r)
			}
		},
	))
	defer srv.Close()

	var logBuf bytes.Buffer
	storeRoot := t.TempDir()
	inst := &Installer{
		Store:             store.NewStore(storeRoot),
		BinaryFallbackLog: &logBuf,
	}

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "tool", Version: "1.0"},
		Source: recipe.Source{
			URL:    srv.URL + "/source.tar.gz",
			SHA256: srcHash,
		},
		Binary: map[string]recipe.Binary{
			fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH): {
				// Non-GHCR URL, no explicit trust field → default
				// sigstore → must fail because attestation can't
				// validate a non-GHCR artifact.
				URL:    srv.URL + "/tool-1.0.tar.zst",
				SHA256: hash,
				// Trust intentionally empty; EffectiveTrust
				// resolves to "sigstore".
			},
		},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin",
				"echo '#!/bin/sh' > $PREFIX/bin/tool",
				"chmod +x $PREFIX/bin/tool",
			},
		},
	}

	// The binary install must be rejected on policy grounds
	// — the fallback to source is acceptable (it's how the
	// installer always handles a rejected binary) but the
	// reason must surface in the fallback log.
	_, err = inst.Install(r)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	got := logBuf.String()
	if got == "" {
		t.Fatal("expected fallback log to record binary " +
			"rejection, got empty buffer")
	}
	if !strings.Contains(got, "trust") && !strings.Contains(got, "sigstore") {
		t.Errorf("fallback log missing trust-policy reason:\n%s", got)
	}
	if !strings.Contains(got, "ghcr") && !strings.Contains(got, "GHCR") {
		t.Errorf("fallback log should mention GHCR requirement:\n%s", got)
	}
}

// TestInstallBinaryNonGHCRSha256OnlyAccepted ensures a
// recipe that explicitly declares trust = "sha256-only"
// is accepted from a non-GHCR URL (with an informational
// warning surfaced via BinaryFallbackLog) and the binary
// is installed without attestation verification.
func TestInstallBinaryNonGHCRSha256OnlyAccepted(t *testing.T) {
	binContent := "#!/bin/sh\necho vendor-binary"
	tarzst := createTestTarZstd(t, "bin/tool", binContent)
	hash := hashFile(t, tarzst)
	blobData, err := os.ReadFile(tarzst)
	if err != nil {
		t.Fatalf("read tar.zst: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Write(blobData)
		},
	))
	defer srv.Close()

	storeRoot := t.TempDir()
	inst := &Installer{
		Store: store.NewStore(storeRoot),
	}

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "tool", Version: "1.0"},
		Source:  recipe.Source{URL: "http://unused", SHA256: "unused"},
		Binary: map[string]recipe.Binary{
			fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH): {
				URL:    srv.URL + "/tool-1.0.tar.zst",
				SHA256: hash,
				Trust:  recipe.TrustSHA256Only,
			},
		},
	}

	result, err := inst.Install(r)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if result.Method != "binary" {
		t.Errorf("Method = %q, want %q", result.Method, "binary")
	}
}

// TestCheckBinaryTrustPolicy asserts the policy layer
// independently of the download + verify machinery. The
// full install path hits ghcr.io for a ghcr.io URL
// (token exchange), so we can't exercise "GHCR URL accepted"
// end-to-end inside a unit test. This table drives the
// policy decision directly.
func TestCheckBinaryTrustPolicy(t *testing.T) {
	tests := []struct {
		name    string
		bin     recipe.Binary
		wantErr bool
	}{
		{
			name: "ghcr URL with default (empty) trust is accepted",
			bin: recipe.Binary{
				URL: "https://ghcr.io/v2/kelp/gale-recipes/jq/blobs/sha256:abc",
			},
			wantErr: false,
		},
		{
			name: "ghcr URL with explicit sigstore trust is accepted",
			bin: recipe.Binary{
				URL:   "https://ghcr.io/v2/kelp/gale-recipes/jq/blobs/sha256:abc",
				Trust: recipe.TrustSigstore,
			},
			wantErr: false,
		},
		{
			name: "non-ghcr URL with default trust is rejected",
			bin: recipe.Binary{
				URL: "https://example.com/releases/tool-1.0.tar.zst",
			},
			wantErr: true,
		},
		{
			name: "non-ghcr URL with explicit sigstore trust is rejected",
			bin: recipe.Binary{
				URL:   "https://example.com/releases/tool-1.0.tar.zst",
				Trust: recipe.TrustSigstore,
			},
			wantErr: true,
		},
		{
			name: "non-ghcr URL with sha256-only trust is accepted",
			bin: recipe.Binary{
				URL:   "https://example.com/releases/tool-1.0.tar.zst",
				Trust: recipe.TrustSHA256Only,
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkBinaryTrustPolicy(&tt.bin)
			if (err != nil) != tt.wantErr {
				t.Errorf("checkBinaryTrustPolicy(%+v) err=%v, wantErr=%v",
					tt.bin, err, tt.wantErr)
			}
		})
	}
}

// TestInstallSkipsBuildOnlyDepsWhenBinarySucceeds verifies that
// when a recipe has a usable prebuilt binary, build-only deps
// are NOT installed. Build-only deps (autoconf, automake, etc.)
// are needed only to compile from source; the prebuilt binary
// doesn't use them. Installing them for every binary install is
// wasted bandwidth and store space.
//
// Runtime deps must still be installed (the binary links against
// them) and .gale-deps.toml must record BOTH so IsStale's
// staleness detection keeps working.
func TestInstallSkipsBuildOnlyDepsWhenBinarySucceeds(t *testing.T) {
	// Three prebuilt binaries: victim (the target), bdep
	// (build-only), rdep (runtime). All served via httptest.
	victimTar := createTestTarZstd(t, "bin/victim",
		"#!/bin/sh\necho victim")
	victimHash := hashFile(t, victimTar)
	victimData, err := os.ReadFile(victimTar)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}

	bdepTar := createTestTarZstd(t, "bin/bdep",
		"#!/bin/sh\necho bdep")
	bdepHash := hashFile(t, bdepTar)
	bdepData, err := os.ReadFile(bdepTar)
	if err != nil {
		t.Fatalf("read bdep: %v", err)
	}

	rdepTar := createTestTarZstd(t, "bin/rdep",
		"#!/bin/sh\necho rdep")
	rdepHash := hashFile(t, rdepTar)
	rdepData, err := os.ReadFile(rdepTar)
	if err != nil {
		t.Fatalf("read rdep: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/victim.tar.zst":
				w.Write(victimData)
			case "/bdep.tar.zst":
				w.Write(bdepData)
			case "/rdep.tar.zst":
				w.Write(rdepData)
			default:
				http.NotFound(w, r)
			}
		},
	))
	defer srv.Close()

	restore := download.SetHTTPClient(srv.Client())
	defer restore()

	storeRoot := t.TempDir()
	platform := fmt.Sprintf("%s-%s",
		runtime.GOOS, runtime.GOARCH)

	makeRec := func(name, hash, path string) *recipe.Recipe {
		return &recipe.Recipe{
			Package: recipe.Package{Name: name, Version: "1.0"},
			Source:  recipe.Source{URL: "http://unused", SHA256: "unused"},
			Binary: map[string]recipe.Binary{
				platform: {
					URL:    srv.URL + path,
					SHA256: hash,
					Trust:  recipe.TrustSHA256Only,
				},
			},
		}
	}

	inst := &Installer{
		Store: store.NewStore(storeRoot),
		Resolver: func(name string) (*recipe.Recipe, error) {
			switch name {
			case "bdep":
				return makeRec("bdep", bdepHash, "/bdep.tar.zst"), nil
			case "rdep":
				return makeRec("rdep", rdepHash, "/rdep.tar.zst"), nil
			}
			return nil, fmt.Errorf("unknown dep: %s", name)
		},
	}

	victim := makeRec("victim", victimHash, "/victim.tar.zst")
	victim.Dependencies = recipe.Dependencies{
		Build:   []string{"bdep"},
		Runtime: []string{"rdep"},
	}

	result, err := inst.Install(victim)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if result.Method != "binary" {
		t.Fatalf("Method = %q, want binary", result.Method)
	}

	// Runtime dep must be installed.
	if _, err := os.Stat(filepath.Join(storeRoot,
		"rdep", "1.0-1", "bin", "rdep")); err != nil {
		t.Errorf("runtime dep not installed: %v", err)
	}

	// Build-only dep must NOT be installed — this is the
	// regression check.
	bdepPath := filepath.Join(storeRoot, "bdep", "1.0-1")
	if _, err := os.Stat(bdepPath); err == nil {
		t.Errorf("build-only dep was installed at %s — "+
			"should have been skipped because the binary "+
			"install succeeded", bdepPath)
	}

	// Metadata must record both so IsStale still works.
	md, err := ReadDepsMetadata(filepath.Join(storeRoot,
		"victim", "1.0-1"))
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	names := map[string]bool{}
	for _, d := range md.Deps {
		names[d.Name] = true
	}
	if !names["bdep"] {
		t.Errorf("metadata missing bdep entry; "+
			"got %v — staleness detection won't catch "+
			"build-dep revision bumps", md.Deps)
	}
	if !names["rdep"] {
		t.Errorf("metadata missing rdep entry; got %v", md.Deps)
	}
}

// TestInstallInstallsBuildOnlyDepsOnSourceFallback verifies
// that when the binary attempt fails, the deferred
// build-only deps DO get installed before the source build
// runs. Otherwise a failed binary install would leave a
// half-resolved state where the source build can't proceed.
func TestInstallInstallsBuildOnlyDepsOnSourceFallback(t *testing.T) {
	// bdep is a real prebuilt; rdep is a real prebuilt.
	// victim advertises a binary but the server 404s — so
	// the install falls through to source.
	bdepTar := createTestTarZstd(t, "bin/bdep",
		"#!/bin/sh\necho bdep")
	bdepHash := hashFile(t, bdepTar)
	bdepData, _ := os.ReadFile(bdepTar)

	rdepTar := createTestTarZstd(t, "bin/rdep",
		"#!/bin/sh\necho rdep")
	rdepHash := hashFile(t, rdepTar)
	rdepData, _ := os.ReadFile(rdepTar)

	// Real source tarball for victim so the source build
	// has something to run against.
	srcTar := createTestSourceTarGz(t)
	srcHash := hashFile(t, srcTar)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/bdep.tar.zst":
				w.Write(bdepData)
			case "/rdep.tar.zst":
				w.Write(rdepData)
			case "/source.tar.gz":
				http.ServeFile(w, r, srcTar)
			default:
				// /victim-binary.tar.zst falls through here
				// and the install path treats 404 as a binary
				// failure → source fallback.
				http.NotFound(w, r)
			}
		},
	))
	defer srv.Close()

	restore := download.SetHTTPClient(srv.Client())
	defer restore()

	storeRoot := t.TempDir()
	platform := fmt.Sprintf("%s-%s",
		runtime.GOOS, runtime.GOARCH)

	inst := &Installer{
		Store: store.NewStore(storeRoot),
		// Silence the fallback warning in test output.
		BinaryFallbackLog: io.Discard,
		Resolver: func(name string) (*recipe.Recipe, error) {
			switch name {
			case "bdep":
				return &recipe.Recipe{
					Package: recipe.Package{
						Name: "bdep", Version: "1.0",
					},
					Source: recipe.Source{URL: "unused", SHA256: "unused"},
					Binary: map[string]recipe.Binary{
						platform: {
							URL:    srv.URL + "/bdep.tar.zst",
							SHA256: bdepHash,
							Trust:  recipe.TrustSHA256Only,
						},
					},
				}, nil
			case "rdep":
				return &recipe.Recipe{
					Package: recipe.Package{
						Name: "rdep", Version: "1.0",
					},
					Source: recipe.Source{URL: "unused", SHA256: "unused"},
					Binary: map[string]recipe.Binary{
						platform: {
							URL:    srv.URL + "/rdep.tar.zst",
							SHA256: rdepHash,
							Trust:  recipe.TrustSHA256Only,
						},
					},
				}, nil
			}
			return nil, fmt.Errorf("unknown: %s", name)
		},
	}

	victim := &recipe.Recipe{
		Package: recipe.Package{Name: "victim", Version: "1.0"},
		Source: recipe.Source{
			URL:    srv.URL + "/source.tar.gz",
			SHA256: srcHash,
		},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin",
				"echo '#!/bin/sh' > $PREFIX/bin/victim",
				"chmod +x $PREFIX/bin/victim",
			},
		},
		Binary: map[string]recipe.Binary{
			platform: {
				URL:    srv.URL + "/victim-binary.tar.zst",
				SHA256: "deadbeef",
				Trust:  recipe.TrustSHA256Only,
			},
		},
		Dependencies: recipe.Dependencies{
			Build:   []string{"bdep"},
			Runtime: []string{"rdep"},
		},
	}

	result, err := inst.Install(victim)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if result.Method != "source" {
		t.Fatalf("Method = %q, want source", result.Method)
	}

	// Both deps should be installed in the fallback path.
	for _, dep := range []string{"bdep", "rdep"} {
		path := filepath.Join(storeRoot, dep, "1.0-1",
			"bin", dep)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s not installed after source "+
				"fallback: %v", dep, err)
		}
	}
}

// TestInstallInstallsAllDepsInSourceOnlyMode verifies that
// SourceOnly mode walks the full dep set via
// InstallBuildDeps (not just runtime deps) — even when the
// recipe declares a usable binary, because the binary path
// is never entered.
//
// Pre-installs bdep so the deps walk hits the Cached path
// and the test doesn't have to model a full source build
// for the dep itself; the assertion is that the walker
// VISITED bdep, evidenced by bdep ending up in the build
// env paths returned to the caller.
func TestInstallInstallsAllDepsInSourceOnlyMode(t *testing.T) {
	srcTar := createTestSourceTarGz(t)
	srcHash := hashFile(t, srcTar)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/source.tar.gz" {
				http.ServeFile(w, r, srcTar)
				return
			}
			http.NotFound(w, r)
		},
	))
	defer srv.Close()

	restore := download.SetHTTPClient(srv.Client())
	defer restore()

	storeRoot := t.TempDir()
	s := store.NewStore(storeRoot)
	// Pre-install bdep so the deps walk treats it as
	// cached. The point of the test is the walker reaches
	// build-only deps, not that it can build them.
	preInstall(t, s, "bdep", "1.0-1")

	platform := fmt.Sprintf("%s-%s",
		runtime.GOOS, runtime.GOARCH)

	inst := &Installer{
		Store:      s,
		SourceOnly: true,
		Resolver: func(name string) (*recipe.Recipe, error) {
			if name == "bdep" {
				return &recipe.Recipe{
					Package: recipe.Package{
						Name: "bdep", Version: "1.0",
					},
				}, nil
			}
			return nil, fmt.Errorf("unknown: %s", name)
		},
	}

	victim := &recipe.Recipe{
		Package: recipe.Package{Name: "victim", Version: "1.0"},
		Source: recipe.Source{
			URL:    srv.URL + "/source.tar.gz",
			SHA256: srcHash,
		},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin",
				"echo '#!/bin/sh' > $PREFIX/bin/victim",
				"chmod +x $PREFIX/bin/victim",
			},
		},
		// Recipe DOES advertise a binary, but SourceOnly is
		// set on the installer so we ignore it.
		Binary: map[string]recipe.Binary{
			platform: {
				URL:    srv.URL + "/never-fetched.tar.zst",
				SHA256: "deadbeef",
				Trust:  recipe.TrustSHA256Only,
			},
		},
		Dependencies: recipe.Dependencies{
			Build: []string{"bdep"},
		},
	}

	if _, err := inst.Install(victim); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// bdep should appear in victim's .gale-deps.toml — this
	// only happens if the dep walker visited it via the
	// SourceOnly path's InstallBuildDeps (since the binary
	// path's metadata fallback is unreachable in
	// SourceOnly).
	md, err := ReadDepsMetadata(filepath.Join(storeRoot,
		"victim", "1.0-1"))
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	found := false
	for _, d := range md.Deps {
		if d.Name == "bdep" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("bdep missing from metadata in SourceOnly "+
			"mode — deps walker didn't visit build-only "+
			"deps; got %+v", md.Deps)
	}
}

// TestInstallSkipsBuildOnlyDepsPreservesStaleness pins the
// contract that even though build-only deps aren't
// installed locally, they ARE recorded in
// .gale-deps.toml — so IsStale flags the package as stale
// when a build dep's recipe bumps revision.
func TestInstallSkipsBuildOnlyDepsPreservesStaleness(t *testing.T) {
	victimTar := createTestTarZstd(t, "bin/victim",
		"#!/bin/sh\necho victim")
	victimHash := hashFile(t, victimTar)
	victimData, _ := os.ReadFile(victimTar)

	bdepTar := createTestTarZstd(t, "bin/bdep",
		"#!/bin/sh\necho bdep")
	bdepHash := hashFile(t, bdepTar)
	bdepData, _ := os.ReadFile(bdepTar)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/victim.tar.zst":
				w.Write(victimData)
			case "/bdep.tar.zst":
				w.Write(bdepData)
			default:
				http.NotFound(w, r)
			}
		},
	))
	defer srv.Close()

	restore := download.SetHTTPClient(srv.Client())
	defer restore()

	storeRoot := t.TempDir()
	platform := fmt.Sprintf("%s-%s",
		runtime.GOOS, runtime.GOARCH)

	// bdep at revision 1 when victim is installed.
	bdepRev := 1
	resolver := func(name string) (*recipe.Recipe, error) {
		if name == "bdep" {
			return &recipe.Recipe{
				Package: recipe.Package{
					Name: "bdep", Version: "1.0",
					Revision: bdepRev,
				},
				Source: recipe.Source{URL: "unused", SHA256: "unused"},
				Binary: map[string]recipe.Binary{
					platform: {
						URL:    srv.URL + "/bdep.tar.zst",
						SHA256: bdepHash,
						Trust:  recipe.TrustSHA256Only,
					},
				},
			}, nil
		}
		return nil, fmt.Errorf("unknown: %s", name)
	}

	inst := &Installer{
		Store:    store.NewStore(storeRoot),
		Resolver: resolver,
	}

	victim := &recipe.Recipe{
		Package: recipe.Package{Name: "victim", Version: "1.0"},
		Source:  recipe.Source{URL: "unused", SHA256: "unused"},
		Binary: map[string]recipe.Binary{
			platform: {
				URL:    srv.URL + "/victim.tar.zst",
				SHA256: victimHash,
				Trust:  recipe.TrustSHA256Only,
			},
		},
		Dependencies: recipe.Dependencies{
			Build: []string{"bdep"},
		},
	}

	if _, err := inst.Install(victim); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Confirm metadata recorded bdep at revision 1.
	storeDir := filepath.Join(storeRoot, "victim", "1.0-1")
	md, err := ReadDepsMetadata(storeDir)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if len(md.Deps) != 1 || md.Deps[0].Name != "bdep" ||
		md.Deps[0].Revision != 1 {
		t.Fatalf("metadata = %+v, want one bdep@1.0-1 entry",
			md.Deps)
	}

	// Bump bdep's revision in the resolver and ask
	// IsStale to compare.
	bdepRev = 2
	stale, err := IsStale(storeDir, victim, resolver)
	if err != nil {
		t.Fatalf("IsStale: %v", err)
	}
	if !stale {
		t.Error("victim should be reported stale after " +
			"bdep revision bump — metadata records bdep " +
			"but a binary-only install skipped its actual " +
			"installation, so without the recorded entry " +
			"staleness detection silently breaks")
	}
}

// --- InstallWithFinalize ---

// TestInstallWithFinalize_BlocksConcurrentStoreRemove is the
// load-bearing regression test. InstallWithFinalize must hold the
// per-package lock while finalize() runs, so a concurrent
// store.Remove cannot delete the store dir out from under the caller
// (e.g. a generation rebuild) until finalize() has finished.
//
// The test pre-seeds the store so the install returns MethodCached
// (avoiding a real network/build round-trip). It then calls
// InstallWithFinalize with a finalize that blocks on a channel for
// ~200 ms. A goroutine races store.Remove against that window.
// The assertion is that Remove returns AFTER finalize unblocked —
// proving the lock was held for the whole finalize duration.
func TestInstallWithFinalize_BlocksConcurrentStoreRemove(t *testing.T) {
	storeRoot := t.TempDir()
	s := store.NewStore(storeRoot)

	// Pre-seed the store so install hits MethodCached.
	// Use the canonical <version>-<revision> form.
	dir, err := s.Create("lockpkg", "1.0-1")
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatalf("create bin: %v", err)
	}

	inst := &Installer{Store: s}
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "lockpkg", Version: "1.0"},
		Source:  recipe.Source{URL: "http://unused", SHA256: "unused"},
	}

	// unblock is closed by the test once we want finalize to return.
	unblock := make(chan struct{})
	// finalizeStarted is closed once finalize() is entered so we know
	// the lock is held.
	finalizeStarted := make(chan struct{})

	var installDone time.Time
	done := make(chan error, 1)
	go func() {
		_, err := inst.InstallWithFinalize(r, false, func(res *InstallResult) error {
			close(finalizeStarted)
			<-unblock // block until test says go
			return nil
		})
		installDone = time.Now()
		done <- err
	}()

	// Wait until finalize is entered (lock is held).
	select {
	case <-finalizeStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("finalize never started")
	}

	// Now race store.Remove against the held lock.
	var removeDone time.Time
	removeDoneC := make(chan struct{})
	go func() {
		defer close(removeDoneC)
		// Remove uses the same per-package lock internally; it must
		// block until finalize releases it.
		_ = s.Remove("lockpkg", "1.0-1")
		removeDone = time.Now()
	}()

	// Pause briefly to let Remove block on the lock.
	time.Sleep(150 * time.Millisecond)

	// Remove must NOT have returned yet (it should be waiting for
	// the per-package lock that finalize holds).
	select {
	case <-removeDoneC:
		t.Fatal("store.Remove returned before finalize released the lock")
	default:
	}

	// Unblock finalize.
	unblockTime := time.Now()
	close(unblock)

	// Wait for both operations.
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("InstallWithFinalize error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("InstallWithFinalize did not complete")
	}

	select {
	case <-removeDoneC:
	case <-time.After(10 * time.Second):
		t.Fatal("store.Remove did not complete after lock release")
	}

	// Remove must have finished AFTER finalize was unblocked.
	if removeDone.Before(unblockTime) {
		t.Errorf("store.Remove finished before finalize was unblocked: "+
			"remove=%v unblock=%v — lock was not held during finalize",
			removeDone, unblockTime)
	}
	_ = installDone // suppress unused warning
}

// TestInstallWithFinalize_PropagatesFinalizeError asserts that when
// finalize returns a non-nil error, InstallWithFinalize returns a
// non-nil error AND a non-nil result (partial state is visible).
func TestInstallWithFinalize_PropagatesFinalizeError(t *testing.T) {
	storeRoot := t.TempDir()
	s := store.NewStore(storeRoot)

	// Pre-seed so install hits MethodCached — avoids network.
	dir, err := s.Create("errpkg", "2.0-1")
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatalf("create bin: %v", err)
	}

	inst := &Installer{Store: s}
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "errpkg", Version: "2.0"},
		Source:  recipe.Source{URL: "http://unused", SHA256: "unused"},
	}

	sentinel := fmt.Errorf("finalize sentinel error")

	result, err := inst.InstallWithFinalize(r, false, func(res *InstallResult) error {
		return sentinel
	})

	if err == nil {
		t.Fatal("expected non-nil error from finalize, got nil")
	}
	if result == nil {
		t.Fatal("expected non-nil result even when finalize errors")
	}
	// The error must wrap or equal the sentinel.
	if !strings.Contains(err.Error(), sentinel.Error()) {
		t.Errorf("error = %q, want it to contain sentinel %q",
			err.Error(), sentinel.Error())
	}
	// Result must carry the package identity.
	if result.Name != "errpkg" {
		t.Errorf("result.Name = %q, want %q", result.Name, "errpkg")
	}
}

// TestInstallWithFinalize_NilFinalizeIsNoop asserts that passing nil
// as finalize causes the call to behave identically to Install:
// no panic, no error, and the result carries the expected fields.
func TestInstallWithFinalize_NilFinalizeIsNoop(t *testing.T) {
	storeRoot := t.TempDir()
	s := store.NewStore(storeRoot)

	// Pre-seed so install hits MethodCached.
	dir, err := s.Create("nooppkg", "3.0-1")
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatalf("create bin: %v", err)
	}

	inst := &Installer{Store: s}
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "nooppkg", Version: "3.0"},
		Source:  recipe.Source{URL: "http://unused", SHA256: "unused"},
	}

	result, err := inst.InstallWithFinalize(r, false, nil)
	if err != nil {
		t.Fatalf("InstallWithFinalize with nil finalize: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result for nil finalize")
	}
	if result.Name != "nooppkg" {
		t.Errorf("result.Name = %q, want %q", result.Name, "nooppkg")
	}
	if result.Method != MethodCached {
		t.Errorf("result.Method = %q, want %q",
			result.Method, MethodCached)
	}
}

// --- InstallLocalWithFinalize ---

// TestInstallLocalWithFinalize_HoldsLockAcrossFinalize verifies that
// InstallLocalWithFinalize holds the per-package lock for the entire
// duration of finalize(), not just for the build phase. A goroutine
// calls lockPackage directly (the same primitive store.Remove uses)
// and must block until finalize() returns.
func TestInstallLocalWithFinalize_HoldsLockAcrossFinalize(t *testing.T) {
	sourceDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(sourceDir, "README"),
		[]byte("placeholder"), 0o644,
	); err != nil {
		t.Fatalf("write source: %v", err)
	}

	storeRoot := t.TempDir()
	inst := &Installer{Store: store.NewStore(storeRoot)}

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "localpkg", Version: "1.0"},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin",
				"echo '#!/bin/sh\necho local' > $PREFIX/bin/localpkg",
				"chmod +x $PREFIX/bin/localpkg",
			},
		},
	}

	// unblock is closed by the test once we want finalize to return.
	unblock := make(chan struct{})
	// finalizeStarted is closed once finalize() is entered so we
	// know the per-package lock is held.
	finalizeStarted := make(chan struct{})

	done := make(chan error, 1)
	go func() {
		_, err := inst.InstallLocalWithFinalize(r, sourceDir,
			func(res *InstallResult) error {
				close(finalizeStarted)
				<-unblock // hold the lock until test says go
				return nil
			})
		done <- err
	}()

	// Wait until finalize is entered (lock is held).
	select {
	case <-finalizeStarted:
	case <-time.After(30 * time.Second):
		t.Fatal("finalize never started")
	}

	// Try to acquire the same per-package lock from a goroutine.
	// It must block until finalize releases it.
	lockAcquired := make(chan struct{})
	go func() {
		unlock, err := lockPackage(storeRoot, "localpkg", "1.0-1")
		if err != nil {
			t.Errorf("lockPackage: %v", err)
			close(lockAcquired)
			return
		}
		close(lockAcquired)
		unlock()
	}()

	// Pause briefly to let the goroutine reach the lock contention.
	time.Sleep(150 * time.Millisecond)

	// Lock must NOT have been acquired while finalize holds it.
	select {
	case <-lockAcquired:
		t.Fatal("lockPackage acquired the lock before finalize released it")
	default:
	}

	// Unblock finalize — lock is now released.
	close(unblock)

	// Both finalize and the lock goroutine must complete.
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("InstallLocalWithFinalize: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("InstallLocalWithFinalize did not complete")
	}

	select {
	case <-lockAcquired:
	case <-time.After(10 * time.Second):
		t.Fatal("lockPackage did not acquire lock after finalize released it")
	}
}

// --- InstallGitWithFinalize ---

// TestInstallGitWithFinalize_HoldsLockAcrossFinalize verifies that
// InstallGitWithFinalize holds the per-package lock (keyed on the
// commit hash) for the entire duration of finalize(), not just for
// the build phase. A goroutine calls lockPackage directly and must
// block until finalize() returns. Uses a local bare git repo to
// avoid any network dependency.
func TestInstallGitWithFinalize_HoldsLockAcrossFinalize(t *testing.T) {
	// Build a local bare git repo so the test doesn't touch the
	// network. gitutil.Clone works with file:// paths.
	repoDir := makeLocalBareRepo(t)

	storeRoot := t.TempDir()
	inst := &Installer{Store: store.NewStore(storeRoot)}

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "gitpkg", Version: "0.1"},
		Source:  recipe.Source{Repo: repoDir},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin",
				"echo '#!/bin/sh\necho git' > $PREFIX/bin/gitpkg",
				"chmod +x $PREFIX/bin/gitpkg",
			},
		},
	}

	// unblock is closed by the test once we want finalize to return.
	unblock := make(chan struct{})
	// finalizeStarted is closed once finalize() is entered so we
	// know the per-package lock is held.
	finalizeStarted := make(chan struct{})
	// capturedHash receives the commit hash resolved during build,
	// so the test can use it to contend on the correct lock.
	capturedHash := make(chan string, 1)

	done := make(chan error, 1)
	go func() {
		_, err := inst.InstallGitWithFinalize(r,
			func(res *InstallResult) error {
				capturedHash <- res.Version
				close(finalizeStarted)
				<-unblock // hold the lock until test says go
				return nil
			})
		done <- err
	}()

	// Wait until finalize is entered (lock is held).
	select {
	case <-finalizeStarted:
	case <-time.After(60 * time.Second):
		t.Fatal("finalize never started")
	}

	hash := <-capturedHash

	// Try to acquire the same per-package lock from a goroutine.
	// It must block until finalize releases it.
	lockAcquired := make(chan struct{})
	go func() {
		unlock, err := lockPackage(storeRoot, "gitpkg", hash)
		if err != nil {
			t.Errorf("lockPackage: %v", err)
			close(lockAcquired)
			return
		}
		close(lockAcquired)
		unlock()
	}()

	// Pause briefly to let the goroutine reach the lock contention.
	time.Sleep(150 * time.Millisecond)

	// Lock must NOT have been acquired while finalize holds it.
	select {
	case <-lockAcquired:
		t.Fatal("lockPackage acquired the lock before finalize released it")
	default:
	}

	// Unblock finalize — lock is now released.
	close(unblock)

	// Both finalize and the lock goroutine must complete.
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("InstallGitWithFinalize: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("InstallGitWithFinalize did not complete")
	}

	select {
	case <-lockAcquired:
	case <-time.After(10 * time.Second):
		t.Fatal("lockPackage did not acquire lock after finalize released it")
	}
}

// makeLocalBareRepo creates a local bare git repo with a single
// commit so tests can clone it without network access.
func makeLocalBareRepo(t *testing.T) string {
	t.Helper()

	workDir := t.TempDir()
	gitRun(t, workDir, "git", "init")
	gitRun(t, workDir, "git", "config", "user.email", "test@test.com")
	gitRun(t, workDir, "git", "config", "user.name", "Test")
	gitRun(t, workDir, "git", "config", "commit.gpgsign", "false")
	if err := os.WriteFile(
		filepath.Join(workDir, "README"),
		[]byte("hello"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	gitRun(t, workDir, "git", "add", "README")
	gitRun(t, workDir, "git", "commit", "-m", "initial")

	bareDir := t.TempDir()
	gitRun(t, "", "git", "clone", "--bare", workDir, bareDir)
	return bareDir
}

// gitRun executes a git command in dir, failing the test on error.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...) //nolint:gosec
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v failed: %s: %v", args, out, err)
	}
}

// --- Regression: installBinaryTo verifies OCI attestation ---

// recordingVerifier is a test-local Verifier that records the
// calls made by the install path. It returns nil (success) by
// default so the install continues; callers set ociErr to
// drive the API-fallback path.
type recordingVerifier struct {
	calledFile    bool
	calledOCI     bool
	capturedOCI   string
	capturedIsDir bool
	capturedSHA   string
	statErr       error
	ociErr        error
}

func (rv *recordingVerifier) Available() bool           { return true }
func (rv *recordingVerifier) UnavailableReason() string { return "" }

func (rv *recordingVerifier) VerifyFile(filePath, repo string) error {
	rv.calledFile = true
	info, err := os.Stat(filePath)
	if err != nil {
		rv.statErr = err
		// Intentionally succeed: the fake records the stat error
		// for the test to assert on, and the install must proceed
		// so we can inspect what path VerifyFile was handed.
		return nil //nolint:nilerr
	}
	rv.capturedIsDir = info.IsDir()

	// Hash the file bytes so we can compare to the expected
	// archive SHA256.
	if !rv.capturedIsDir {
		f, err := os.Open(filePath)
		if err == nil {
			h := sha256.New()
			io.Copy(h, f) //nolint:errcheck
			f.Close()
			rv.capturedSHA = fmt.Sprintf("%x", h.Sum(nil))
		}
	}
	return nil
}

func (rv *recordingVerifier) VerifyOCI(ociURI, repo string) error {
	rv.calledOCI = true
	rv.capturedOCI = ociURI
	return rv.ociErr
}

// hostRewrite is a round-tripper that rewrites every request to
// target the given host, keeping the path/query intact. Used to
// redirect ghcr.io URLs to a local TLS test server.
type hostRewrite struct {
	base http.RoundTripper
	host string
}

func (h hostRewrite) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.URL.Scheme = "https"
	r2.URL.Host = h.host
	return h.base.RoundTrip(r2)
}

// binaryInstallFixture holds the objects built by setupBinaryInstallTest.
type binaryInstallFixture struct {
	inst *Installer
	rv   *recordingVerifier
	hash string
}

// setupBinaryInstallTest builds a fake binary archive, serves it over
// TLS with a host-rewrite to ghcr.io, and returns an installer with a
// recording verifier and the archive hash. Cleanup is registered with
// t.Cleanup.
func setupBinaryInstallTest(t *testing.T) *binaryInstallFixture {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	binContent := "#!/bin/sh\necho from-sigstore-binary"
	tarzst := createTestTarZstd(t, "bin/testpkg", binContent)
	hash := hashFile(t, tarzst)
	blobData, err := os.ReadFile(tarzst)
	if err != nil {
		t.Fatalf("read tar.zst: %v", err)
	}

	srv := httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Write(blobData) //nolint:errcheck
		},
	))
	t.Cleanup(srv.Close)

	restore := download.SetHTTPClient(&http.Client{
		Transport: hostRewrite{
			base: srv.Client().Transport,
			host: srv.Listener.Addr().String(),
		},
	})
	t.Cleanup(restore)

	t.Setenv("GALE_GITHUB_TOKEN", "fake-token-for-test")

	rv := &recordingVerifier{}
	storeRoot := t.TempDir()
	inst := &Installer{
		Store:    store.NewStore(storeRoot),
		Verifier: rv,
	}
	return &binaryInstallFixture{inst: inst, rv: rv, hash: hash}
}

// TestInstallBinaryVerifiesOCIURI asserts that installBinaryTo
// passes the OCI image reference to VerifyOCI for sigstore-trusted
// prebuilt binaries.
func TestInstallBinaryVerifiesOCIURI(t *testing.T) {
	fx := setupBinaryInstallTest(t)

	blobURL := fmt.Sprintf(
		"https://ghcr.io/v2/owner/repo/testpkg/blobs/sha256:%s", fx.hash,
	)
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "1.0"},
		Source:  recipe.Source{URL: "http://unused", SHA256: "unused"},
		Binary: map[string]recipe.Binary{
			fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH): {
				URL:    blobURL,
				SHA256: fx.hash,
				Trust:  recipe.TrustSigstore,
			},
		},
	}

	result, err := fx.inst.Install(r)
	if err != nil {
		t.Fatalf("Install error: %v", err)
	}
	if result.Method != MethodBinary {
		t.Errorf("Method = %q, want %q", result.Method, MethodBinary)
	}

	if !fx.rv.calledOCI {
		t.Fatal("VerifyOCI was never called; Verifier not wired into install path")
	}

	platform := fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH)
	wantOCI := fmt.Sprintf("oci://ghcr.io/owner/repo/testpkg:1.0-%s", platform)
	if fx.rv.capturedOCI != wantOCI {
		t.Errorf("VerifyOCI received %q, want %q", fx.rv.capturedOCI, wantOCI)
	}
	if fx.rv.calledFile {
		t.Errorf("VerifyFile was called unexpectedly; OCI verification succeeded")
	}

	tmpDir := build.TmpDir()
	if tmpDir != "" {
		pattern := filepath.Join(tmpDir, "gale-verify-*")
		leaked, err := filepath.Glob(pattern)
		if err == nil && len(leaked) > 0 {
			t.Errorf("leaked tempfile(s) under %s: %v", tmpDir, leaked)
		}
	}
}

// TestInstallBinaryEmitsAttestationTimingPhase asserts that
// installBinaryTo wraps the attestation VerifyOCI call in a
// timing.Phase so that --verbose surfaces attestation cost.
//
// Today the binary-stream phase is timed but the VerifyOCI call
// right after it is not, so no "[timing] attestation" line ever
// appears. The substring assertion below is the RED reason: it
// fails until the attestation phase is added.
func TestInstallBinaryEmitsAttestationTimingPhase(t *testing.T) {
	// No t.Parallel(): timing.SetOutput is a process-global sink.

	// Capture timing output via a verbose sink.
	var buf bytes.Buffer
	timing.SetOutput(output.NewWithOptions(&buf, output.Options{Verbose: true}))
	defer timing.SetOutput(nil)

	fx := setupBinaryInstallTest(t)

	blobURL := fmt.Sprintf(
		"https://ghcr.io/v2/owner/repo/testpkg/blobs/sha256:%s",
		fx.hash,
	)
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "1.0"},
		Source:  recipe.Source{URL: "http://unused", SHA256: "unused"},
		Binary: map[string]recipe.Binary{
			fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH): {
				URL:    blobURL,
				SHA256: fx.hash,
				Trust:  recipe.TrustSigstore,
			},
		},
	}

	result, err := fx.inst.Install(r)
	if err != nil {
		t.Fatalf("Install error: %v", err)
	}
	if result.Method != MethodBinary {
		t.Errorf("Method = %q, want %q", result.Method, MethodBinary)
	}
	if !fx.rv.calledOCI {
		t.Fatal("VerifyOCI was never called; Verifier not wired into install path")
	}

	if !strings.Contains(buf.String(), "[timing] attestation") {
		t.Errorf("expected a \"[timing] attestation\" line in timing "+
			"output, but the attestation VerifyOCI call is not wrapped "+
			"in timing.Phase.\ntiming output:\n%s", buf.String())
	}
}

// TestInstallParallelDepClosureBounded proves the per-package
// dependency closure downloads its dep binaries CONCURRENTLY,
// bounded by the Installer's Downloads limiter.
//
// Today installDepsInner runs the dep loop serially, so the peak
// number of in-flight binary fetches is 1. This test sets a
// limiter of 4 and asserts the peak overlap is >= 2 (real
// concurrency) and <= 4 (the limiter bound). Assertion (a) is the
// one that fails RED: a serial loop never overlaps, so peak == 1.
func TestInstallParallelDepClosureBounded(t *testing.T) {
	platform := fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH)

	// Four prebuilt binaries: the target plus three runtime deps.
	type pkg struct {
		name string
		hash string
		data []byte
		path string
	}
	mkPkg := func(name string) pkg {
		tarPath := createTestTarZstd(t, "bin/"+name,
			"#!/bin/sh\necho "+name)
		data, err := os.ReadFile(tarPath)
		if err != nil {
			t.Fatalf("read %s tar: %v", name, err)
		}
		return pkg{
			name: name,
			hash: hashFile(t, tarPath),
			data: data,
			path: "/" + name + ".tar.zst",
		}
	}

	target := mkPkg("target")
	depA := mkPkg("depa")
	depB := mkPkg("depb")
	depC := mkPkg("depc")

	byPath := map[string][]byte{
		target.path: target.data,
		depA.path:   depA.data,
		depB.path:   depB.data,
		depC.path:   depC.data,
	}
	// The dep tar paths whose concurrency we track. The target's
	// own fetch happens after its deps, so tracking only the dep
	// fetches isolates closure parallelism.
	depPaths := map[string]bool{
		depA.path: true,
		depB.path: true,
		depC.path: true,
	}

	var (
		mu       sync.Mutex
		inFlight int
		peak     int
	)
	track := func(delta int) {
		mu.Lock()
		inFlight += delta
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()
	}
	var hits int32

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			data, ok := byPath[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			if depPaths[r.URL.Path] {
				atomic.AddInt32(&hits, 1)
				track(1)
				// Artificial delay so concurrent dep fetches
				// overlap in wall-clock and the peak is observable.
				time.Sleep(40 * time.Millisecond)
				defer track(-1)
			}
			w.Write(data) //nolint:errcheck
		},
	))
	defer srv.Close()

	restore := download.SetHTTPClient(srv.Client())
	defer restore()

	storeRoot := t.TempDir()

	makeRec := func(p pkg) *recipe.Recipe {
		return &recipe.Recipe{
			Package: recipe.Package{Name: p.name, Version: "1.0"},
			Source:  recipe.Source{URL: "http://unused", SHA256: "unused"},
			Binary: map[string]recipe.Binary{
				platform: {
					URL:    srv.URL + p.path,
					SHA256: p.hash,
					Trust:  recipe.TrustSHA256Only,
				},
			},
		}
	}

	inst := &Installer{
		Store:     store.NewStore(storeRoot),
		Downloads: parallel.NewLimiter(4),
		Resolver: func(name string) (*recipe.Recipe, error) {
			switch name {
			case "depa":
				return makeRec(depA), nil
			case "depb":
				return makeRec(depB), nil
			case "depc":
				return makeRec(depC), nil
			}
			return nil, fmt.Errorf("unknown dep: %s", name)
		},
	}

	rec := makeRec(target)
	rec.Dependencies = recipe.Dependencies{
		Runtime: []string{"depa", "depb", "depc"},
	}

	if _, err := inst.Install(rec); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("dep fetch count = %d, want 3", got)
	}

	mu.Lock()
	gotPeak := peak
	mu.Unlock()

	// (a) Real overlap: a parallel closure runs at least two dep
	// fetches at once. A serial loop yields peak == 1 — RED.
	if gotPeak < 2 {
		t.Errorf("peak concurrent dep downloads = %d, want >= 2; "+
			"the dep closure is running serially", gotPeak)
	}
	// (b) Limiter bound: never more than 4 in flight.
	if gotPeak > 4 {
		t.Errorf("peak concurrent dep downloads = %d, want <= 4; "+
			"limiter not enforced", gotPeak)
	}

	// (c) Correctness: every dep present in the store.
	for _, name := range []string{"depa", "depb", "depc"} {
		bin := filepath.Join(storeRoot, name, "1.0-1", "bin", name)
		if _, err := os.Stat(bin); err != nil {
			t.Errorf("dep %s not installed: %v", name, err)
		}
	}
}
