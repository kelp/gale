package fetch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCopyMappedDirPlacesNestedFiles(t *testing.T) {
	src := t.TempDir()
	writeMapped(t, filepath.Join(src, "tool"), "tool\n", 0o755)
	writeMapped(t, filepath.Join(src, "nested", "readme"), extraBody, 0o644)
	dest := filepath.Join(t.TempDir(), "pkg")

	if err := copyMapped(context.Background(), src, dest, 0o755); err != nil {
		t.Fatalf("copyMapped: %v", err)
	}
	if got := string(mustRead(t, filepath.Join(dest, "tool"))); got != "tool\n" {
		t.Errorf("tool = %q, want %q", got, "tool\n")
	}
	if got := filePerm(t, filepath.Join(dest, "tool")); got != 0o755 {
		t.Errorf("tool mode = %o, want 0755", got)
	}
	if got := filePerm(t, filepath.Join(dest, "nested", "readme")); got != 0o644 {
		t.Errorf("readme mode = %o, want 0644", got)
	}
	if got := filePerm(t, dest); got != 0o755 {
		t.Errorf("dest dir mode = %o, want 0755", got)
	}
	if got := filePerm(t, filepath.Join(dest, "nested")); got != 0o755 {
		t.Errorf("nested dir mode = %o, want 0755", got)
	}
}

func TestCopyMappedDirRefusesEmpty(t *testing.T) {
	src := t.TempDir()
	if err := os.Mkdir(filepath.Join(src, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "pkg")
	err := copyMapped(context.Background(), src, dest, 0o755)
	if err == nil {
		t.Fatal("copyMapped succeeded, want empty directory error")
	}
	if !strings.Contains(err.Error(), "no regular files") {
		t.Fatalf("error = %v, want no regular files", err)
	}
}

func TestCopyMappedDirRefusesMode644(t *testing.T) {
	src := t.TempDir()
	writeMapped(t, filepath.Join(src, "tool"), "tool\n", 0o755)
	err := copyMapped(context.Background(), src, filepath.Join(t.TempDir(), "pkg"), 0o644)
	if err == nil {
		t.Fatal("copyMapped succeeded, want directory mode error")
	}
	if !strings.Contains(err.Error(), "0755") {
		t.Fatalf("error = %v, want 0755", err)
	}
}

func TestCopyMappedDirRefusesPlantedSymlink(t *testing.T) {
	src := t.TempDir()
	writeMapped(t, filepath.Join(src, "tool"), "tool\n", 0o755)
	if err := os.Symlink("tool", filepath.Join(src, "also")); err != nil {
		t.Fatal(err)
	}
	err := copyMapped(context.Background(), src, filepath.Join(t.TempDir(), "pkg"), 0o755)
	if err == nil {
		t.Fatal("copyMapped succeeded, want symlink error")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error = %v, want symlink", err)
	}
}

func TestCopyMappedDirRefusesPlantedHardlink(t *testing.T) {
	src := t.TempDir()
	tool := filepath.Join(src, "tool")
	writeMapped(t, tool, "tool\n", 0o755)
	if err := os.Link(tool, filepath.Join(src, "also")); err != nil {
		t.Fatal(err)
	}
	err := copyMapped(context.Background(), src, filepath.Join(t.TempDir(), "pkg"), 0o755)
	if err == nil {
		t.Fatal("copyMapped succeeded, want hardlink error")
	}
	if !strings.Contains(err.Error(), "hardlink") {
		t.Fatalf("error = %v, want hardlink", err)
	}
}

func TestCopyMappedDirRefusesFIFO(t *testing.T) {
	src := t.TempDir()
	writeMapped(t, filepath.Join(src, "tool"), "tool\n", 0o755)
	if err := syscall.Mkfifo(filepath.Join(src, "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := copyMapped(context.Background(), src, filepath.Join(t.TempDir(), "pkg"), 0o755)
	if err == nil {
		t.Fatal("copyMapped succeeded, want non-regular error")
	}
}

func TestCopyMappedDirHonorsCancel(t *testing.T) {
	src := t.TempDir()
	writeMapped(t, filepath.Join(src, "tool"), "tool\n", 0o755)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := copyMapped(ctx, src, filepath.Join(t.TempDir(), "pkg"), 0o755)
	if err == nil {
		t.Fatal("copyMapped succeeded, want canceled context")
	}
}

func writeMapped(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
