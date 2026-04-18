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
		}))
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
	storeDir := t.TempDir()
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
		[]byte("fake"), 0o755); err != nil {
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
