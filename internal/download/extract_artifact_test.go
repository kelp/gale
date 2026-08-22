package download

import (
	"archive/tar"
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestExtractArtifactExtractsRegularTree(t *testing.T) {
	umaskMu.Lock()
	defer umaskMu.Unlock()
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)

	archive := filepath.Join(t.TempDir(), "ok.tar.gz")
	writeTarGzHeaders(t, archive, []tar.Header{{
		Name: "bin/jq",
		Mode: 0o755,
	}, {
		Name: "README",
		Mode: 0o644,
	}}, []string{"hello\n", "hi\n"})

	dest := t.TempDir()
	if err := ExtractArtifact(context.Background(), archive, dest, "tar.gz"); err != nil {
		t.Fatalf("ExtractArtifact: %v", err)
	}
	if filePerm(t, filepath.Join(dest, "bin", "jq")).Perm() != 0o755 {
		t.Errorf("jq perm = %o, want 0755", filePerm(t, filepath.Join(dest, "bin", "jq")).Perm())
	}
	if filePerm(t, filepath.Join(dest, "README")).Perm() != 0o644 {
		t.Errorf("README perm = %o, want 0644", filePerm(t, filepath.Join(dest, "README")).Perm())
	}
}

func TestExtractArtifactRefusesLinks(t *testing.T) {
	cases := []struct {
		name string
		hdr  tar.Header
		kind string
	}{
		{
			name: "symlink",
			hdr:  tar.Header{Typeflag: tar.TypeSymlink, Name: "link", Linkname: "target"},
			kind: "symlink",
		},
		{
			name: "hardlink",
			hdr:  tar.Header{Typeflag: tar.TypeLink, Name: "linked.txt", Linkname: "original.txt"},
			kind: "hardlink",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), tc.name+".tar.gz")
			headers := []tar.Header{{
				Name: "original.txt",
				Mode: 0o644,
			}, tc.hdr}
			writeTarGzHeaders(t, archive, headers, []string{"body", ""})

			err := ExtractArtifact(context.Background(), archive, t.TempDir(), "tar.gz")
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.kind) {
				t.Errorf("error = %q, want it to name %s", err, tc.kind)
			}
		})
	}
}

func TestExtractArtifactRefusesGaleSidecars(t *testing.T) {
	names := []string{
		".gale-provenance.toml",
		".gale-deps.toml",
		"bin/.gale-foo.toml",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "side.tar.gz")
			writeTarGzHeaders(t, archive, []tar.Header{{
				Name: name,
				Mode: 0o644,
			}}, []string{"sidecar\n"})
			err := ExtractArtifact(context.Background(), archive, t.TempDir(), "tar.gz")
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), "gale sidecar") {
				t.Errorf("error = %q, want gale sidecar", err)
			}
		})
	}
}

func TestExtractArtifactZipRefusesSidecar(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "side.zip")
	createZip(t, archive, map[string]string{".gale-deps.toml": "x"})
	err := ExtractArtifact(context.Background(), archive, t.TempDir(), "zip")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "gale sidecar") {
		t.Errorf("error = %q, want gale sidecar", err)
	}
}

func TestExtractArtifactRejectsUnknownFormat(t *testing.T) {
	err := ExtractArtifact(context.Background(), "x.tar.zst", t.TempDir(), "tar.zst")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestExtractArtifactBuildInputStillAllowsSidecar(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "deps.tar.gz")
	writeTarGzHeaders(t, archive, []tar.Header{{
		Name: ".gale-deps.toml",
		Mode: 0o644,
	}}, []string{"kept\n"})
	dest := t.TempDir()
	if err := ExtractTarGz(context.Background(), archive, dest); err != nil {
		t.Fatalf("ExtractTarGz: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, ".gale-deps.toml"))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if string(got) != "kept\n" {
		t.Errorf("sidecar = %q, want kept", got)
	}
}
