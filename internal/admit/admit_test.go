package admit

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestClassifyHostELF(t *testing.T) {
	p := filepath.Join(t.TempDir(), "just")
	if err := os.WriteFile(p, ELFStub(runtime.GOARCH), 0o755); err != nil {
		t.Fatal(err)
	}
	kind, arch, err := Classify(p)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if kind != KindELF {
		t.Fatalf("kind = %v, want ELF", kind)
	}
	if arch != runtime.GOARCH {
		t.Fatalf("arch = %q, want %s", arch, runtime.GOARCH)
	}
}

func TestClassifyMachOStub(t *testing.T) {
	p := filepath.Join(t.TempDir(), "just")
	if err := os.WriteFile(p, MachOARM64Stub(), 0o755); err != nil {
		t.Fatal(err)
	}
	kind, arch, err := Classify(p)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if kind != KindMachO || arch != "arm64" {
		t.Fatalf("kind=%v arch=%q, want Mach-O arm64", kind, arch)
	}
}

func TestClassifyTextIsNotBinary(t *testing.T) {
	p := filepath.Join(t.TempDir(), "just")
	if err := os.WriteFile(p, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Classify(p); err == nil {
		t.Fatal("Classify succeeded, want ErrNotBinary")
	}
}

func TestSystemOnly(t *testing.T) {
	t.Parallel()
	cases := []struct {
		goos, lib string
		want      bool
	}{
		{"darwin", "/usr/lib/libSystem.B.dylib", true},
		{"darwin", "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", true},
		{"darwin", "/opt/homebrew/lib/foo.dylib", false},
		{"linux", "linux-vdso.so.1", true},
		{"linux", "/lib64/ld-linux-x86-64.so.2", true},
		{"linux", "/lib/x86_64-linux-gnu/libc.so.6", true},
		{"linux", "/opt/foo/libbar.so", false},
		{"linux", "/lib/x86_64-linux-gnu/libm.so.6", false},
	}
	for _, tc := range cases {
		if got := SystemOnly(tc.goos, tc.lib); got != tc.want {
			t.Errorf("SystemOnly(%s, %s) = %v, want %v", tc.goos, tc.lib, got, tc.want)
		}
	}
}

func TestParseDynamicLibsOtool(t *testing.T) {
	t.Parallel()
	out := `/tmp/just:
	/usr/lib/libSystem.B.dylib (compatibility version 1.0.0, current version 1351.0.0)
	/System/Library/Frameworks/CoreFoundation.framework/Versions/A/CoreFoundation (compatibility version 150.0.0, current version 1.0.0)
`
	libs, err := ParseDynamicLibs(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(libs) != 2 || !strings.HasPrefix(libs[0], "/usr/lib/") {
		t.Fatalf("libs = %#v", libs)
	}
}

func TestParseDynamicLibsLdd(t *testing.T) {
	t.Parallel()
	out := `	linux-vdso.so.1 (0x00007ffd)
	libc.so.6 => /lib/x86_64-linux-gnu/libc.so.6 (0x00007f)
	/lib64/ld-linux-x86-64.so.2 (0x00007f)
`
	libs, err := ParseDynamicLibs(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(libs) != 3 {
		t.Fatalf("libs = %#v", libs)
	}
}

func TestParseDynamicLibsStatic(t *testing.T) {
	t.Parallel()
	libs, err := ParseDynamicLibs("\tnot a dynamic executable")
	if err != nil || libs != nil {
		t.Fatalf("libs = %#v err=%v", libs, err)
	}
}

func TestInspectTreeNoBinary(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "readme"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := InspectTree(context.Background(), dir, "linux", "amd64", stubInsp{})
	if err == nil {
		t.Fatal("InspectTree succeeded, want ErrNoBinary")
	}
}

type stubInsp struct {
	libs []string
	err  error
	sign error
}

func (s stubInsp) CodeSign(string) error                { return s.sign }
func (s stubInsp) DynamicLibs(string) ([]string, error) { return s.libs, s.err }
