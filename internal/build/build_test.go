package build

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/recipe"
	"github.com/ulikunitz/xz"
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
	result, err := Build(r, outputDir, false, nil)
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
	result, err := Build(r, outputDir, false, nil)
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
	result, err := Build(r, outputDir, false, nil)
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
	_, err := Build(r, outputDir, false, nil)
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
	_, err := Build(r, outputDir, false, nil)
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
	_, err := Build(r, outputDir, false, nil)
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
	_, err := Build(r, outputDir, false, nil)
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
	_, _ = Build(r, outputDir, false, nil)

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
	result, err := Build(r, outputDir, false, nil)
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
	result, err := Build(r, outputDir, false, nil)
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
	result, err := Build(r, outputDir, false, nil)
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

func createSourceTarXz(t *testing.T, files map[string]string) (string, string) {
	t.Helper()

	archivePath := filepath.Join(t.TempDir(), "source.tar.xz")

	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("failed to create archive file: %v", err)
	}
	defer f.Close()

	xw, err := xz.NewWriter(f)
	if err != nil {
		t.Fatalf("failed to create xz writer: %v", err)
	}
	defer xw.Close()

	tw := tar.NewWriter(xw)
	defer tw.Close()

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	dirs := make(map[string]bool)
	for _, name := range names {
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

	tw.Close()
	xw.Close()
	f.Close()

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
	result, err := Build(r, outputDir, false, &BuildDeps{
		BinDirs: []string{toolDir},
	})
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

// --- Behavior 8: BuildLocal uses local source directory ---

func TestBuildLocalSuccessReturnsResultWithArchiveAndSHA256(t *testing.T) {
	// Create a local source directory with a simple script.
	srcDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(srcDir, "hello.sh"),
		[]byte("#!/bin/sh\necho hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &recipe.Recipe{
		Package: recipe.Package{
			Name:    "testpkg",
			Version: "1.0",
		},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin && cp hello.sh $PREFIX/bin/hello && chmod +x $PREFIX/bin/hello",
			},
		},
	}

	outputDir := t.TempDir()
	result, err := BuildLocal(r, srcDir, outputDir, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Archive == "" {
		t.Error("expected Archive path to be set")
	}
	if result.SHA256 == "" {
		t.Error("expected SHA256 to be set")
	}

	// Verify the archive contains the built binary.
	extractDir := t.TempDir()
	if err := download.ExtractTarZstd(result.Archive, extractDir); err != nil {
		t.Fatalf("failed to extract: %v", err)
	}
	helloPath := filepath.Join(extractDir, "bin", "hello")
	if _, err := os.Stat(helloPath); err != nil {
		t.Fatalf("bin/hello not found in output: %v", err)
	}
}

func TestBuildLocalDoesNotRequireSourceSection(t *testing.T) {
	srcDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(srcDir, "README"),
		[]byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &recipe.Recipe{
		Package: recipe.Package{
			Name:    "testpkg",
			Version: "1.0",
		},
		// No Source section — BuildLocal should not need it.
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin && echo '#!/bin/sh' > $PREFIX/bin/hello && chmod +x $PREFIX/bin/hello",
			},
		},
	}

	outputDir := t.TempDir()
	result, err := BuildLocal(r, srcDir, outputDir, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Archive == "" {
		t.Error("expected non-empty archive path")
	}
}

func TestBuildLocalStepFailureReturnsError(t *testing.T) {
	srcDir := t.TempDir()

	r := &recipe.Recipe{
		Package: recipe.Package{
			Name:    "testpkg",
			Version: "1.0",
		},
		Build: recipe.Build{
			Steps: []string{"exit 1"},
		},
	}

	outputDir := t.TempDir()
	_, err := BuildLocal(r, srcDir, outputDir, false, nil)
	if err == nil {
		t.Fatal("expected error for failing build step")
	}
}

func TestBuildLocalWithExtraPaths(t *testing.T) {
	toolDir := t.TempDir()
	toolPath := filepath.Join(toolDir, "mytool")
	if err := os.WriteFile(toolPath,
		[]byte("#!/bin/sh\necho mytool-output > \"$1\""),
		0o755); err != nil {
		t.Fatal(err)
	}

	srcDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(srcDir, "README"),
		[]byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "1.0"},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin",
				"mytool $PREFIX/bin/output.txt",
			},
		},
	}

	outputDir := t.TempDir()
	result, err := BuildLocal(r, srcDir, outputDir, false, &BuildDeps{
		BinDirs: []string{toolDir},
	})
	if err != nil {
		t.Fatalf("BuildLocal error: %v", err)
	}

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

// --- Behavior 9: resolveTools creates isolated symlink dir ---

func TestResolveToolsCreatesSymlinks(t *testing.T) {
	// Create a fake bin dir with a tool and a decoy.
	fakeBin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	// Fake tool binary.
	if err := os.WriteFile(
		filepath.Join(fakeBin, "fakecargo"),
		[]byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Decoy that should NOT be linked.
	if err := os.WriteFile(
		filepath.Join(fakeBin, "ls"),
		[]byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	toolsDir := t.TempDir()
	resolveTools(toolsDir, []string{
		filepath.Join(fakeBin, "fakecargo"),
	})

	// fakecargo should be symlinked.
	if _, err := os.Lstat(
		filepath.Join(toolsDir, "fakecargo")); err != nil {
		t.Errorf("expected fakecargo symlink: %v", err)
	}

	// ls should NOT be in the tools dir.
	if _, err := os.Lstat(
		filepath.Join(toolsDir, "ls")); err == nil {
		t.Error("ls should not be in isolated tools dir")
	}
}

// --- Behavior 10: Dynamic linker paths in buildEnv ---

func TestBuildEnvIncludesDynamicLinkerPath(t *testing.T) {
	deps := &BuildDeps{
		StoreDirs: []string{"/fake/store/pkg"},
	}
	env := buildEnv("/tmp/prefix", "4", "1.0.0", "", false, deps)

	envMap := envToMap(env)

	// Should always have LIBRARY_PATH.
	if _, ok := envMap["LIBRARY_PATH"]; !ok {
		t.Error("expected LIBRARY_PATH in env")
	}

	// Should have cmake search paths.
	if _, ok := envMap["CMAKE_LIBRARY_PATH"]; !ok {
		t.Error("expected CMAKE_LIBRARY_PATH in env")
	}
	if _, ok := envMap["CMAKE_INCLUDE_PATH"]; !ok {
		t.Error("expected CMAKE_INCLUDE_PATH in env")
	}

	// Platform-specific dynamic linker path.
	switch runtime.GOOS {
	case "linux":
		val, ok := envMap["LD_LIBRARY_PATH"]
		if !ok {
			t.Fatal("expected LD_LIBRARY_PATH on linux")
		}
		if val != envMap["LIBRARY_PATH"] {
			t.Errorf("LD_LIBRARY_PATH = %q, want %q",
				val, envMap["LIBRARY_PATH"])
		}
	case "darwin":
		val, ok := envMap["DYLD_FALLBACK_LIBRARY_PATH"]
		if !ok {
			t.Fatal(
				"expected DYLD_FALLBACK_LIBRARY_PATH on darwin")
		}
		if val != envMap["LIBRARY_PATH"] {
			t.Errorf(
				"DYLD_FALLBACK_LIBRARY_PATH = %q, want %q",
				val, envMap["LIBRARY_PATH"])
		}
	}
}

func TestBuildEnvNoDynamicLinkerPathWithoutDeps(t *testing.T) {
	env := buildEnv("/tmp/prefix", "4", "1.0.0", "", false, nil)
	envMap := envToMap(env)

	if _, ok := envMap["LD_LIBRARY_PATH"]; ok {
		t.Error("LD_LIBRARY_PATH should not be set without deps")
	}
	if _, ok := envMap["DYLD_FALLBACK_LIBRARY_PATH"]; ok {
		t.Error(
			"DYLD_FALLBACK_LIBRARY_PATH should not be set " +
				"without deps")
	}
	if _, ok := envMap["CMAKE_LIBRARY_PATH"]; ok {
		t.Error(
			"CMAKE_LIBRARY_PATH should not be set without deps")
	}
	if _, ok := envMap["CMAKE_INCLUDE_PATH"]; ok {
		t.Error(
			"CMAKE_INCLUDE_PATH should not be set without deps")
	}
}

// --- Behavior 11: Platform variables in buildEnv ---

func TestBuildEnvIncludesPlatformVars(t *testing.T) {
	env := buildEnv("/tmp/prefix", "4", "1.0.0", "", false, nil)
	envMap := envToMap(env)

	if val, ok := envMap["OS"]; !ok || val != runtime.GOOS {
		t.Errorf("OS = %q, want %q", val, runtime.GOOS)
	}
	if val, ok := envMap["ARCH"]; !ok || val != runtime.GOARCH {
		t.Errorf("ARCH = %q, want %q", val, runtime.GOARCH)
	}
	want := runtime.GOOS + "-" + runtime.GOARCH
	if val, ok := envMap["PLATFORM"]; !ok || val != want {
		t.Errorf("PLATFORM = %q, want %q", val, want)
	}
}

// --- Behavior 12: checkPlatform ---

func TestCheckPlatformEmptyListReturnsNil(t *testing.T) {
	r := &recipe.Recipe{
		Package: recipe.Package{
			Name:      "testpkg",
			Platforms: nil,
		},
	}
	if err := checkPlatform(r); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestCheckPlatformCurrentInListReturnsNil(t *testing.T) {
	current := runtime.GOOS + "-" + runtime.GOARCH
	r := &recipe.Recipe{
		Package: recipe.Package{
			Name:      "testpkg",
			Platforms: []string{current},
		},
	}
	if err := checkPlatform(r); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestCheckPlatformCurrentNotInListReturnsError(t *testing.T) {
	r := &recipe.Recipe{
		Package: recipe.Package{
			Name:      "testpkg",
			Platforms: []string{"fakeos-fakearch"},
		},
	}
	err := checkPlatform(r)
	if err == nil {
		t.Fatal("expected error for unsupported platform")
	}
	if !errors.Is(err, ErrUnsupportedPlatform) {
		t.Errorf("expected ErrUnsupportedPlatform, got %v", err)
	}
}

// --- Behavior 13: VERSION variable in buildEnv ---

func TestBuildEnvIncludesVersion(t *testing.T) {
	env := buildEnv("/tmp/prefix", "4", "1.8.1", "", false, nil)
	envMap := envToMap(env)

	if val, ok := envMap["VERSION"]; !ok || val != "1.8.1" {
		t.Errorf("VERSION = %q, want %q", val, "1.8.1")
	}
}

// --- Behavior 14: SystemDeps returns correct deps ---

func TestSystemDepsReturnsCorrectDeps(t *testing.T) {
	tests := []struct {
		system string
		want   []string
	}{
		{"cmake", []string{"cmake"}},
		{"go", []string{"go"}},
		{"cargo", []string{"rust"}},
		{"", nil},
		{"autotools", nil},
	}
	for _, tt := range tests {
		t.Run(tt.system, func(t *testing.T) {
			got := SystemDeps(tt.system)
			if len(got) != len(tt.want) {
				t.Fatalf("SystemDeps(%q) = %v, want %v",
					tt.system, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("SystemDeps(%q)[%d] = %q, want %q",
						tt.system, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// --- Behavior 15: CMAKE_PREFIX_PATH in buildEnv ---

func TestBuildEnvCMakePrefixPath(t *testing.T) {
	deps := &BuildDeps{
		StoreDirs: []string{"/fake/store/a", "/fake/store/b"},
	}
	env := buildEnv("/tmp/prefix", "4", "1.0.0", "cmake", false, deps)
	envMap := envToMap(env)

	val, ok := envMap["CMAKE_PREFIX_PATH"]
	if !ok {
		t.Fatal("expected CMAKE_PREFIX_PATH in env")
	}
	// cmake uses semicolons as separators.
	want := "/fake/store/a;/fake/store/b"
	if val != want {
		t.Errorf("CMAKE_PREFIX_PATH = %q, want %q", val, want)
	}
}

func TestBuildEnvNoCMakePrefixPathWithoutCMake(t *testing.T) {
	deps := &BuildDeps{
		StoreDirs: []string{"/fake/store/a"},
	}
	env := buildEnv("/tmp/prefix", "4", "1.0.0", "go", false, deps)
	envMap := envToMap(env)

	if _, ok := envMap["CMAKE_PREFIX_PATH"]; ok {
		t.Error("CMAKE_PREFIX_PATH should not be set for non-cmake systems")
	}
}

func TestBuildEnvNoCMakePrefixPathWithoutDeps(t *testing.T) {
	env := buildEnv("/tmp/prefix", "4", "1.0.0", "cmake", false, nil)
	envMap := envToMap(env)

	if _, ok := envMap["CMAKE_PREFIX_PATH"]; ok {
		t.Error("CMAKE_PREFIX_PATH should not be set without deps")
	}
}

// --- Behavior 16: Compiler flags in buildEnv ---

func TestBuildEnvReleaseFlagsDefault(t *testing.T) {
	env := buildEnv("/tmp/prefix", "4", "1.0.0", "", false, nil)
	envMap := envToMap(env)

	if val := envMap["CFLAGS"]; val != "-O2" {
		t.Errorf("CFLAGS = %q, want %q", val, "-O2")
	}
	if val := envMap["CXXFLAGS"]; val != "-O2" {
		t.Errorf("CXXFLAGS = %q, want %q", val, "-O2")
	}
	if val := envMap["LDFLAGS"]; val != "-Wl,-S" {
		t.Errorf("LDFLAGS = %q, want %q", val, "-Wl,-S")
	}
}

func TestBuildEnvDebugFlags(t *testing.T) {
	env := buildEnv("/tmp/prefix", "4", "1.0.0", "", true, nil)
	envMap := envToMap(env)

	if val := envMap["CFLAGS"]; val != "-O0 -g" {
		t.Errorf("CFLAGS = %q, want %q", val, "-O0 -g")
	}
	if val := envMap["CXXFLAGS"]; val != "-O0 -g" {
		t.Errorf("CXXFLAGS = %q, want %q", val, "-O0 -g")
	}
	if val, ok := envMap["LDFLAGS"]; ok && val != "" {
		t.Errorf("LDFLAGS should be empty in debug, got %q", val)
	}
}

func TestBuildEnvZeroARDateAlwaysSet(t *testing.T) {
	// Release mode.
	env := buildEnv("/tmp/prefix", "4", "1.0.0", "", false, nil)
	envMap := envToMap(env)
	if envMap["ZERO_AR_DATE"] != "1" {
		t.Error("ZERO_AR_DATE not set in release mode")
	}

	// Debug mode.
	env = buildEnv("/tmp/prefix", "4", "1.0.0", "", true, nil)
	envMap = envToMap(env)
	if envMap["ZERO_AR_DATE"] != "1" {
		t.Error("ZERO_AR_DATE not set in debug mode")
	}
}

func TestBuildEnvUserCFLAGSNotOverridden(t *testing.T) {
	t.Setenv("CFLAGS", "-march=native")

	env := buildEnv("/tmp/prefix", "4", "1.0.0", "", false, nil)
	envMap := envToMap(env)

	if val := envMap["CFLAGS"]; val != "-march=native" {
		t.Errorf("CFLAGS = %q, want user-set %q",
			val, "-march=native")
	}
}

// --- Behavior 17: sourceExtension extracts archive suffix ---

func TestSourceExtensionExtractsCorrectSuffix(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://github.com/foo/bar/archive/refs/tags/v1.0.tar.gz", ".tar.gz"},
		{"https://github.com/foo/bar/releases/download/v1.0/bar-1.0.tar.xz", ".tar.xz"},
		{"https://github.com/foo/bar/releases/download/v1.0/bar-1.0.tar.bz2", ".tar.bz2"},
		{"https://github.com/foo/bar/archive/refs/tags/v1.0.tar.zst", ".tar.zst"},
		{"https://github.com/foo/bar/releases/download/v1.0/bar-1.0.tgz", ".tgz"},
		{"https://github.com/foo/bar/releases/download/v1.0/bar-1.0.zip", ".zip"},
		{"https://example.com/unknown-format.dat", ".tar.gz"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := sourceExtension(tt.url)
			if got != tt.want {
				t.Errorf("sourceExtension(%q) = %q, want %q",
					tt.url, got, tt.want)
			}
		})
	}
}

// --- Behavior 18: Build handles .tar.xz sources ---

func TestBuildSuccessWithTarXzSource(t *testing.T) {
	tarball, hash := createSourceTarXz(t, map[string]string{
		"testpkg-1.0/README": "hello",
	})
	srv := serveFile(t, tarball)

	r := &recipe.Recipe{
		Package: recipe.Package{
			Name:    "testpkg",
			Version: "1.0",
		},
		Source: recipe.Source{
			URL:    srv.URL + "/testpkg-1.0.tar.xz",
			SHA256: hash,
		},
		Build: recipe.Build{
			Steps: []string{
				"mkdir -p $PREFIX/bin && echo '#!/bin/sh' > $PREFIX/bin/hello && chmod +x $PREFIX/bin/hello",
			},
		},
	}

	outputDir := t.TempDir()
	result, err := Build(r, outputDir, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Archive == "" {
		t.Error("expected Archive path to be set")
	}
}

// envToMap converts a []string env slice to a map.
func envToMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		m[k] = v
	}
	return m
}
