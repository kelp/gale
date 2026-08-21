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

func TestClassifyFatMachO(t *testing.T) {
	p := filepath.Join(t.TempDir(), "just")
	if err := os.WriteFile(p, MachOFatARM64Stub(), 0o755); err != nil {
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

func TestInspectTreeFatMachO(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "just")
	if err := os.WriteFile(p, MachOFatARM64Stub(), 0o755); err != nil {
		t.Fatal(err)
	}
	err := InspectTree(context.Background(), dir, "darwin", "arm64", stubInsp{
		libs: []string{"/usr/lib/libSystem.B.dylib"},
	})
	if err != nil {
		t.Fatalf("InspectTree: %v", err)
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
		{"darwin", "/usr/libexec/libfoo.dylib", false},
		{"darwin", "/Systematics/libfoo.dylib", false},
		{"linux", "linux-vdso.so.1", true},
		{"linux", "/opt/linux-vdso.so.1", false},
		{"linux", "/lib64/ld-linux-x86-64.so.2", true},
		{"linux", "/lib/x86_64-linux-gnu/libc.so.6", true},
		{"linux", "/opt/foo/libbar.so", false},
		{"linux", "/lib/x86_64-linux-gnu/libm.so.6", false},
		{"linux", "/opt/foo/libc.so.6", false},
		{"linux", "/tmp/ld-linux-x86-64.so.2", false},
		{"linux", "libc.so.6", true},
		{"linux", "libc.so.evil", false},
		{"linux", "opt/foo/libc.so.6", false},
		{"linux", "/lib/../opt/foo/libc.so.6", false},
		{"linux", "./libc.so.6", false},
		{"darwin", "/usr/lib/../opt/foo.dylib", false},
		{"darwin", "usr/lib/libSystem.B.dylib", false},
		{"darwin", "/System/Volumes/Data/opt/foo.dylib", false},
		{"darwin", "/System/Library/../Volumes/Data/opt/foo.dylib", false},
	}
	for _, tc := range cases {
		if got := SystemOnly(tc.goos, tc.lib); got != tc.want {
			t.Errorf("SystemOnly(%s, %s) = %v, want %v", tc.goos, tc.lib, got, tc.want)
		}
	}
}

func TestLinuxSearchPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		p    string
		want bool
	}{
		{"/lib64", true},
		{"/usr/lib", true},
		{"/usr/lib64/x86_64-linux-gnu", true},
		{"/lib/x86_64-linux-gnu", true},
		{"/opt/evil", false},
		{"$ORIGIN", false},
		{"$ORIGIN/../lib", false},
		{"/lib/../opt/foo", false},
		{"opt/foo", false},
		{"./lib", false},
	}
	for _, tc := range cases {
		if got := linuxSearchPath(tc.p); got != tc.want {
			t.Errorf("linuxSearchPath(%s) = %v, want %v", tc.p, got, tc.want)
		}
	}
}

func TestCheckLinuxSearchPaths(t *testing.T) {
	t.Parallel()
	if err := checkLinuxSearchPaths([]string{"/lib64:/usr/lib"}); err != nil {
		t.Fatalf("system paths: %v", err)
	}
	err := checkLinuxSearchPaths([]string{"$ORIGIN:/lib64"})
	if err == nil || !strings.Contains(err.Error(), "non-system search path") {
		t.Fatalf("err = %v, want non-system search path", err)
	}
	for _, v := range []string{"/lib64:", ":/lib64", "/lib64::/usr/lib", ""} {
		err := checkLinuxSearchPaths([]string{v})
		if err == nil || !strings.Contains(err.Error(), "non-system search path") {
			t.Fatalf("checkLinuxSearchPaths(%q) = %v, want empty-segment reject", v, err)
		}
	}
}

func TestNativeDynamicLibsParsesELF(t *testing.T) {
	p := filepath.Join(t.TempDir(), "just")
	if err := os.WriteFile(p, ELFStub(runtime.GOARCH), 0o755); err != nil {
		t.Fatal(err)
	}
	libs, err := (Native{}).DynamicLibs(p)
	if err != nil {
		t.Fatalf("DynamicLibs: %v", err)
	}
	if len(libs) != 0 {
		t.Fatalf("libs = %#v, want none", libs)
	}
}

func TestNativeDynamicLibsIncludesInterp(t *testing.T) {
	const interp = "/opt/evil/ld-linux-x86-64.so.2"
	p := filepath.Join(t.TempDir(), "just")
	if err := os.WriteFile(p, ELFInterpStub(runtime.GOARCH, interp), 0o755); err != nil {
		t.Fatal(err)
	}
	libs, err := (Native{}).DynamicLibs(p)
	if err != nil {
		t.Fatalf("DynamicLibs: %v", err)
	}
	if len(libs) != 1 || libs[0] != interp {
		t.Fatalf("libs = %#v, want [%q]", libs, interp)
	}
}

func TestInspectTreeRejectsNonSystemInterp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "just")
	if err := os.WriteFile(p, ELFInterpStub(runtime.GOARCH, "/opt/evil/ld-linux-x86-64.so.2"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := InspectTree(context.Background(), dir, "linux", runtime.GOARCH, Native{})
	if err == nil {
		t.Fatal("InspectTree succeeded, want non-system interpreter")
	}
	if !strings.Contains(err.Error(), "non-system library") {
		t.Fatalf("err = %v, want non-system library", err)
	}
}

func TestNativeDynamicLibsParsesMachO(t *testing.T) {
	p := filepath.Join(t.TempDir(), "just")
	if err := os.WriteFile(p, MachOARM64Stub(), 0o755); err != nil {
		t.Fatal(err)
	}
	libs, err := (Native{}).DynamicLibs(p)
	if err != nil {
		t.Fatalf("DynamicLibs: %v", err)
	}
	if len(libs) != 0 {
		t.Fatalf("libs = %#v, want none", libs)
	}
}

func TestNativeDynamicLibsParsesFatMachO(t *testing.T) {
	p := filepath.Join(t.TempDir(), "just")
	if err := os.WriteFile(p, MachOFatARM64Stub(), 0o755); err != nil {
		t.Fatal(err)
	}
	libs, err := (Native{}).DynamicLibs(p)
	if err != nil {
		t.Fatalf("DynamicLibs: %v", err)
	}
	if len(libs) != 0 {
		t.Fatalf("libs = %#v, want none", libs)
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
