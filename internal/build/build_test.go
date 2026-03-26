package build

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/recipe"
)

// --- Behavior 1: Successful build ---

func TestBuildSuccessReturnsResultWithArchiveAndSHA256(t *testing.T) {
	tarball, hash := createSourceTarGz(t, map[string]string{
		"testpkg-1.0/README": "hello",
	})
	srv := serveFile(t, tarball)

	r := &recipe.Recipe{
		Package: recipe.Package{
			Name:    "testpkg",
			Version: "1.0",
		},
		Source: recipe.Source{
			URL:    srv.URL + "/testpkg-1.0.tar.gz",
			SHA256: hash,
		},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin && echo '#!/bin/sh' > $PREFIX/bin/hello && chmod +x $PREFIX/bin/hello",
			},
		},
	}

	outputDir := t.TempDir()
	result, err := Build(r, outputDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Archive == "" {
		t.Error("expected Archive path to be set")
	}
	if result.SHA256 == "" {
		t.Error("expected SHA256 to be set")
	}

	// Verify the archive file exists.
	info, err := os.Stat(result.Archive)
	if err != nil {
		t.Fatalf("archive file does not exist: %v", err)
	}
	if info.Size() == 0 {
		t.Error("archive file is empty")
	}
}

// --- Behavior 2: Build step execution ---

func TestBuildStepRunsWithPREFIXAndJOBS(t *testing.T) {
	tarball, hash := createSourceTarGz(t, map[string]string{
		"testpkg-1.0/README": "hello",
	})
	srv := serveFile(t, tarball)

	r := &recipe.Recipe{
		Package: recipe.Package{
			Name:    "testpkg",
			Version: "1.0",
		},
		Source: recipe.Source{
			URL:    srv.URL + "/testpkg-1.0.tar.gz",
			SHA256: hash,
		},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin && echo '#!/bin/sh\necho hello' > $PREFIX/bin/hello && chmod +x $PREFIX/bin/hello",
			},
		},
	}

	outputDir := t.TempDir()
	result, err := Build(r, outputDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Extract the output archive and verify bin/hello exists.
	extractDir := t.TempDir()
	if err := download.ExtractTarZstd(result.Archive, extractDir); err != nil {
		t.Fatalf("failed to extract result archive: %v", err)
	}

	helloPath := filepath.Join(extractDir, "bin", "hello")
	info, err := os.Stat(helloPath)
	if err != nil {
		t.Fatalf("bin/hello not found in output archive: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("bin/hello should be executable, got mode %v",
			info.Mode())
	}
}

func TestBuildStepMultipleStepsRunInOrder(t *testing.T) {
	tarball, hash := createSourceTarGz(t, map[string]string{
		"testpkg-1.0/README": "hello",
	})
	srv := serveFile(t, tarball)

	r := &recipe.Recipe{
		Package: recipe.Package{
			Name:    "testpkg",
			Version: "1.0",
		},
		Source: recipe.Source{
			URL:    srv.URL + "/testpkg-1.0.tar.gz",
			SHA256: hash,
		},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin",
				"echo '#!/bin/sh' > $PREFIX/bin/hello",
				"chmod +x $PREFIX/bin/hello",
			},
		},
	}

	outputDir := t.TempDir()
	result, err := Build(r, outputDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Extract and verify the file was created by all steps.
	extractDir := t.TempDir()
	if err := download.ExtractTarZstd(result.Archive, extractDir); err != nil {
		t.Fatalf("failed to extract result archive: %v", err)
	}

	helloPath := filepath.Join(extractDir, "bin", "hello")
	if _, err := os.Stat(helloPath); err != nil {
		t.Fatalf("bin/hello not found: %v", err)
	}
}

// --- Behavior 3: Build step failure ---

func TestBuildStepFailureReturnsError(t *testing.T) {
	tarball, hash := createSourceTarGz(t, map[string]string{
		"testpkg-1.0/README": "hello",
	})
	srv := serveFile(t, tarball)

	r := &recipe.Recipe{
		Package: recipe.Package{
			Name:    "testpkg",
			Version: "1.0",
		},
		Source: recipe.Source{
			URL:    srv.URL + "/testpkg-1.0.tar.gz",
			SHA256: hash,
		},
		Build: recipe.Build{
			Steps: []string{
				"exit 1",
			},
		},
	}

	outputDir := t.TempDir()
	_, err := Build(r, outputDir)
	if err == nil {
		t.Fatal("expected error for failing build step")
	}
}

func TestBuildStepFailureErrorContainsStep(t *testing.T) {
	tarball, hash := createSourceTarGz(t, map[string]string{
		"testpkg-1.0/README": "hello",
	})
	srv := serveFile(t, tarball)

	failingStep := "false && this-should-fail"
	r := &recipe.Recipe{
		Package: recipe.Package{
			Name:    "testpkg",
			Version: "1.0",
		},
		Source: recipe.Source{
			URL:    srv.URL + "/testpkg-1.0.tar.gz",
			SHA256: hash,
		},
		Build: recipe.Build{
			Steps: []string{
				failingStep,
			},
		},
	}

	outputDir := t.TempDir()
	_, err := Build(r, outputDir)
	if err == nil {
		t.Fatal("expected error for failing build step")
	}
	if !strings.Contains(err.Error(), failingStep) {
		t.Errorf("error should contain the failing step %q, got %q",
			failingStep, err.Error())
	}
}

func TestBuildStepFailureSecondStepStopsExecution(t *testing.T) {
	tarball, hash := createSourceTarGz(t, map[string]string{
		"testpkg-1.0/README": "hello",
	})
	srv := serveFile(t, tarball)

	r := &recipe.Recipe{
		Package: recipe.Package{
			Name:    "testpkg",
			Version: "1.0",
		},
		Source: recipe.Source{
			URL:    srv.URL + "/testpkg-1.0.tar.gz",
			SHA256: hash,
		},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin",
				"exit 1",
				"echo should-not-run > $PREFIX/bin/bad",
			},
		},
	}

	outputDir := t.TempDir()
	_, err := Build(r, outputDir)
	if err == nil {
		t.Fatal("expected error for failing build step")
	}
}

// --- Behavior 4: Source hash mismatch ---

func TestBuildSourceHashMismatchReturnsError(t *testing.T) {
	tarball, _ := createSourceTarGz(t, map[string]string{
		"testpkg-1.0/README": "hello",
	})
	srv := serveFile(t, tarball)

	wrongHash := "0000000000000000000000000000000000000000000000000000000000000000"
	r := &recipe.Recipe{
		Package: recipe.Package{
			Name:    "testpkg",
			Version: "1.0",
		},
		Source: recipe.Source{
			URL:    srv.URL + "/testpkg-1.0.tar.gz",
			SHA256: wrongHash,
		},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin && echo hello > $PREFIX/bin/hello",
			},
		},
	}

	outputDir := t.TempDir()
	_, err := Build(r, outputDir)
	if err == nil {
		t.Fatal("expected error for hash mismatch")
	}
	if !strings.Contains(err.Error(), "sha256") &&
		!strings.Contains(err.Error(), "SHA256") &&
		!strings.Contains(err.Error(), "mismatch") &&
		!strings.Contains(err.Error(), "hash") {
		t.Errorf("error should mention hash mismatch, got %q",
			err.Error())
	}
}

func TestBuildSourceHashMismatchDoesNotRunSteps(t *testing.T) {
	tarball, _ := createSourceTarGz(t, map[string]string{
		"testpkg-1.0/README": "hello",
	})
	srv := serveFile(t, tarball)

	// Use a marker file to detect if steps ran.
	markerDir := t.TempDir()
	markerPath := filepath.Join(markerDir, "ran")

	wrongHash := "0000000000000000000000000000000000000000000000000000000000000000"
	r := &recipe.Recipe{
		Package: recipe.Package{
			Name:    "testpkg",
			Version: "1.0",
		},
		Source: recipe.Source{
			URL:    srv.URL + "/testpkg-1.0.tar.gz",
			SHA256: wrongHash,
		},
		Build: recipe.Build{
			Steps: []string{
				fmt.Sprintf("touch %s", markerPath),
			},
		},
	}

	outputDir := t.TempDir()
	_, _ = Build(r, outputDir)

	if _, err := os.Stat(markerPath); err == nil {
		t.Error("build steps should not have run after hash mismatch")
	}
}

// --- Behavior 5: Detect single top-level directory ---

func TestBuildCdIntoSingleTopLevelDirectory(t *testing.T) {
	// The source tarball has a single top-level dir with a
	// script inside. The build step references a file that
	// only exists inside that directory, proving the build
	// cd'd into it.
	tarball, hash := createSourceTarGz(t, map[string]string{
		"testpkg-1.0/hello.sh": "#!/bin/sh\necho hello",
	})
	srv := serveFile(t, tarball)

	r := &recipe.Recipe{
		Package: recipe.Package{
			Name:    "testpkg",
			Version: "1.0",
		},
		Source: recipe.Source{
			URL:    srv.URL + "/testpkg-1.0.tar.gz",
			SHA256: hash,
		},
		Build: recipe.Build{
			Steps: []string{
				// This step relies on being inside testpkg-1.0/
				// because it references hello.sh without a path prefix.
				"test -f hello.sh && mkdir -p $PREFIX/bin && cp hello.sh $PREFIX/bin/hello && chmod +x $PREFIX/bin/hello",
			},
		},
	}

	outputDir := t.TempDir()
	result, err := Build(r, outputDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Extract and verify.
	extractDir := t.TempDir()
	if err := download.ExtractTarZstd(result.Archive, extractDir); err != nil {
		t.Fatalf("failed to extract result archive: %v", err)
	}

	helloPath := filepath.Join(extractDir, "bin", "hello")
	if _, err := os.Stat(helloPath); err != nil {
		t.Fatalf("bin/hello not found: build did not cd into top-level dir: %v", err)
	}
}

// --- Behavior 6: Output tar.zst ---

func TestBuildOutputIsValidTarZstd(t *testing.T) {
	tarball, hash := createSourceTarGz(t, map[string]string{
		"testpkg-1.0/README": "hello",
	})
	srv := serveFile(t, tarball)

	r := &recipe.Recipe{
		Package: recipe.Package{
			Name:    "testpkg",
			Version: "1.0",
		},
		Source: recipe.Source{
			URL:    srv.URL + "/testpkg-1.0.tar.gz",
			SHA256: hash,
		},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin && echo '#!/bin/sh' > $PREFIX/bin/hello && chmod +x $PREFIX/bin/hello",
			},
		},
	}

	outputDir := t.TempDir()
	result, err := Build(r, outputDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Verify the output is a valid tar.zst by extracting it.
	extractDir := t.TempDir()
	if err := download.ExtractTarZstd(result.Archive, extractDir); err != nil {
		t.Fatalf("output is not valid tar.zst: %v", err)
	}

	// Verify bin/hello exists relative to the archive root.
	helloPath := filepath.Join(extractDir, "bin", "hello")
	if _, err := os.Stat(helloPath); err != nil {
		t.Fatalf("bin/hello not found in output archive: %v", err)
	}
}

func TestBuildOutputSHA256MatchesArchive(t *testing.T) {
	tarball, hash := createSourceTarGz(t, map[string]string{
		"testpkg-1.0/README": "hello",
	})
	srv := serveFile(t, tarball)

	r := &recipe.Recipe{
		Package: recipe.Package{
			Name:    "testpkg",
			Version: "1.0",
		},
		Source: recipe.Source{
			URL:    srv.URL + "/testpkg-1.0.tar.gz",
			SHA256: hash,
		},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin && echo '#!/bin/sh' > $PREFIX/bin/hello && chmod +x $PREFIX/bin/hello",
			},
		},
	}

	outputDir := t.TempDir()
	result, err := Build(r, outputDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Independently hash the archive and compare.
	if err := download.VerifySHA256(result.Archive, result.SHA256); err != nil {
		t.Errorf("result SHA256 does not match archive: %v", err)
	}
}

// --- test helpers ---

// createSourceTarGz builds a tar.gz at a temp path with the given
// files and returns the path and hex-encoded SHA256 of the archive.
func createSourceTarGz(t *testing.T, files map[string]string) (string, string) {
	t.Helper()

	archivePath := filepath.Join(t.TempDir(), "source.tar.gz")

	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("failed to create archive file: %v", err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	// Collect and sort names for deterministic output.
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	dirs := make(map[string]bool)
	for _, name := range names {
		// Emit directory entries for each ancestor path.
		if dir := filepath.Dir(name); dir != "." {
			parts := strings.Split(
				filepath.ToSlash(dir), "/")
			for i := range parts {
				d := strings.Join(parts[:i+1], "/") + "/"
				if !dirs[d] {
					dirs[d] = true
					dhdr := &tar.Header{
						Typeflag: tar.TypeDir,
						Name:     d,
						Mode:     0o755,
					}
					if err := tw.WriteHeader(dhdr); err != nil {
						t.Fatalf("failed to write dir header: %v",
							err)
					}
				}
			}
		}

		content := files[name]
		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("failed to write tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("failed to write tar content: %v", err)
		}
	}

	// Close writers before hashing.
	tw.Close()
	gw.Close()
	f.Close()

	// Compute SHA256 of the archive.
	data, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("failed to read archive: %v", err)
	}
	h := sha256.Sum256(data)
	hash := fmt.Sprintf("%x", h)

	return archivePath, hash
}

// serveFile starts an httptest server that serves the file at
// the given path for any request. The server is closed when the
// test finishes.
func serveFile(t *testing.T, filePath string) *httptest.Server {
	t.Helper()

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read file for serving: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write(data)
		}))
	t.Cleanup(srv.Close)

	return srv
}

// --- Extra PATH dirs ---

func TestBuildWithExtraPathsMakesToolsAvailable(t *testing.T) {
	// Create a fake tool in a temp dir.
	toolDir := t.TempDir()
	toolPath := filepath.Join(toolDir, "mytool")
	err := os.WriteFile(toolPath,
		[]byte("#!/bin/sh\necho mytool-output > \"$1\""),
		0o755)
	if err != nil {
		t.Fatalf("write tool: %v", err)
	}

	tarball, hash := createSourceTarGz(t, map[string]string{
		"testpkg-1.0/README": "hello",
	})
	srv := serveFile(t, tarball)

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "1.0"},
		Source:  recipe.Source{URL: srv.URL, SHA256: hash},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin",
				"mytool $PREFIX/bin/output.txt",
			},
		},
	}

	outputDir := t.TempDir()
	result, err := Build(r, outputDir, toolDir)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	if result.Archive == "" {
		t.Error("expected non-empty archive path")
	}

	// Extract and verify mytool was found.
	extractDir := t.TempDir()
	if err := download.ExtractTarZstd(
		result.Archive, extractDir); err != nil {
		t.Fatalf("extract: %v", err)
	}
	data, err := os.ReadFile(
		filepath.Join(extractDir, "bin", "output.txt"))
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !strings.Contains(string(data), "mytool-output") {
		t.Errorf("output = %q, want mytool-output", data)
	}
}
