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

	"github.com/kelp/gale/internal/profile"
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
	binDir := t.TempDir()
	inst := &Installer{
		Store:   store.NewStore(storeRoot),
		Profile: profile.NewProfile(binDir),
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

	// Verify symlink in profile.
	profileBin := filepath.Join(binDir, "testpkg")
	if _, err := os.Lstat(profileBin); err != nil {
		t.Errorf("binary not linked in profile: %v", err)
	}
}

func TestInstallSkipsAlreadyInstalled(t *testing.T) {
	storeRoot := t.TempDir()
	binDir := t.TempDir()

	s := store.NewStore(storeRoot)
	s.Create("testpkg", "1.0")
	binPath := filepath.Join(storeRoot, "testpkg", "1.0", "bin")
	os.MkdirAll(binPath, 0o755)

	inst := &Installer{
		Store:   s,
		Profile: profile.NewProfile(binDir),
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
	binDir := t.TempDir()
	inst := &Installer{
		Store:   store.NewStore(storeRoot),
		Profile: profile.NewProfile(binDir),
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
	binDir := t.TempDir()
	inst := &Installer{
		Store:   store.NewStore(storeRoot),
		Profile: profile.NewProfile(binDir),
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

	// Verify symlink points to v2.0.
	linkPath := filepath.Join(binDir, "testpkg")
	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if !strings.Contains(target, "2.0") {
		t.Errorf("symlink target = %q, want path containing 2.0",
			target)
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
	srv := httptest.NewServer(http.HandlerFunc(
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

	storeRoot := t.TempDir()
	binDir := t.TempDir()
	inst := &Installer{
		Store:   store.NewStore(storeRoot),
		Profile: profile.NewProfile(binDir),
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

	// Verify symlink in profile.
	profileBin := filepath.Join(binDir, "testpkg")
	if _, err := os.Lstat(profileBin); err != nil {
		t.Errorf("binary not linked in profile: %v", err)
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
	binDir := t.TempDir()
	inst := &Installer{
		Store:   store.NewStore(storeRoot),
		Profile: profile.NewProfile(binDir),
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
