package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/inspect"
	"github.com/kelp/gale/internal/output"
)

// linkageStore installs a real Mach-O under
// <galeDir>/pkg/example/1.0/bin/example whose only dep reference is
// ref, reached through the relative farm rpath a gale build bakes in.
//
// The rpath spelling is relativeFarmRpath's for an executable one
// level below the prefix: four levels up from <store>/<ver>/bin lands
// on <galeDir>, so the ref resolves through <galeDir>/lib and nowhere
// else. That is the whole point of the check — a binary carrying only
// the farm rpath aborts at exec the moment the farm stops providing
// the name it recorded.
func linkageStore(t *testing.T, galeDir, ref string) {
	t.Helper()
	binDir := filepath.Join(galeDir, "pkg", "example", "1.0", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "example.c")
	if err := os.WriteFile(src,
		[]byte("int main(void) { return 0; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(binDir, "example")
	build := exec.Command("cc", "-Wl,-headerpad_max_install_names",
		"-o", bin, src)
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("cc unavailable: %v\n%s", err, out)
	}
	if err := exec.Command("install_name_tool", "-add_rpath",
		"@executable_path/../../../../lib", bin).Run(); err != nil {
		t.Fatalf("add_rpath: %v", err)
	}
	if err := exec.Command("install_name_tool", "-change",
		firstLoadedDylib(t, bin), ref, bin).Run(); err != nil {
		t.Fatalf("change dep to %s: %v", ref, err)
	}
	// Best-effort, as in internal/inspect's fixture: an unsigned
	// Mach-O still parses, it just cannot be executed. Nothing here
	// executes it.
	_ = exec.Command("codesign", "--force", "--sign", "-", bin).Run()
}

// firstLoadedDylib returns the first LC_LOAD_DYLIB name otool reports
// for bin. Read rather than assumed: hardcoding libSystem's path
// would turn a future toolchain change into a silent no-op rewrite
// and a test that passes for the wrong reason.
func firstLoadedDylib(t *testing.T, bin string) string {
	t.Helper()
	out, err := exec.Command("otool", "-L", bin).Output()
	if err != nil {
		t.Fatalf("otool -L: %v", err)
	}
	// Line 0 is the file name; every following line is "\t<path> (…)".
	for _, line := range strings.Split(string(out), "\n")[1:] {
		if !strings.HasPrefix(line, "\t") {
			continue
		}
		name, _, ok := strings.Cut(strings.TrimSpace(line), " (")
		if ok && name != "" {
			return name
		}
	}
	t.Fatalf("no LC_LOAD_DYLIB to rewrite in %s:\n%s", bin, out)
	return ""
}

// linkageContext is the doctor context for a run over the store
// linkageStore built: one declared global package, no project scope.
func linkageContext(home, galeDir string) (*doctorContext, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return &doctorContext{
		galeDir:    galeDir,
		storeRoot:  filepath.Join(galeDir, "pkg"),
		cwd:        home,
		globalPkgs: map[string]string{"example": "1.0"},
		projPkgs:   map[string]string{},
		out:        output.NewWithOptions(buf, output.Options{}),
	}, buf
}

// TestDoctorFailsOnAnUnresolvableRpathDep is gh#215's red case: a
// prebuilt from a gale that predated canonicalDepName carries an
// unversioned @rpath ref the farm never provides. The binary is
// broken now — dyld aborts on the next exec — so the check fails
// rather than warns, the same class as a broken bin/ symlink.
func TestDoctorFailsOnAnUnresolvableRpathDep(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin only: the fixture needs install_name_tool")
	}
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	linkageStore(t, galeDir, "@rpath/libfake.dylib")

	ctx, buf := linkageContext(home, galeDir)
	if checkMachOLinkage(ctx) {
		t.Errorf("an unresolvable @rpath ref must fail the check; "+
			"output: %q", buf.String())
	}
	for _, want := range []string{
		"example", "bin/example", "@rpath/libfake.dylib",
		"gale install --build",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("report must name %q; output: %q", want, buf.String())
		}
	}
	// Mutation probe (temporary, reverted in the next commit): a
	// terminal t.Fatal turns a test that RAN red while leaving one
	// that SKIPPED green, which is the only way to tell the two
	// apart on a macOS leg that runs without -v (gh#216).
	t.Fatal("mutation probe")
}

// TestDoctorPassesWhenTheFarmResolvesTheRef is the guard against the
// false-positive direction. The same binary with the same ref is
// healthy once the farm provides the name, and a check that reddened
// there would redden on every machine.
func TestDoctorPassesWhenTheFarmResolvesTheRef(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin only: the fixture needs install_name_tool")
	}
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	linkageStore(t, galeDir, "@rpath/libfake.dylib")

	farmDir := filepath.Join(galeDir, "lib")
	if err := os.MkdirAll(farmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(farmDir, "libfake.dylib"),
		[]byte("stands in for a farmed dylib"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, buf := linkageContext(home, galeDir)
	if !checkMachOLinkage(ctx) {
		t.Errorf("a ref the farm resolves is not a finding; output: %q",
			buf.String())
	}
	if strings.Contains(buf.String(), "libfake.dylib") {
		t.Errorf("nothing to report, so the ref must not appear; "+
			"output: %q", buf.String())
	}
	// Mutation probe (temporary, reverted in the next commit): a
	// terminal t.Fatal turns a test that RAN red while leaving one
	// that SKIPPED green, which is the only way to tell the two
	// apart on a macOS leg that runs without -v (gh#216).
	t.Fatal("mutation probe")
}

// TestDoctorSkipsBinaryLinkageOffDarwin pins the runtime gate.
// internal/inspect compiles everywhere, but on ELF a bare
// libc.so.6 DT_NEEDED resolves through the system loader rather than
// an rpath, so resolveRef would flag every binary on the machine.
// The goos seam is what lets this run on both platforms; a gate read
// straight from runtime.GOOS could only ever be tested on one.
func TestDoctorSkipsBinaryLinkageOffDarwin(t *testing.T) {
	home := t.TempDir()
	ctx, buf := linkageContext(home, filepath.Join(home, ".gale"))
	if !checkMachOLinkageOn(ctx, "linux") {
		t.Errorf("the skip branch must pass; output: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "darwin only") {
		t.Errorf("the skip must be visible in the report; output: %q",
			buf.String())
	}
}

// TestUnresolvableRefLinesReportOnlyUnresolvableRefs pins the filter.
// The other four kinds are gale inspect's: a dead rpath entry beside
// a live one resolves fine, and the recipe-dependent kinds need a
// registry doctor deliberately does not touch.
func TestUnresolvableRefLinesReportOnlyUnresolvableRefs(t *testing.T) {
	cases := []struct {
		kind      inspect.Kind
		wantLines int
	}{
		{inspect.KindUnresolvableRef, 1},
		{inspect.KindStaleRpath, 0},
		{inspect.KindVersionSkew, 0},
		{inspect.KindUndeclaredDep, 0},
		{inspect.KindOverDeclaredDep, 0},
	}
	for _, tc := range cases {
		got := unresolvableRefLines([]inspect.Issue{{
			Kind:    tc.kind,
			Package: "example",
			Version: "1.0",
			Binary:  "bin/example",
			Details: "@rpath/libfake.dylib",
		}})
		if len(got) != tc.wantLines {
			t.Errorf("%s: got %d line(s) %v, want %d",
				tc.kind, len(got), got, tc.wantLines)
			continue
		}
		if tc.wantLines == 0 {
			continue
		}
		for _, want := range []string{
			"example", "1.0", "bin/example", "@rpath/libfake.dylib",
		} {
			if !strings.Contains(got[0], want) {
				t.Errorf("%s: line %q omits %q", tc.kind, got[0], want)
			}
		}
	}
}
