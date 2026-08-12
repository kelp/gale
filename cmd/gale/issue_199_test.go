package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/output"
)

// opensslLikeStore creates storeRoot/<name>/<version>/lib holding a
// versioned soname plus the unversioned alias symlink beside it —
// the layout openssl and openssl4 both install.
func opensslLikeStore(
	t *testing.T, storeRoot, name, version, soname string,
) string {
	t.Helper()
	storeDir := filepath.Join(storeRoot, name, version)
	libDir := filepath.Join(storeDir, "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libDir, soname),
		[]byte("not really mach-o"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(soname,
		filepath.Join(libDir, "libssl.dylib")); err != nil {
		t.Fatal(err)
	}
	return storeDir
}

// TestDoctorWarnsAboutAContestedAliasWithoutFailing is the gh#50
// regression guard for gh#199's new diagnostic.
//
// openssl and openssl4 both ship libssl.dylib, so the farm drops it
// — correctly, and permanently while both are installed. Reporting
// that through CheckDrift would render it as an Error telling the
// user to run `gale doctor --repair`, and the repair could never
// clear it, because the farm is already doing the only safe thing.
// It must warn and still pass.
func TestDoctorWarnsAboutAContestedAliasWithoutFailing(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("alias farming is darwin-only")
	}
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")
	farmDir := filepath.Join(galeDir, "lib")

	ssl3 := opensslLikeStore(t, storeRoot, "openssl", "3.6.1-4",
		"libssl.3.dylib")
	ssl4 := opensslLikeStore(t, storeRoot, "openssl4", "4.0.0-2",
		"libssl.4.dylib")
	if err := farm.Rebuild([]string{ssl3, ssl4}, farmDir); err != nil {
		t.Fatalf("an alias collision must not fail the rebuild: %v", err)
	}

	var buf bytes.Buffer
	ctx := &doctorContext{
		galeDir:   galeDir,
		storeRoot: storeRoot,
		cwd:       home,
		globalPkgs: map[string]string{
			"openssl":  "3.6.1-4",
			"openssl4": "4.0.0-2",
		},
		projPkgs: map[string]string{},
		out:      output.NewWithOptions(&buf, output.Options{}),
	}

	if !checkFarmScope(ctx, ctx.globalPkgs) {
		t.Errorf("a dropped alias is the correct steady state and "+
			"must not fail the farm check; output: %q", buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "libssl.dylib") {
		t.Errorf("the drop must be visible; output: %q", out)
	}
	if !strings.Contains(out, "openssl@3.6.1-4") ||
		!strings.Contains(out, "openssl4@4.0.0-2") {
		t.Errorf("the warning must name both providers; output: %q", out)
	}
	if strings.Contains(out, "gale doctor --repair") {
		t.Errorf("no repair can clear a collision, so it must not be "+
			"offered (gh#50); output: %q", out)
	}
	if strings.Contains(out, "missing farm entry") {
		t.Errorf("a dropped alias is not missing drift; output: %q", out)
	}
}

// TestDoctorIsQuietWhenTheAliasIsUncontested: the sole-provider
// case is the fix working, not a problem, and must produce no
// warning at all.
func TestDoctorIsQuietWhenTheAliasIsUncontested(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("alias farming is darwin-only")
	}
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")
	farmDir := filepath.Join(galeDir, "lib")

	ssl3 := opensslLikeStore(t, storeRoot, "openssl", "3.6.1-4",
		"libssl.3.dylib")
	if err := farm.Rebuild([]string{ssl3}, farmDir); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	ctx := &doctorContext{
		galeDir:    galeDir,
		storeRoot:  storeRoot,
		cwd:        home,
		globalPkgs: map[string]string{"openssl": "3.6.1-4"},
		projPkgs:   map[string]string{},
		out:        output.NewWithOptions(&buf, output.Options{}),
	}

	if !checkFarmScope(ctx, ctx.globalPkgs) {
		t.Errorf("farm should be clean; output: %q", buf.String())
	}
	if strings.Contains(buf.String(), "not farmed") {
		t.Errorf("an uncontested alias is farmed, so nothing to "+
			"report; output: %q", buf.String())
	}
}
