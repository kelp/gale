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
	"os/exec"
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
	storeDir := t.TempDir()
	os.MkdirAll(filepath.Join(storeDir, "lib"), 0o755)
	os.MkdirAll(filepath.Join(storeDir, "include"), 0o755)
	deps := &BuildDeps{
		StoreDirs: []string{storeDir},
	}
	env, _, _ := buildEnv(&BuildContext{PrefixDir: "/tmp/prefix", Jobs: "4", Version: "1.0.0", System: "", Debug: false, Deps: deps})

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
	env, _, _ := buildEnv(&BuildContext{PrefixDir: "/tmp/prefix", Jobs: "4", Version: "1.0.0", System: "", Debug: false, Deps: nil})
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
	env, _, _ := buildEnv(&BuildContext{PrefixDir: "/tmp/prefix", Jobs: "4", Version: "1.0.0", System: "", Debug: false, Deps: nil})
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
	env, _, _ := buildEnv(&BuildContext{PrefixDir: "/tmp/prefix", Jobs: "4", Version: "1.8.1", System: "", Debug: false, Deps: nil})
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
		{"zig", []string{"zig"}},
		{"python", []string{"python"}},
		{"ruby", []string{"ruby"}},
		{"", nil},
		{"autotools", nil},
		{"meson", nil},
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

func TestBuildEnvHeaderOnlyDepStillSetsIncludePath(t *testing.T) {
	// A dep that ships only include/ (header-only library)
	// alongside a bin-only dep (e.g. cmake, no lib/) must
	// still produce C_INCLUDE_PATH / CMAKE_INCLUDE_PATH.
	// Regression: earlier revision gated ALL search-path
	// env vars on libPathStr != "", which silently dropped
	// include paths when no dep had a lib/ dir.
	headerOnly := t.TempDir()
	os.MkdirAll(filepath.Join(headerOnly, "include"), 0o755)

	binOnly := t.TempDir()
	os.MkdirAll(filepath.Join(binOnly, "bin"), 0o755)

	deps := &BuildDeps{
		StoreDirs: []string{headerOnly, binOnly},
	}
	env, _, _ := buildEnv(&BuildContext{
		PrefixDir: "/tmp/prefix",
		Jobs:      "4",
		Version:   "1.0.0",
		System:    "",
		Deps:      deps,
	})
	envMap := envToMap(env)

	if val, ok := envMap["C_INCLUDE_PATH"]; !ok || val == "" {
		t.Error("expected C_INCLUDE_PATH to be set for header-only dep")
	}
	if val, ok := envMap["CMAKE_INCLUDE_PATH"]; !ok || val == "" {
		t.Error("expected CMAKE_INCLUDE_PATH to be set for header-only dep")
	}
	if _, ok := envMap["LIBRARY_PATH"]; ok {
		t.Error("LIBRARY_PATH should not be set when no dep has lib/")
	}
}

func TestBuildEnvBinOnlyDepStillSetsCMakePrefixPath(t *testing.T) {
	// A cmake-system build with a bin-only dep must still
	// receive CMAKE_PREFIX_PATH, even though the dep has no
	// lib/ or include/ subdirs. Regression against the same
	// over-aggressive libPathStr gating.
	binOnly := t.TempDir()
	os.MkdirAll(filepath.Join(binOnly, "bin"), 0o755)

	deps := &BuildDeps{
		StoreDirs: []string{binOnly},
	}
	env, _, _ := buildEnv(&BuildContext{
		PrefixDir: "/tmp/prefix",
		Jobs:      "4",
		Version:   "1.0.0",
		System:    "cmake",
		Deps:      deps,
	})
	envMap := envToMap(env)

	if val, ok := envMap["CMAKE_PREFIX_PATH"]; !ok || val == "" {
		t.Error("expected CMAKE_PREFIX_PATH to be set for bin-only dep under cmake system")
	}
}

// --- Behavior 15: CMAKE_PREFIX_PATH in buildEnv ---

func TestBuildEnvCMakePrefixPath(t *testing.T) {
	storeA := t.TempDir()
	storeB := t.TempDir()
	for _, d := range []string{storeA, storeB} {
		os.MkdirAll(filepath.Join(d, "lib"), 0o755)
	}
	deps := &BuildDeps{
		StoreDirs: []string{storeA, storeB},
	}
	env, _, _ := buildEnv(&BuildContext{PrefixDir: "/tmp/prefix", Jobs: "4", Version: "1.0.0", System: "cmake", Debug: false, Deps: deps})
	envMap := envToMap(env)

	val, ok := envMap["CMAKE_PREFIX_PATH"]
	if !ok {
		t.Fatal("expected CMAKE_PREFIX_PATH in env")
	}
	// cmake uses semicolons as separators.
	want := storeA + ";" + storeB
	if val != want {
		t.Errorf("CMAKE_PREFIX_PATH = %q, want %q", val, want)
	}
}

func TestBuildEnvNoCMakePrefixPathWithoutCMake(t *testing.T) {
	deps := &BuildDeps{
		StoreDirs: []string{"/fake/store/a"},
	}
	env, _, _ := buildEnv(&BuildContext{PrefixDir: "/tmp/prefix", Jobs: "4", Version: "1.0.0", System: "go", Debug: false, Deps: deps})
	envMap := envToMap(env)

	if _, ok := envMap["CMAKE_PREFIX_PATH"]; ok {
		t.Error("CMAKE_PREFIX_PATH should not be set for non-cmake systems")
	}
}

func TestBuildEnvNoCMakePrefixPathWithoutDeps(t *testing.T) {
	env, _, _ := buildEnv(&BuildContext{PrefixDir: "/tmp/prefix", Jobs: "4", Version: "1.0.0", System: "cmake", Debug: false, Deps: nil})
	envMap := envToMap(env)

	if _, ok := envMap["CMAKE_PREFIX_PATH"]; ok {
		t.Error("CMAKE_PREFIX_PATH should not be set without deps")
	}
}

// --- Behavior 16: Compiler flags in buildEnv ---

func TestBuildEnvReleaseFlagsDefault(t *testing.T) {
	env, _, _ := buildEnv(&BuildContext{PrefixDir: "/tmp/prefix", Jobs: "4", Version: "1.0.0", System: "", Debug: false, Deps: nil})
	envMap := envToMap(env)

	if val := envMap["CFLAGS"]; val != "-O2" {
		t.Errorf("CFLAGS = %q, want %q", val, "-O2")
	}
	if val := envMap["CXXFLAGS"]; val != "-O2" {
		t.Errorf("CXXFLAGS = %q, want %q", val, "-O2")
	}
	ldflags := envMap["LDFLAGS"]
	if !strings.Contains(ldflags, "-Wl,-S") {
		t.Errorf("LDFLAGS = %q, want -Wl,-S", ldflags)
	}
}

func TestBuildEnvDebugFlags(t *testing.T) {
	env, _, _ := buildEnv(&BuildContext{PrefixDir: "/tmp/prefix", Jobs: "4", Version: "1.0.0", System: "", Debug: true, Deps: nil})
	envMap := envToMap(env)

	if val := envMap["CFLAGS"]; val != "-O0 -g" {
		t.Errorf("CFLAGS = %q, want %q", val, "-O0 -g")
	}
	if val := envMap["CXXFLAGS"]; val != "-O0 -g" {
		t.Errorf("CXXFLAGS = %q, want %q", val, "-O0 -g")
	}
	ldflags := envMap["LDFLAGS"]
	if strings.Contains(ldflags, "-Wl,-S") {
		t.Errorf("LDFLAGS should not contain -Wl,-S in debug, got %q", ldflags)
	}
}

func TestBuildEnvZeroARDateAlwaysSet(t *testing.T) {
	// Release mode.
	env, _, _ := buildEnv(&BuildContext{PrefixDir: "/tmp/prefix", Jobs: "4", Version: "1.0.0", System: "", Debug: false, Deps: nil})
	envMap := envToMap(env)
	if envMap["ZERO_AR_DATE"] != "1" {
		t.Error("ZERO_AR_DATE not set in release mode")
	}

	// Debug mode.
	env, _, _ = buildEnv(&BuildContext{PrefixDir: "/tmp/prefix", Jobs: "4", Version: "1.0.0", System: "", Debug: true, Deps: nil})
	envMap = envToMap(env)
	if envMap["ZERO_AR_DATE"] != "1" {
		t.Error("ZERO_AR_DATE not set in debug mode")
	}
}

func TestBuildEnvUserCFLAGSNotOverridden(t *testing.T) {
	t.Setenv("CFLAGS", "-march=native")

	env, _, _ := buildEnv(&BuildContext{PrefixDir: "/tmp/prefix", Jobs: "4", Version: "1.0.0", System: "", Debug: false, Deps: nil})
	envMap := envToMap(env)

	if val := envMap["CFLAGS"]; val != "-march=native" {
		t.Errorf("CFLAGS = %q, want user-set %q",
			val, "-march=native")
	}
}

func TestBuildEnvExportsDepCPPFLAGSAndDepLDFLAGS(t *testing.T) {
	depDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(depDir, "include"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(depDir, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}

	deps := &BuildDeps{
		StoreDirs: []string{depDir},
	}
	env, _, _ := buildEnv(&BuildContext{PrefixDir: "/tmp/prefix", Jobs: "4", Version: "1.0.0", System: "", Debug: false, Deps: deps})
	envMap := envToMap(env)

	// DEP_CPPFLAGS should contain -I for dep include dir.
	depCPP, ok := envMap["DEP_CPPFLAGS"]
	if !ok {
		t.Fatal("expected DEP_CPPFLAGS in env")
	}
	wantInc := "-I" + filepath.Join(depDir, "include")
	if !strings.Contains(depCPP, wantInc) {
		t.Errorf("DEP_CPPFLAGS = %q, want to contain %q",
			depCPP, wantInc)
	}

	// DEP_LDFLAGS should contain -L for dep lib dir.
	depLD, ok := envMap["DEP_LDFLAGS"]
	if !ok {
		t.Fatal("expected DEP_LDFLAGS in env")
	}
	wantLib := "-L" + filepath.Join(depDir, "lib")
	if !strings.Contains(depLD, wantLib) {
		t.Errorf("DEP_LDFLAGS = %q, want to contain %q",
			depLD, wantLib)
	}
}

func TestBuildEnvNoDepFlagsWithoutDeps(t *testing.T) {
	env, _, _ := buildEnv(&BuildContext{PrefixDir: "/tmp/prefix", Jobs: "4", Version: "1.0.0", System: "", Debug: false, Deps: nil})
	envMap := envToMap(env)

	if _, ok := envMap["DEP_CPPFLAGS"]; ok {
		t.Error("DEP_CPPFLAGS should not be set without deps")
	}
	if _, ok := envMap["DEP_LDFLAGS"]; ok {
		t.Error("DEP_LDFLAGS should not be set without deps")
	}
}

// --- BuildContext helper methods ---

func TestBaseEnvContainsRequiredVars(t *testing.T) {
	bc := &BuildContext{
		PrefixDir: "/test/prefix",
		Jobs:      "8",
		Version:   "2.0.0",
	}
	env := bc.baseEnv("/home/test", "/usr/bin:/bin", "/tmp")
	m := envToMap(env)

	expect := map[string]string{
		"PREFIX":   "/test/prefix",
		"VERSION":  "2.0.0",
		"JOBS":     "8",
		"HOME":     "/home/test",
		"TMPDIR":   "/tmp",
		"LANG":     "en_US.UTF-8",
		"PATH":     "/usr/bin:/bin",
		"OS":       runtime.GOOS,
		"ARCH":     runtime.GOARCH,
		"PLATFORM": runtime.GOOS + "-" + runtime.GOARCH,
	}
	for k, want := range expect {
		if got, ok := m[k]; !ok {
			t.Errorf("missing %s", k)
		} else if got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestDepSearchPathsComputesPaths(t *testing.T) {
	// Use real temp dirs with lib/include/pkgconfig so
	// the existence checks pass.
	storeA := t.TempDir()
	storeB := t.TempDir()
	for _, d := range []string{storeA, storeB} {
		os.MkdirAll(filepath.Join(d, "lib", "pkgconfig"), 0o755)
		os.MkdirAll(filepath.Join(d, "include"), 0o755)
	}

	bc := &BuildContext{
		System: "cmake",
		Deps: &BuildDeps{
			StoreDirs: []string{storeA, storeB},
		},
	}
	libPath, incPath, pcPath, cmakePath := bc.depSearchPaths()

	wantLib := filepath.Join(storeA, "lib") + ":" + filepath.Join(storeB, "lib")
	if libPath != wantLib {
		t.Errorf("libPath = %q, want %q", libPath, wantLib)
	}
	wantInc := filepath.Join(storeA, "include") + ":" + filepath.Join(storeB, "include")
	if incPath != wantInc {
		t.Errorf("incPath = %q, want %q", incPath, wantInc)
	}
	wantPC := filepath.Join(storeA, "lib", "pkgconfig") + ":" + filepath.Join(storeB, "lib", "pkgconfig")
	if pcPath != wantPC {
		t.Errorf("pcPath = %q, want %q", pcPath, wantPC)
	}
	wantCmake := storeA + ";" + storeB
	if cmakePath != wantCmake {
		t.Errorf("cmakePath = %q, want %q", cmakePath, wantCmake)
	}
}

func TestDepSearchPathsSkipsNonexistentLibDir(t *testing.T) {
	// A dep with no lib/ dir (e.g. cmake, which installs
	// only to share/) must not contribute a bogus lib path
	// to LIBRARY_PATH. clang warns on missing search paths
	// and that stderr output breaks configure scripts with
	// strict LDFLAGS validation (Ruby).
	depWithLib := t.TempDir()
	os.MkdirAll(filepath.Join(depWithLib, "lib"), 0o755)
	os.MkdirAll(filepath.Join(depWithLib, "include"), 0o755)

	depWithoutLib := t.TempDir()
	os.MkdirAll(filepath.Join(depWithoutLib, "share"), 0o755)

	bc := &BuildContext{
		Deps: &BuildDeps{
			StoreDirs: []string{depWithLib, depWithoutLib},
		},
	}
	libPath, incPath, _, _ := bc.depSearchPaths()

	if strings.Contains(libPath, depWithoutLib) {
		t.Errorf("libPath must not contain dep %q with no lib/ dir, got %q",
			depWithoutLib, libPath)
	}
	if !strings.Contains(libPath, depWithLib) {
		t.Errorf("libPath should contain dep %q with real lib/ dir, got %q",
			depWithLib, libPath)
	}
	if strings.Contains(incPath, depWithoutLib) {
		t.Errorf("incPath must not contain dep %q with no include/ dir, got %q",
			depWithoutLib, incPath)
	}
}

func TestDepSearchPathsNoDeps(t *testing.T) {
	bc := &BuildContext{}
	libPath, incPath, pcPath, cmakePath := bc.depSearchPaths()
	if libPath != "" || incPath != "" || pcPath != "" || cmakePath != "" {
		t.Errorf("expected all empty, got lib=%q inc=%q pc=%q cmake=%q",
			libPath, incPath, pcPath, cmakePath)
	}
}

func TestDepSearchPathsNoCMakeWithoutSystem(t *testing.T) {
	bc := &BuildContext{
		System: "go",
		Deps: &BuildDeps{
			StoreDirs: []string{"/store/a/1.0"},
		},
	}
	_, _, _, cmakePath := bc.depSearchPaths()
	if cmakePath != "" {
		t.Errorf("cmakePath = %q, want empty for non-cmake system", cmakePath)
	}
}

func TestPerDepEnvGeneratesDepVars(t *testing.T) {
	bc := &BuildContext{
		Deps: &BuildDeps{
			NamedDirs: map[string]string{
				"openssl": "/store/openssl/3.0",
				"lib-foo": "/store/lib-foo/1.0",
			},
		},
	}
	env, _, _ := bc.perDepEnv()
	m := envToMap(env)

	if m["DEP_OPENSSL"] != "/store/openssl/3.0" {
		t.Errorf("DEP_OPENSSL = %q, want %q", m["DEP_OPENSSL"], "/store/openssl/3.0")
	}
	if m["DEP_LIB_FOO"] != "/store/lib-foo/1.0" {
		t.Errorf("DEP_LIB_FOO = %q, want %q", m["DEP_LIB_FOO"], "/store/lib-foo/1.0")
	}
}

func TestPerDepEnvNoDeps(t *testing.T) {
	bc := &BuildContext{}
	env, cppflags, ldflags := bc.perDepEnv()
	if len(env) != 0 {
		t.Errorf("expected no env vars, got %d", len(env))
	}
	if cppflags != "" || ldflags != "" {
		t.Errorf("expected empty flags, got cpp=%q ld=%q", cppflags, ldflags)
	}
}

func TestPerDepEnvComputesDepFlags(t *testing.T) {
	// Create temp dirs with include/ and lib/ so os.Stat succeeds.
	storeDir := t.TempDir()
	os.MkdirAll(filepath.Join(storeDir, "include"), 0o755)
	os.MkdirAll(filepath.Join(storeDir, "lib"), 0o755)

	bc := &BuildContext{
		Deps: &BuildDeps{
			StoreDirs: []string{storeDir},
			NamedDirs: map[string]string{"test": storeDir},
		},
	}
	env, cppflags, ldflags := bc.perDepEnv()
	m := envToMap(env)

	if m["DEP_CPPFLAGS"] == "" {
		t.Error("expected DEP_CPPFLAGS to be set")
	}
	if m["DEP_LDFLAGS"] == "" {
		t.Error("expected DEP_LDFLAGS to be set")
	}
	if cppflags == "" {
		t.Error("expected non-empty cppflags")
	}
	if ldflags == "" {
		t.Error("expected non-empty ldflags")
	}
}

func TestPerDepEnvDoesNotInjectRpathOnDarwin(t *testing.T) {
	// Rpath is added post-build by AddDepRpaths via
	// install_name_tool. Putting -Wl,-rpath in LDFLAGS
	// breaks configure scripts that validate LDFLAGS
	// (e.g. Ruby) and creates duplicate LC_RPATH entries.
	storeDir := t.TempDir()
	os.MkdirAll(filepath.Join(storeDir, "include"), 0o755)
	os.MkdirAll(filepath.Join(storeDir, "lib"), 0o755)

	bc := &BuildContext{
		Deps: &BuildDeps{
			StoreDirs: []string{storeDir},
		},
	}
	_, _, ldflags := bc.perDepEnv()

	if strings.Contains(ldflags, "-Wl,-rpath") {
		t.Errorf("depLDFLAGS must not contain -Wl,-rpath (handled post-build by AddDepRpaths), got %q", ldflags)
	}
	if !strings.Contains(ldflags, "-L"+filepath.Join(storeDir, "lib")) {
		t.Errorf("depLDFLAGS should still contain -L for dep lib dir, got %q", ldflags)
	}
}

func TestCompilerFlagsRelease(t *testing.T) {
	bc := &BuildContext{Debug: false}
	flags := bc.compilerFlags("", "")
	m := envToMap(flags)

	if m["CFLAGS"] != "-O2" {
		t.Errorf("CFLAGS = %q, want -O2", m["CFLAGS"])
	}
	if m["CXXFLAGS"] != "-O2" {
		t.Errorf("CXXFLAGS = %q, want -O2", m["CXXFLAGS"])
	}
	if !strings.Contains(m["LDFLAGS"], "-Wl,-S") {
		t.Errorf("LDFLAGS = %q, want to contain -Wl,-S", m["LDFLAGS"])
	}
}

func TestCompilerFlagsDebug(t *testing.T) {
	bc := &BuildContext{Debug: true}
	flags := bc.compilerFlags("", "")
	m := envToMap(flags)

	if m["CFLAGS"] != "-O0 -g" {
		t.Errorf("CFLAGS = %q, want '-O0 -g'", m["CFLAGS"])
	}
	if m["CXXFLAGS"] != "-O0 -g" {
		t.Errorf("CXXFLAGS = %q, want '-O0 -g'", m["CXXFLAGS"])
	}
}

func TestCompilerFlagsWithDepFlags(t *testing.T) {
	bc := &BuildContext{Debug: false}
	flags := bc.compilerFlags("-I/foo/include", "-L/foo/lib")
	m := envToMap(flags)

	if m["CPPFLAGS"] != "-I/foo/include" {
		t.Errorf("CPPFLAGS = %q, want '-I/foo/include'", m["CPPFLAGS"])
	}
	if !strings.Contains(m["LDFLAGS"], "-L/foo/lib") {
		t.Errorf("LDFLAGS = %q, want to contain -L/foo/lib", m["LDFLAGS"])
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

// --- Behavior 19: fixupShebangs rewrites build-prefix shebangs ---

func TestFixupShebangsRewritesPrefixShebang(t *testing.T) {
	prefixDir := t.TempDir()
	binDir := filepath.Join(prefixDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Script with a shebang pointing into the prefix.
	script := filepath.Join(binDir, "pip")
	content := "#!" + prefixDir + "/bin/python3.13\nimport sys\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := fixupShebangs(prefixDir); err != nil {
		t.Fatalf("fixupShebangs: %v", err)
	}

	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}

	got := strings.SplitN(string(data), "\n", 2)
	if got[0] != "#!/usr/bin/env python3.13" {
		t.Errorf("shebang = %q, want %q",
			got[0], "#!/usr/bin/env python3.13")
	}
	// Body preserved.
	if got[1] != "import sys\n" {
		t.Errorf("body = %q, want %q", got[1], "import sys\n")
	}
}

func TestFixupShebangsSkipsNonPrefixShebang(t *testing.T) {
	prefixDir := t.TempDir()
	binDir := filepath.Join(prefixDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Script with a system shebang — should not be changed.
	script := filepath.Join(binDir, "tool")
	content := "#!/usr/bin/env bash\necho hello\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := fixupShebangs(prefixDir); err != nil {
		t.Fatalf("fixupShebangs: %v", err)
	}

	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Errorf("content changed: %q", string(data))
	}
}

func TestFixupShebangsSkipsBinaries(t *testing.T) {
	prefixDir := t.TempDir()
	binDir := filepath.Join(prefixDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Binary file — should not be touched.
	binary := filepath.Join(binDir, "hello")
	content := []byte{0x7f, 'E', 'L', 'F', 0, 0, 0, 0}
	if err := os.WriteFile(binary, content, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := fixupShebangs(prefixDir); err != nil {
		t.Fatalf("fixupShebangs: %v", err)
	}

	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(string(data), string(content)) {
		t.Error("binary content was modified")
	}
}

// --- BUG-2: buildEnv propagates MkdirTemp failure ---

func TestBuildEnvReturnsNilOnTmpDirFailure(t *testing.T) {
	// When MkdirTemp fails (e.g., TmpDir returns empty
	// string pointing to a non-writable location), buildEnv
	// should return nil env instead of falling back to a
	// shared fixed path.
	//
	// We can't easily simulate MkdirTemp failure in a unit
	// test without mocking, but we can verify the fixed
	// fallback path is no longer used by checking that
	// buildEnv never produces a PATH containing a
	// non-unique "gale-tools" dir (without random suffix).
	env, cleanup, err := buildEnv(&BuildContext{PrefixDir: "/tmp/prefix", Jobs: "4", Version: "1.0.0", System: "", Debug: false, Deps: nil})
	if cleanup != nil {
		defer cleanup()
	}

	if env == nil {
		// MkdirTemp actually failed in test env — that's
		// fine, the important thing is no shared fallback.
		return
	}
	if err != nil {
		// Error returned properly — test passed.
		return
	}

	envMap := envToMap(env)
	pathVal := envMap["PATH"]
	toolsDir := strings.SplitN(pathVal, ":", 2)[0]

	// The toolsDir should contain a random suffix from
	// MkdirTemp, not the fixed "gale-tools" name.
	if filepath.Base(toolsDir) == "gale-tools" {
		t.Error("toolsDir uses fixed shared name; " +
			"expected unique MkdirTemp path")
	}
}

// --- BUG-3: setDefault checks env slice, not os.Getenv ---

func TestSetDefaultUsesEnvSliceNotHostEnv(t *testing.T) {
	// If the key exists in the env slice, setDefault
	// should keep the slice value. It should NOT read
	// os.Getenv to decide.

	// Ensure the host has no CFLAGS set.
	t.Setenv("CFLAGS", "")

	// Pre-populate the env slice with a CFLAGS entry.
	env := []string{"CFLAGS=-Ofoo"}

	// setDefault should see CFLAGS already in the slice
	// and not append a duplicate.
	setDefault(&env, "CFLAGS", "-O2")

	// Count CFLAGS entries.
	count := 0
	for _, e := range env {
		if strings.HasPrefix(e, "CFLAGS=") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 CFLAGS entry, got %d: %v",
			count, env)
	}

	// The value should be the original, not the default.
	envMap := envToMap(env)
	if envMap["CFLAGS"] != "-Ofoo" {
		t.Errorf("CFLAGS = %q, want %q",
			envMap["CFLAGS"], "-Ofoo")
	}
}

func TestSetDefaultAppendsWhenKeyMissing(t *testing.T) {
	// When the key is absent from both the env slice and
	// host env, setDefault should append the default.
	t.Setenv("MYFLAG", "")

	var env []string
	setDefault(&env, "MYFLAG", "default-val")

	envMap := envToMap(env)
	if envMap["MYFLAG"] != "default-val" {
		t.Errorf("MYFLAG = %q, want %q",
			envMap["MYFLAG"], "default-val")
	}
}

// --- BUG-4: detectSourceRoot with stray file at root ---

func TestDetectSourceRootDescendsWithStrayFile(t *testing.T) {
	// A tarball root with one directory and a stray file
	// (e.g., .gitattributes) should still descend into
	// the single directory.
	srcDir := t.TempDir()
	subDir := filepath.Join(srcDir, "pkg-1.0")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Stray file at root level.
	stray := filepath.Join(srcDir, ".gitattributes")
	if err := os.WriteFile(stray, []byte("* text=auto\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := detectSourceRoot(srcDir)
	if err != nil {
		t.Fatalf("detectSourceRoot: %v", err)
	}

	if got != subDir {
		t.Errorf("detectSourceRoot = %q, want %q", got, subDir)
	}
}

// --- BUG-6: copyFile preserves source permissions ---

func TestCopyFilePreservesPermissions(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	src := filepath.Join(srcDir, "script.sh")
	dst := filepath.Join(dstDir, "script.sh")

	// Create source file with executable permissions.
	if err := os.WriteFile(src, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}

	srcInfo, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	dstInfo, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}

	if srcInfo.Mode() != dstInfo.Mode() {
		t.Errorf("dst mode = %v, want %v (same as src)",
			dstInfo.Mode(), srcInfo.Mode())
	}
}

// --- BUG-1: buildEnv cleanup removes tools dir ---

func TestBuildEnvCleanupRemovesToolsDir(t *testing.T) {
	env, cleanup, err := buildEnv(&BuildContext{PrefixDir: "/tmp/prefix", Jobs: "4", Version: "1.0.0", System: "", Debug: false, Deps: nil})
	if err != nil {
		t.Fatalf("buildEnv error: %v", err)
	}
	if cleanup == nil {
		t.Fatal("expected non-nil cleanup function")
	}

	// Extract toolsDir from PATH — it's the first entry.
	envMap := envToMap(env)
	pathVal := envMap["PATH"]
	toolsDir := strings.SplitN(pathVal, ":", 2)[0]

	// Verify the tools dir exists before cleanup.
	if _, err := os.Stat(toolsDir); err != nil {
		t.Fatalf("tools dir should exist before cleanup: %v", err)
	}

	cleanup()

	// After cleanup, the tools dir should be gone.
	if _, err := os.Stat(toolsDir); !os.IsNotExist(err) {
		t.Errorf("tools dir should be removed after cleanup, got err: %v", err)
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

// --- Prefix path rewriting in text files ---

func TestReplacePrefixInTextFiles(t *testing.T) {
	prefixDir := t.TempDir()

	// Create a script with hardcoded build prefix.
	binDir := filepath.Join(prefixDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(binDir, "autoconf")
	content := fmt.Sprintf(`#!/usr/bin/perl
my $pkgdatadir = '%s/share/autoconf';
my $autom4te = '%s/bin/autom4te';
`, prefixDir, prefixDir)
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a binary file that should NOT be modified.
	binFile := filepath.Join(binDir, "real-binary")
	binData := []byte{0x7f, 'E', 'L', 'F', 0, 0, 0, 0}
	if err := os.WriteFile(binFile, binData, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a share/ text file with the prefix.
	shareDir := filepath.Join(prefixDir, "share", "autoconf")
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dataFile := filepath.Join(shareDir, "autom4te.cfg")
	dataCfg := fmt.Sprintf("datadir = %s/share/autoconf\n", prefixDir)
	if err := os.WriteFile(dataFile, []byte(dataCfg), 0o644); err != nil {
		t.Fatal(err)
	}

	// Replace the build prefix with a placeholder.
	if err := ReplacePrefixInTextFiles(prefixDir, PrefixPlaceholder); err != nil {
		t.Fatalf("ReplacePrefixInTextFiles: %v", err)
	}

	// Script should have placeholder, not the original prefix.
	got, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), prefixDir) {
		t.Errorf("script still contains build prefix:\n%s", got)
	}
	if !strings.Contains(string(got), PrefixPlaceholder) {
		t.Errorf("script should contain placeholder:\n%s", got)
	}

	// Binary should be unchanged.
	gotBin, _ := os.ReadFile(binFile)
	if string(gotBin) != string(binData) {
		t.Error("binary file was modified")
	}

	// Share file should have placeholder.
	gotCfg, _ := os.ReadFile(dataFile)
	if strings.Contains(string(gotCfg), prefixDir) {
		t.Errorf("share file still contains build prefix:\n%s", gotCfg)
	}
	if !strings.Contains(string(gotCfg), PrefixPlaceholder) {
		t.Errorf("share file should contain placeholder:\n%s", gotCfg)
	}
}

func TestRestorePrefixPlaceholder(t *testing.T) {
	storeDir := t.TempDir()

	binDir := filepath.Join(storeDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	script := filepath.Join(binDir, "tool")
	content := fmt.Sprintf("#!/usr/bin/perl\nmy $dir = '%s/share';\n",
		PrefixPlaceholder)
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := RestorePrefixPlaceholder(storeDir); err != nil {
		t.Fatalf("RestorePrefixPlaceholder: %v", err)
	}

	got, _ := os.ReadFile(script)
	if strings.Contains(string(got), PrefixPlaceholder) {
		t.Errorf("script still contains placeholder:\n%s", got)
	}
	if !strings.Contains(string(got), storeDir) {
		t.Errorf("script should contain store dir %q:\n%s",
			storeDir, got)
	}
}

// --- BUG FIX 1: buildEnv returns error on MkdirTemp failure ---

func TestBuildEnvReturnsErrorOnTmpDirFailure(t *testing.T) {
	// Save original HOME for cleanup.
	oldHome := os.Getenv("HOME")
	defer os.Setenv("HOME", oldHome)

	// Create a temp dir structure with ~/.gale/tmp that is not writable.
	tmpBase := t.TempDir()
	fakeHome := filepath.Join(tmpBase, "home")
	galeDir := filepath.Join(fakeHome, ".gale")
	tmpDir := filepath.Join(galeDir, "tmp")

	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Make ~/.gale/tmp read-only so MkdirTemp inside it will fail.
	if err := os.Chmod(tmpDir, 0o444); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(tmpDir, 0o755) // restore for cleanup

	os.Setenv("HOME", fakeHome)

	env, cleanup, err := buildEnv(&BuildContext{PrefixDir: "/tmp/prefix", Jobs: "4", Version: "1.0.0", System: "", Debug: false, Deps: nil})
	if err == nil {
		t.Fatal("expected error when MkdirTemp fails")
	}
	if env != nil {
		t.Error("expected nil env on error")
	}
	if cleanup != nil {
		cleanup() // clean up if somehow it succeeded
	}
}

// --- BUG FIX 2: Build step error preserves error chain ---

func TestBuildStepErrorPreservesChain(t *testing.T) {
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

	// Check that the error chain is preserved.
	// The underlying error should be an *exec.ExitError.
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Errorf("error chain broken: expected *exec.ExitError in chain, got %T: %v",
			err, err)
	}
}

func TestBuildEnvIncludesRecipeEnvVars(t *testing.T) {
	bc := &BuildContext{
		PrefixDir: "/tmp/prefix",
		SourceDir: t.TempDir(),
		Jobs:      "4",
		Version:   "1.0.0",
		Env:       map[string]string{"MY_VAR": "hello"},
	}
	env, cleanup, err := buildEnv(bc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer cleanup()

	found := false
	for _, e := range env {
		if e == "MY_VAR=hello" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected MY_VAR=hello in build env")
	}
}

func TestBuildEnvExpandsPrefixInRecipeEnvVars(t *testing.T) {
	bc := &BuildContext{
		PrefixDir: "/tmp/test-prefix",
		SourceDir: t.TempDir(),
		Jobs:      "4",
		Version:   "2.0.0",
		Env:       map[string]string{"RUNTIME": "${PREFIX}/lib/runtime"},
	}
	env, cleanup, err := buildEnv(bc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer cleanup()

	want := "RUNTIME=/tmp/test-prefix/lib/runtime"
	found := false
	for _, e := range env {
		if e == want {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected %q in build env", want)
	}
}
