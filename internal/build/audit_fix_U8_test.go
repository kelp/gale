// Tests for audit unit U8 (build fixups). One test section per
// issue:
//
//	#51 — Linux rpath fixups must visit ELF files under
//	      libexec/ and sbin/, not just bin/ and lib/.
//	#56 — fixupShebangs must preserve interpreter arguments.
//	#57 — text-fixup passes must scan include/.
//	#55 — a corrupt source-cache entry must be evicted and the
//	      source re-downloaded, not become a permanent failure.
package build

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/recipe"
)

// --- Issue #51: rpath fixups skip libexec/ and sbin/ ---

// writeFakeELF writes a file that passes isELF (correct magic)
// at path, creating parent directories.
func writeFakeELF(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data := append([]byte{0x7f, 'E', 'L', 'F'}, make([]byte, 60)...)
	if err := os.WriteFile(path, data, 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
}

// installFakePatchelf puts a logging patchelf shim first on
// PATH. Every invocation is appended to the returned log file
// (one line per call, args space-joined). --print-rpath prints
// printRpath so RelocateStaleRpaths has something to rewrite.
func installFakePatchelf(t *testing.T, printRpath string) string {
	t.Helper()
	dir := t.TempDir()
	logFile := filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + logFile + "\n" +
		"if [ \"$1\" = \"--print-rpath\" ]; then\n" +
		"  printf '%s\\n' '" + printRpath + "'\n" +
		"fi\n" +
		"exit 0\n"
	shim := filepath.Join(dir, "patchelf")
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logFile
}

// readPatchelfLog returns the shim log contents ("" if the
// shim was never invoked).
func readPatchelfLog(t *testing.T, logFile string) string {
	t.Helper()
	data, err := os.ReadFile(logFile)
	if err != nil {
		return ""
	}
	return string(data)
}

// elfFixupPrefix builds a prefix tree with ELF files in bin/,
// lib/, libexec/git-core/, and sbin/ and returns the prefix
// plus the libexec and sbin file paths.
func elfFixupPrefix(t *testing.T) (prefix, libexecELF, sbinELF string) {
	t.Helper()
	prefix = t.TempDir()
	writeFakeELF(t, filepath.Join(prefix, "bin", "tool"))
	writeFakeELF(t, filepath.Join(prefix, "lib", "libx.so"))
	libexecELF = filepath.Join(prefix, "libexec", "git-core", "git-remote-http")
	writeFakeELF(t, libexecELF)
	sbinELF = filepath.Join(prefix, "sbin", "daemon")
	writeFakeELF(t, sbinELF)
	return prefix, libexecELF, sbinELF
}

func TestFixupBinariesVisitsLibexecAndSbin(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux rpath fixups only")
	}
	logFile := installFakePatchelf(t, "")
	prefix, libexecELF, sbinELF := elfFixupPrefix(t)

	if err := FixupBinaries(prefix); err != nil {
		t.Fatalf("FixupBinaries: %v", err)
	}

	log := readPatchelfLog(t, logFile)
	if !strings.Contains(log, libexecELF) {
		t.Errorf("libexec ELF %s never patched; patchelf calls:\n%s",
			libexecELF, log)
	}
	if !strings.Contains(log, sbinELF) {
		t.Errorf("sbin ELF %s never patched; patchelf calls:\n%s",
			sbinELF, log)
	}
	// The own-lib rpath must point at the package's lib/ from
	// the file's actual depth, not assume bin/-level placement.
	if !strings.Contains(log, "$ORIGIN/../../lib "+libexecELF) {
		t.Errorf("libexec ELF should get depth-aware own-lib "+
			"rpath $ORIGIN/../../lib; patchelf calls:\n%s", log)
	}
}

func TestAddDepRpathsVisitsLibexecAndSbin(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux rpath fixups only")
	}
	logFile := installFakePatchelf(t, "")
	prefix, libexecELF, sbinELF := elfFixupPrefix(t)

	// Dep store dir with a lib/ so AddDepRpaths proceeds.
	depDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(depDir, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := AddDepRpaths(prefix, []string{depDir}); err != nil {
		t.Fatalf("AddDepRpaths: %v", err)
	}

	log := readPatchelfLog(t, logFile)
	if !strings.Contains(log, libexecELF) {
		t.Errorf("libexec ELF %s got no farm rpath; patchelf calls:\n%s",
			libexecELF, log)
	}
	if !strings.Contains(log, sbinELF) {
		t.Errorf("sbin ELF %s got no farm rpath; patchelf calls:\n%s",
			sbinELF, log)
	}
	// Both rpath components must match the file's depth:
	// own lib at ../../lib, farm 3 more levels up.
	want := "$ORIGIN/../../lib:$ORIGIN/../../../../../lib " + libexecELF
	if !strings.Contains(log, want) {
		t.Errorf("libexec ELF rpath should be depth-aware (%s); "+
			"patchelf calls:\n%s", want, log)
	}
}

func TestRelocateStaleRpathsVisitsLibexecAndSbin(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux rpath fixups only")
	}
	logFile := installFakePatchelf(t,
		"/Users/runner/.gale/pkg/openssl/3.3.0-1/lib")
	prefix, libexecELF, sbinELF := elfFixupPrefix(t)

	storeRoot := "/home/test/.gale/pkg"
	if err := RelocateStaleRpaths(prefix, storeRoot); err != nil {
		t.Fatalf("RelocateStaleRpaths: %v", err)
	}

	log := readPatchelfLog(t, logFile)
	wantLibexec := "--set-rpath " + storeRoot +
		"/openssl/3.3.0-1/lib " + libexecELF
	if !strings.Contains(log, wantLibexec) {
		t.Errorf("stale rpath in libexec ELF not relocated; "+
			"patchelf calls:\n%s", log)
	}
	wantSbin := "--set-rpath " + storeRoot +
		"/openssl/3.3.0-1/lib " + sbinELF
	if !strings.Contains(log, wantSbin) {
		t.Errorf("stale rpath in sbin ELF not relocated; "+
			"patchelf calls:\n%s", log)
	}
}

// --- Issue #56: fixupShebangs drops interpreter arguments ---

func TestFixupShebangsPreservesInterpreterArgs(t *testing.T) {
	prefixDir := t.TempDir()
	binDir := filepath.Join(prefixDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	script := filepath.Join(binDir, "tool.pl")
	content := "#!" + prefixDir + "/bin/perl -w\nprint \"hi\";\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil { //nolint:gosec
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
	// "#!/usr/bin/env perl -w" is broken on Linux: the kernel
	// passes "perl -w" to env as one argument. env -S splits it.
	want := "#!/usr/bin/env -S perl -w"
	if got[0] != want {
		t.Errorf("shebang = %q, want %q", got[0], want)
	}
	if got[1] != "print \"hi\";\n" {
		t.Errorf("body = %q, want %q", got[1], "print \"hi\";\n")
	}
}

func TestFixupShebangsNoArgsStaysPlainEnvForm(t *testing.T) {
	prefixDir := t.TempDir()
	binDir := filepath.Join(prefixDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	script := filepath.Join(binDir, "tool.py")
	content := "#!" + prefixDir + "/bin/python3\nimport sys\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil { //nolint:gosec
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
	if got[0] != "#!/usr/bin/env python3" {
		t.Errorf("shebang = %q, want %q",
			got[0], "#!/usr/bin/env python3")
	}
}

// --- Issue #57: text fixups skip include/ ---

func TestReplacePrefixInTextFilesScansInclude(t *testing.T) {
	prefixDir := t.TempDir()
	incDir := filepath.Join(prefixDir, "include", "node")
	if err := os.MkdirAll(incDir, 0o755); err != nil {
		t.Fatal(err)
	}
	header := filepath.Join(incDir, "config.gypi")
	content := "\"node_prefix\": \"" + prefixDir + "\"\n"
	if err := os.WriteFile(header, []byte(content), 0o644); err != nil { //nolint:gosec
		t.Fatal(err)
	}

	if err := ReplacePrefixInTextFiles(prefixDir, PrefixPlaceholder); err != nil {
		t.Fatalf("ReplacePrefixInTextFiles: %v", err)
	}

	data, err := os.ReadFile(header)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), prefixDir) {
		t.Errorf("header under include/ still embeds the build "+
			"prefix: %q", string(data))
	}
	if !strings.Contains(string(data), PrefixPlaceholder) {
		t.Errorf("header under include/ missing placeholder: %q",
			string(data))
	}
}

func TestRestorePrefixPlaceholderScansInclude(t *testing.T) {
	storeDir := t.TempDir()
	incDir := filepath.Join(storeDir, "include")
	if err := os.MkdirAll(incDir, 0o755); err != nil {
		t.Fatal(err)
	}
	header := filepath.Join(incDir, "config.h")
	content := "#define PREFIX \"" + PrefixPlaceholder + "\"\n"
	if err := os.WriteFile(header, []byte(content), 0o644); err != nil { //nolint:gosec
		t.Fatal(err)
	}

	if err := RestorePrefixPlaceholder(storeDir); err != nil {
		t.Fatalf("RestorePrefixPlaceholder: %v", err)
	}

	data, err := os.ReadFile(header)
	if err != nil {
		t.Fatal(err)
	}
	want := "#define PREFIX \"" + storeDir + "\"\n"
	if string(data) != want {
		t.Errorf("header = %q, want %q", string(data), want)
	}
}

func TestRelocateStalePathsInTextFilesScansInclude(t *testing.T) {
	prefixDir := t.TempDir()
	incDir := filepath.Join(prefixDir, "include", "node")
	if err := os.MkdirAll(incDir, 0o755); err != nil {
		t.Fatal(err)
	}
	header := filepath.Join(incDir, "config.gypi")
	stale := "/Users/runner/.gale/pkg/nodejs/26.1.0-2"
	content := "\"node_prefix\": \"" + stale + "\"\n"
	if err := os.WriteFile(header, []byte(content), 0o644); err != nil { //nolint:gosec
		t.Fatal(err)
	}

	if err := RelocateStalePathsInTextFiles(prefixDir, "/local/.gale/pkg"); err != nil {
		t.Fatalf("RelocateStalePathsInTextFiles: %v", err)
	}

	data, err := os.ReadFile(header)
	if err != nil {
		t.Fatal(err)
	}
	want := "\"node_prefix\": \"/local/.gale/pkg/nodejs/26.1.0-2\"\n"
	if string(data) != want {
		t.Errorf("header = %q, want %q", string(data), want)
	}
}

// --- Issue #55: corrupt source-cache entry never evicted ---

func TestBuildEvictsCorruptSourceCacheEntry(t *testing.T) {
	// Isolate ~/.gale (source cache + tmp) in a temp home.
	t.Setenv("HOME", t.TempDir())

	tarball, hash := createSourceTarGz(t, map[string]string{
		"testpkg-1.0/README": "hello",
	})
	data, err := os.ReadFile(tarball)
	if err != nil {
		t.Fatal(err)
	}

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			hits++
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write(data) //nolint:errcheck
		},
	))
	t.Cleanup(srv.Close)

	// Seed a corrupt (truncated) cache entry under the
	// correct hash-keyed name, as an interrupted copy would.
	cacheDir := sourceCache()
	if cacheDir == "" {
		t.Fatal("sourceCache returned empty dir")
	}
	cachedFile := filepath.Join(cacheDir, hash)
	if err := os.WriteFile(cachedFile, []byte("truncated junk"), 0o644); err != nil { //nolint:gosec
		t.Fatal(err)
	}

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "1.0"},
		Source: recipe.Source{
			URL:    srv.URL + "/testpkg-1.0.tar.gz",
			SHA256: hash,
		},
		Build: recipe.Build{
			Steps: []string{"mkdir -p $PREFIX/bin"},
		},
	}

	// Pre-fix this fails permanently with "verify source:
	// sha256 mismatch" without ever contacting the server.
	if _, err := Build(r, t.TempDir(), false, nil); err != nil {
		t.Fatalf("Build with corrupt cache entry: %v", err)
	}
	if hits == 0 {
		t.Error("corrupt cache entry not evicted: upstream " +
			"was never contacted")
	}

	// The cache must hold a verified copy afterwards.
	got, err := os.ReadFile(cachedFile)
	if err != nil {
		t.Fatalf("read cache entry after build: %v", err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(got)) != hash {
		t.Error("cache entry still corrupt after build")
	}
}
