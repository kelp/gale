package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ulikunitz/xz"

	"github.com/kelp/gale/internal/admit"
	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/fetch"
	"github.com/kelp/gale/internal/index"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/store"
)

const (
	admitJustURL = "https://github.com/casey/just/releases/download/1.56.0/just.tar.gz"
	admitBinURL  = "https://github.com/casey/just/releases/download/1.56.0/just"
)

func TestAdmitIsRegistered(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"admit"})
	if err != nil {
		t.Fatalf("find admit: %v", err)
	}
	if cmd.Name() != "admit" {
		t.Fatalf("command name = %q, want admit", cmd.Name())
	}
}

func TestAdmitTarGzEmitsLintCleanFragment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	fx := newAdmitFix(t)
	out, err := executeAdmit(
		t,
		"--archive", fx.archive,
		"--name", "just",
		"--version", "1.56.0",
		"--os", "linux",
		"--arch", runtime.GOARCH,
		"--url", admitJustURL,
		"--file", "just:bin/just:755",
	)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if strings.Contains(out, "attestation") {
		t.Fatalf("fragment has attestation key:\n%s", out)
	}
	if !strings.Contains(out, `hash_source = "computed"`) {
		t.Fatalf("fragment missing hash_source:\n%s", out)
	}
	if !strings.Contains(out, `format = "tar.gz"`) {
		t.Fatalf("fragment missing format:\n%s", out)
	}
	if !strings.Contains(out, `src = "just"`) ||
		!strings.Contains(out, `dest = "bin/just"`) ||
		!strings.Contains(out, "mode = 0o755") {
		t.Fatalf("fragment missing files:\n%s", out)
	}
	art := parseAdmitFragment(t, out)
	if art.Attestation != nil {
		t.Fatalf("attestation = %#v, want nil", art.Attestation)
	}
	if got, err := download.HashFile(context.Background(), fx.archive); err != nil || art.SHA256 != got {
		t.Fatalf("sha256 = %q err=%v, want %q", art.SHA256, err, got)
	}
	if art.TreeDigest != fx.treeDigest() {
		t.Fatalf("tree_digest = %q, want %q", art.TreeDigest, fx.treeDigest())
	}
	assertAdmitHomeClean(t, home)
}

func TestAdmitDifferentBodiesDifferentDigest(t *testing.T) {
	a := newAdmitFix(t)
	b := newAdmitFix(t)
	if err := os.WriteFile(b.elf, append(b.elfBytes(), 'x'), 0o755); err != nil {
		t.Fatal(err)
	}
	b.rebuildTar()
	outA, err := executeAdmit(t, a.args()...)
	if err != nil {
		t.Fatalf("admit a: %v", err)
	}
	outB, err := executeAdmit(t, b.args()...)
	if err != nil {
		t.Fatalf("admit b: %v", err)
	}
	artA := parseAdmitFragment(t, outA)
	artB := parseAdmitFragment(t, outB)
	if artA.TreeDigest == artB.TreeDigest {
		t.Fatalf("tree_digest collided: %s", artA.TreeDigest)
	}
}

func TestAdmitSymlinkFailsClosed(t *testing.T) {
	fx := newAdmitFix(t)
	archive := filepath.Join(t.TempDir(), "link.tar.gz")
	writeAdmitTarGz(t, archive, []tar.Header{
		{Name: "just", Typeflag: tar.TypeSymlink, Linkname: "other"},
	}, nil)
	out, err := executeAdmit(t, fx.withArchive(archive)...)
	if err == nil {
		t.Fatal("admit succeeded, want symlink error")
	}
	if strings.Contains(out, "tree_digest") {
		t.Fatalf("printed tree_digest on failure:\n%s", out)
	}
}

func TestAdmitHardlinkFailsClosed(t *testing.T) {
	fx := newAdmitFix(t)
	archive := filepath.Join(t.TempDir(), "link.tar.gz")
	writeAdmitTarGz(t, archive, []tar.Header{
		{Name: "just", Mode: 0o755, Typeflag: tar.TypeReg},
		{Name: "also", Typeflag: tar.TypeLink, Linkname: "just"},
	}, [][]byte{fx.elfBytes(), nil})
	out, err := executeAdmit(t, fx.withArchive(archive)...)
	if err == nil {
		t.Fatal("admit succeeded, want hardlink error")
	}
	if strings.Contains(out, "tree_digest") {
		t.Fatalf("printed tree_digest on failure:\n%s", out)
	}
}

func TestAdmitArchMismatchFailsClosed(t *testing.T) {
	if runtime.GOARCH == "arm64" {
		t.Skip("need a non-arm64 ELF")
	}
	fx := newAdmitFix(t)
	_, err := executeAdmit(t, fx.withArch("arm64")...)
	if err == nil {
		t.Fatal("admit succeeded, want arch mismatch")
	}
}

func TestAdmitInjectedLddNonSystemFails(t *testing.T) {
	fx := newAdmitFix(t)
	setAdmitInspector(t, stubAdmitInspector{
		libs: []string{"/lib64/ld-linux-x86-64.so.2", "/opt/foo/libbar.so"},
	})
	_, err := executeAdmit(t, fx.args()...)
	if err == nil {
		t.Fatal("admit succeeded, want non-system lib")
	}
}

func TestAdmitInjectedLddSystemOnlySucceeds(t *testing.T) {
	fx := newAdmitFix(t)
	setAdmitInspector(t, stubAdmitInspector{
		libs: []string{
			"linux-vdso.so.1",
			"/lib64/ld-linux-x86-64.so.2",
			"/lib/x86_64-linux-gnu/libc.so.6",
		},
	})
	if _, err := executeAdmit(t, fx.args()...); err != nil {
		t.Fatalf("admit: %v", err)
	}
}

func TestAdmitInjectedOtoolHomebrewFails(t *testing.T) {
	fx := newAdmitMachO(t)
	setAdmitInspector(t, stubAdmitInspector{
		libs: []string{"/usr/lib/libSystem.B.dylib", "/opt/homebrew/lib/foo.dylib"},
	})
	_, err := executeAdmit(t, fx.darwinArgs()...)
	if err == nil {
		t.Fatal("admit succeeded, want non-system dylib")
	}
}

func TestAdmitInjectedOtoolSystemOnlySucceeds(t *testing.T) {
	fx := newAdmitMachO(t)
	setAdmitInspector(t, stubAdmitInspector{
		libs: []string{
			"/usr/lib/libSystem.B.dylib",
			"/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation",
		},
	})
	if _, err := executeAdmit(t, fx.darwinArgs()...); err != nil {
		t.Fatalf("admit: %v", err)
	}
}

func TestAdmitUnknownFormatFailsBeforeExtract(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "just.rar")
	if err := os.WriteFile(archive, []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := executeAdmit(
		t,
		"--archive", archive,
		"--name", "just",
		"--version", "1.56.0",
		"--os", "linux",
		"--arch", "amd64",
		"--url", admitJustURL,
		"--file", "just:bin/just:755",
		"--format", "rar",
	)
	if err == nil {
		t.Fatal("admit succeeded, want unknown format")
	}
}

func TestAdmitBinaryStripFailsBeforeExtract(t *testing.T) {
	fx := newAdmitFix(t)
	_, err := executeAdmit(
		t,
		"--archive", fx.elf,
		"--name", "just",
		"--version", "1.56.0",
		"--os", "linux",
		"--arch", runtime.GOARCH,
		"--url", admitBinURL,
		"--file", "just:bin/just:755",
		"--format", "binary",
		"--strip", "1",
	)
	if err == nil {
		t.Fatal("admit succeeded, want binary strip error")
	}
}

func TestAdmitMissingFileFailsClosed(t *testing.T) {
	fx := newAdmitFix(t)
	_, err := executeAdmit(
		t,
		"--archive", fx.archive,
		"--name", "just",
		"--version", "1.56.0",
		"--os", "linux",
		"--arch", runtime.GOARCH,
		"--url", admitJustURL,
	)
	if err == nil {
		t.Fatal("admit succeeded, want missing --file")
	}
}

func TestAdmitDestEscapeFailsBeforeExtract(t *testing.T) {
	fx := newAdmitFix(t)
	_, err := executeAdmit(t, append(fx.args()[:len(fx.args())-2],
		"--file", "just:../x:755")...)
	if err == nil {
		t.Fatal("admit succeeded, want dest escape")
	}
}

func TestAdmitUpstreamHashSourceRequiresSHA(t *testing.T) {
	fx := newAdmitFix(t)
	_, err := executeAdmit(t, append(fx.args(),
		"--hash-source", "upstream-sha256sums")...)
	if err == nil {
		t.Fatal("admit succeeded, want missing --sha256")
	}
}

func TestAdmitStaleSiblingExtractDoesNotChangeDigest(t *testing.T) {
	fx := newAdmitFix(t)
	stale := filepath.Join(filepath.Dir(fx.archive), "extract")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	poison := []byte("poison-bytes-not-the-elf")
	if err := os.WriteFile(filepath.Join(stale, "just"), poison, 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := executeAdmit(t, fx.args()...)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	art := parseAdmitFragment(t, out)
	if art.TreeDigest != fx.treeDigest() {
		t.Fatalf("tree_digest = %q, want %q (stale extract leaked)",
			art.TreeDigest, fx.treeDigest())
	}
}

func TestAdmitNoBinaryFailsClosed(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "just.tar.gz")
	writeAdmitTarGz(t, archive, []tar.Header{
		{Name: "just", Mode: 0o755, Typeflag: tar.TypeReg},
	}, [][]byte{[]byte("#!/bin/sh\necho hi\n")})
	_, err := executeAdmit(
		t,
		"--archive", archive,
		"--name", "just",
		"--version", "1.56.0",
		"--os", "linux",
		"--arch", "amd64",
		"--url", admitJustURL,
		"--file", "just:bin/just:755",
	)
	if err == nil {
		t.Fatal("admit succeeded, want no inspectable binary")
	}
}

func TestAdmitLinuxTreeOfMachOFailsClosed(t *testing.T) {
	fx := newAdmitMachO(t)
	_, err := executeAdmit(t, fx.args()...)
	if err == nil {
		t.Fatal("admit succeeded, want ELF on linux")
	}
}

func TestAdmitUnsignedMachOFailsClosed(t *testing.T) {
	fx := newAdmitMachO(t)
	setAdmitInspector(t, stubAdmitInspector{
		signErr: errAdmitUnsigned,
		libs:    []string{"/usr/lib/libSystem.B.dylib"},
	})
	_, err := executeAdmit(t, fx.darwinArgs()...)
	if err == nil {
		t.Fatal("admit succeeded, want unsigned")
	}
}

func TestAdmitBinarySrcMustBeURLBasename(t *testing.T) {
	fx := newAdmitFix(t)
	_, err := executeAdmit(
		t,
		"--archive", fx.elf,
		"--name", "just",
		"--version", "1.56.0",
		"--os", "linux",
		"--arch", runtime.GOARCH,
		"--url", admitBinURL,
		"--file", "wrong:bin/just:755",
		"--format", "binary",
	)
	if err == nil {
		t.Fatal("admit succeeded, want src basename mismatch")
	}
}

func TestAdmitZipAndTarXzEmitFragment(t *testing.T) {
	elf := hostELF(t)
	t.Run("zip", func(t *testing.T) {
		archive := filepath.Join(t.TempDir(), "just.zip")
		writeAdmitZip(t, archive, map[string][]byte{"just": elf})
		out, err := executeAdmit(
			t,
			"--archive", archive,
			"--name", "just",
			"--version", "1.56.0",
			"--os", "linux",
			"--arch", runtime.GOARCH,
			"--url", "https://github.com/casey/just/releases/download/1.56.0/just.zip",
			"--file", "just:bin/just:755",
		)
		if err != nil {
			t.Fatalf("admit: %v", err)
		}
		parseAdmitFragment(t, out)
	})
	t.Run("tar.xz", func(t *testing.T) {
		archive := filepath.Join(t.TempDir(), "just.tar.xz")
		writeAdmitTarXz(t, archive, []tar.Header{
			{Name: "just", Mode: 0o755, Typeflag: tar.TypeReg},
		}, [][]byte{elf})
		out, err := executeAdmit(
			t,
			"--archive", archive,
			"--name", "just",
			"--version", "1.56.0",
			"--os", "linux",
			"--arch", runtime.GOARCH,
			"--url", "https://github.com/casey/just/releases/download/1.56.0/just.tar.xz",
			"--file", "just:bin/just:755",
		)
		if err != nil {
			t.Fatalf("admit: %v", err)
		}
		parseAdmitFragment(t, out)
	})
}

func TestAdmitUpstreamHashMismatch(t *testing.T) {
	fx := newAdmitFix(t)
	_, err := executeAdmit(t, append(
		fx.args(),
		"--hash-source", "upstream-sha256sums",
		"--sha256", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	)...)
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("error = %v, want sha256 mismatch", err)
	}
}

func TestAdmitUpstreamHashMatch(t *testing.T) {
	fx := newAdmitFix(t)
	sum, err := download.HashFile(context.Background(), fx.archive)
	if err != nil {
		t.Fatal(err)
	}
	out, err := executeAdmit(t, append(
		fx.args(),
		"--hash-source", "upstream-sha256sums",
		"--sha256", sum,
	)...)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	art := parseAdmitFragment(t, out)
	if art.HashSource != "upstream-sha256sums" {
		t.Fatalf("hash_source = %q", art.HashSource)
	}
}

func executeAdmit(t *testing.T, argv ...string) (string, error) {
	t.Helper()
	resetAdmitFlags(t)
	oldArgs := rootCmd.Flags().Args()
	oldOut := rootCmd.OutOrStdout()
	oldErr := rootCmd.ErrOrStderr()
	t.Cleanup(func() {
		rootCmd.SetArgs(oldArgs)
		rootCmd.SetOut(oldOut)
		rootCmd.SetErr(oldErr)
		resetAdmitFlags(t)
	})
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs(append([]string{"admit"}, argv...))
	err := executeRoot()
	return stdout.String(), err
}

func resetAdmitFlags(t *testing.T) {
	t.Helper()
	f := admitCmd.Flags()
	for k, v := range map[string]string{
		"archive":     "",
		"name":        "",
		"version":     "",
		"os":          "",
		"arch":        "",
		"url":         "",
		"format":      "",
		"hash-source": "computed",
		"sha256":      "",
		"strip":       "0",
	} {
		if err := f.Set(k, v); err != nil {
			t.Fatalf("reset %s: %v", k, err)
		}
	}
	repl, ok := f.Lookup("file").Value.(interface{ Replace([]string) error })
	if !ok {
		t.Fatal("file flag does not implement Replace")
	}
	if err := repl.Replace(nil); err != nil {
		t.Fatal(err)
	}
}

type stubAdmitInspector struct {
	libs    []string
	libsErr error
	signErr error
}

func (s stubAdmitInspector) CodeSign(string) error { return s.signErr }

func (s stubAdmitInspector) DynamicLibs(string) ([]string, error) {
	return s.libs, s.libsErr
}

func setAdmitInspector(t *testing.T, insp admit.Inspector) {
	t.Helper()
	prev := admitInspector
	admitInspector = insp
	t.Cleanup(func() { admitInspector = prev })
}

type admitFix struct {
	t       *testing.T
	elf     string
	archive string
}

func newAdmitFix(t *testing.T) *admitFix {
	t.Helper()
	dir := t.TempDir()
	elf := filepath.Join(dir, "just")
	if err := os.WriteFile(elf, hostELF(t), 0o755); err != nil {
		t.Fatal(err)
	}
	fx := &admitFix{t: t, elf: elf, archive: filepath.Join(dir, "just.tar.gz")}
	fx.rebuildTar()
	setAdmitInspector(t, stubAdmitInspector{
		libs: []string{"linux-vdso.so.1", "/lib64/ld-linux-x86-64.so.2", "libc.so.6"},
	})
	return fx
}

func newAdmitMachO(t *testing.T) *admitFix {
	t.Helper()
	dir := t.TempDir()
	elf := filepath.Join(dir, "just")
	if err := os.WriteFile(elf, admit.MachOARM64Stub(), 0o755); err != nil {
		t.Fatal(err)
	}
	fx := &admitFix{t: t, elf: elf, archive: filepath.Join(dir, "just.tar.gz")}
	fx.rebuildTar()
	return fx
}

func (fx *admitFix) elfBytes() []byte {
	fx.t.Helper()
	b, err := os.ReadFile(fx.elf)
	if err != nil {
		fx.t.Fatal(err)
	}
	return b
}

func (fx *admitFix) rebuildTar() {
	fx.t.Helper()
	writeAdmitTarGz(fx.t, fx.archive, []tar.Header{
		{Name: "just", Mode: 0o755, Typeflag: tar.TypeReg},
	}, [][]byte{fx.elfBytes()})
}

func (fx *admitFix) args() []string {
	return []string{
		"--archive", fx.archive,
		"--name", "just",
		"--version", "1.56.0",
		"--os", "linux",
		"--arch", runtime.GOARCH,
		"--url", admitJustURL,
		"--file", "just:bin/just:755",
	}
}

func (fx *admitFix) darwinArgs() []string {
	return []string{
		"--archive", fx.archive,
		"--name", "just",
		"--version", "1.56.0",
		"--os", "darwin",
		"--arch", "arm64",
		"--url", admitJustURL,
		"--file", "just:bin/just:755",
	}
}

func (fx *admitFix) withArchive(archive string) []string {
	a := fx.args()
	a[1] = archive
	return a
}

func (fx *admitFix) withArch(arch string) []string {
	a := fx.args()
	for i, s := range a {
		if s == "--arch" {
			a[i+1] = arch
			break
		}
	}
	return a
}

func (fx *admitFix) treeDigest() string {
	fx.t.Helper()
	work := fx.t.TempDir()
	archive := filepath.Join(work, "archive")
	if err := copyAdmitFile(fx.archive, archive); err != nil {
		fx.t.Fatal(err)
	}
	tree := filepath.Join(work, "tree")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		fx.t.Fatal(err)
	}
	art := index.Artifact{
		URL:    admitJustURL,
		Format: "tar.gz",
		Files: []index.FileEntry{{
			Src: "just", Dest: "bin/just", Mode: 0o755,
		}},
	}
	if err := fetch.PlaceMapped(context.Background(), archive, tree, art); err != nil {
		fx.t.Fatal(err)
	}
	d, err := provenance.DigestTree(context.Background(), tree)
	if err != nil {
		fx.t.Fatal(err)
	}
	return d
}

func parseAdmitFragment(t *testing.T, frag string) index.Artifact {
	t.Helper()
	const ver = "1.56.0"
	plat := "linux/" + runtime.GOARCH
	doc := "[package]\nname = \"just\"\n" +
		"description = \"x\"\nlicense = \"MIT\"\n" +
		"homepage = \"https://github.com/casey/just\"\n" +
		"repo = \"casey/just\"\nlatest = \"" + ver + "\"\n\n" + frag
	f, err := index.Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v\n%s", err, doc)
	}
	if issues := index.Lint(f); len(issues) > 0 {
		t.Fatalf("Lint: %v\n%s", issues, doc)
	}
	art, ok := f.Versions[ver].Artifacts[plat]
	if !ok {
		t.Fatalf("missing artifact %s %s\n%s", ver, plat, frag)
	}
	return art
}

func assertAdmitHomeClean(t *testing.T, home string) {
	t.Helper()
	galeDir := filepath.Join(home, ".gale")
	for _, p := range []string{
		filepath.Join(galeDir, "pkg", store.FetchNamespace),
		filepath.Join(galeDir, "locks"),
		filepath.Join(galeDir, "current"),
	} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s exists: %v", p, err)
		}
	}
}

func hostELF(t *testing.T) []byte {
	t.Helper()
	return admit.ELFStub(runtime.GOARCH)
}

func writeAdmitTarGz(t *testing.T, path string, headers []tar.Header, bodies [][]byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	writeAdmitTar(t, tw, headers, bodies)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeAdmitTarXz(t *testing.T, path string, headers []tar.Header, bodies [][]byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	xw, err := xz.NewWriter(f)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(xw)
	writeAdmitTar(t, tw, headers, bodies)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := xw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeAdmitTar(t *testing.T, tw *tar.Writer, headers []tar.Header, bodies [][]byte) {
	t.Helper()
	for i, h := range headers {
		hdr := h
		if i < len(bodies) && bodies[i] != nil {
			hdr.Size = int64(len(bodies[i]))
		}
		if err := tw.WriteHeader(&hdr); err != nil {
			t.Fatal(err)
		}
		if i < len(bodies) && bodies[i] != nil {
			if _, err := tw.Write(bodies[i]); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func writeAdmitZip(t *testing.T, path string, files map[string][]byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func copyAdmitFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dest)
		return err
	}
	return out.Close()
}

var errAdmitUnsigned = errors.New("unsigned")
