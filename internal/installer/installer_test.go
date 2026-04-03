package installer

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
	"github.com/klauspost/compress/zstd"
)

func TestInstallFromSourceCreatesBinary(t *testing.T) {
	srcTar := createTestSourceTarGz(t)
	hash := hashFile(t, srcTar)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, srcTar)
		}))
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

	// Verify binary exists in store.
	storeBin := filepath.Join(storeRoot, "testpkg", "1.0", "bin", "testpkg")
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
		}))
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
		}))
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

	// Verify both versions exist in store.
	if !inst.Store.IsInstalled("testpkg", "1.0") {
		t.Error("v1.0 not in store")
	}
	if !inst.Store.IsInstalled("testpkg", "2.0") {
		t.Error("v2.0 not in store")
	}
}

func TestInstallBinaryFromGHCR(t *testing.T) {
	// Create a tar.zst with bin/testpkg.
	binContent := "#!/bin/sh\necho ghcr-binary"
	tarzst := createTestTarZstd(t, "bin/testpkg", binContent)
	hash := hashFile(t, tarzst)
	blobData, err := os.ReadFile(tarzst)
	if err != nil {
		t.Fatalf("read tar.zst: %v", err)
	}

	// Use env var for auth (skips token exchange).
	t.Setenv("GALE_GITHUB_TOKEN", "test-ghcr-token")

	// Mock GHCR blob endpoint — requires auth header.
	var gotAuth string
	srv := httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			if gotAuth != "Bearer test-ghcr-token" {
				http.Error(w, "unauthorized",
					http.StatusUnauthorized)
				return
			}
			w.Write(blobData)
		}))
	defer srv.Close()

	// Use the TLS test server's client so certs are trusted.
	restore := download.SetHTTPClient(srv.Client())
	defer restore()

	storeRoot := t.TempDir()
	inst := &Installer{
		Store: store.NewStore(storeRoot),
	}

	// URL must contain ghcr.io to trigger auth path.
	// We use the test server but embed ghcr.io in the
	// host via a redirect, or we adjust isGHCR. Simplest:
	// use the test server URL directly and make isGHCR
	// also match /v2/.../blobs/ paths.
	blobURL := fmt.Sprintf(
		"%s/v2/kelp/gale-recipes/testpkg/blobs/sha256:%s",
		srv.URL, hash)

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "1.0"},
		Source:  recipe.Source{URL: "http://unused", SHA256: "unused"},
		Binary: map[string]recipe.Binary{
			fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH): {
				URL:    blobURL,
				SHA256: hash,
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
		"testpkg", "1.0", "bin", "testpkg")
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
		}))
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
		"deptool", "1.0", "bin", "deptool")
	if _, err := os.Stat(depBin); err != nil {
		t.Errorf("dep not in store: %v", err)
	}

	// Verify main package built with deptool available.
	marker := filepath.Join(storeRoot,
		"mypkg", "2.0", "bin", "marker.txt")
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
		}))
	defer srv.Close()

	storeRoot := t.TempDir()
	inst := &Installer{
		Store: store.NewStore(storeRoot),
	}

	// Plain HTTP URL (not /v2/.../blobs/) triggers
	// non-GHCR download path.
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "tool", Version: "1.0"},
		Source:  recipe.Source{URL: "http://unused", SHA256: "unused"},
		Binary: map[string]recipe.Binary{
			fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH): {
				URL:    srv.URL + "/tool-1.0.tar.zst",
				SHA256: hash,
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
		"tool", "1.0", "bin", "tool")
	got, err := os.ReadFile(storeBin)
	if err != nil {
		t.Fatalf("read store binary: %v", err)
	}
	if string(got) != binContent {
		t.Errorf("binary content = %q, want %q",
			string(got), binContent)
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
		}))
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
		[]byte("hello"), 0o644); err != nil {
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
		[]byte("local source"), 0o644); err != nil {
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
		"localbuild", "0.1", "bin", "localbuild")
	if _, err := os.Stat(storeBin); err != nil {
		t.Errorf("binary not in store: %v", err)
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
		}))
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

// --- depsToBuildDeps ---

func TestDepsToBuildDepsNil(t *testing.T) {
	got := depsToBuildDeps(nil)
	if got != nil {
		t.Errorf("depsToBuildDeps(nil) = %v, want nil", got)
	}
}

func TestDepsToBuildDepsPopulated(t *testing.T) {
	deps := &DepPaths{
		BinDirs:   []string{"/store/a/1.0/bin", "/store/b/2.0/bin"},
		StoreDirs: []string{"/store/a/1.0", "/store/b/2.0"},
	}
	got := depsToBuildDeps(deps)
	if got == nil {
		t.Fatal("depsToBuildDeps returned nil for non-nil input")
	}
	if len(got.BinDirs) != 2 {
		t.Errorf("BinDirs len = %d, want 2", len(got.BinDirs))
	}
	if got.BinDirs[0] != "/store/a/1.0/bin" {
		t.Errorf("BinDirs[0] = %q, want %q",
			got.BinDirs[0], "/store/a/1.0/bin")
	}
	if len(got.StoreDirs) != 2 {
		t.Errorf("StoreDirs len = %d, want 2",
			len(got.StoreDirs))
	}
	if got.StoreDirs[1] != "/store/b/2.0" {
		t.Errorf("StoreDirs[1] = %q, want %q",
			got.StoreDirs[1], "/store/b/2.0")
	}
}

// --- extractBuild ---

func TestExtractBuildExtractsArchive(t *testing.T) {
	// Create a tar.zst with known content.
	tarzst := createTestTarZstd(t, "bin/hello", "world")
	hash := hashFile(t, tarzst)

	storeDir := t.TempDir()
	result := &build.BuildResult{
		Archive: tarzst,
		SHA256:  hash,
	}

	if err := extractBuild(result, storeDir); err != nil {
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
	storeDir := t.TempDir()
	result := &build.BuildResult{
		Archive: "/nonexistent/archive.tar.zst",
		SHA256:  "abc",
	}

	err := extractBuild(result, storeDir)
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
			want: true,
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
		}))
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
		}))
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
