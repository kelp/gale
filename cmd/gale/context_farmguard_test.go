package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/projects"
)

// TestNewCmdContextWiresFarmGuard: the installer a command context
// builds must carry the cross-project farm claimant guard, wired
// over this machine's registered scopes with the initiating scope
// excluded. Without the wiring every install commits farm links
// unguarded and acceptance test 28's sync/install cases cannot
// hold.
func TestNewCmdContextWiresFarmGuard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	projDir := filepath.Join(home, "proj-a")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(projDir, "gale.toml"), []byte("[packages]\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	chdirTo(t, projDir)

	// External project B locked to curl@8.19.0-1, which is
	// installed with a farmable dylib.
	storeRoot := filepath.Join(home, ".gale", "pkg")
	oldDir := filepath.Join(storeRoot, "curl", "8.19.0-1")
	newDir := filepath.Join(storeRoot, "curl", "8.20.0-1")
	for _, d := range []string{oldDir, newDir} {
		lib := filepath.Join(d, "lib")
		if err := os.MkdirAll(lib, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(lib, sonameFor("libcurl")), []byte("x"), 0o644,
		); err != nil {
			t.Fatal(err)
		}
	}
	projB := filepath.Join(home, "proj-b")
	if err := os.MkdirAll(projB, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := projects.Register(
		filepath.Join(home, ".gale"), projB,
	); err != nil {
		t.Fatal(err)
	}
	lf := &lockfile.V1{
		Version: lockfile.SchemaVersion,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{Roots: []string{"curl@8.19.0-1"}},
		},
		Packages: map[string]lockfile.Package{
			"curl@8.19.0-1": v1Node(),
		},
	}
	if err := lockfile.WriteV1(
		filepath.Join(projB, "gale.lock"), lf,
	); err != nil {
		t.Fatal(err)
	}

	ctx, err := newCmdContext("", false, false)
	if err != nil {
		t.Fatalf("newCmdContext: %v", err)
	}
	if ctx.Installer.FarmGuard == nil {
		t.Fatal("Installer.FarmGuard is not wired")
	}
	// The wired guard sees project B's claim: repointing the
	// claimed soname is refused, the claimed version is allowed.
	if err := ctx.Installer.FarmGuard([]string{newDir}); !errors.Is(
		err, farm.ErrClaimConflict,
	) {
		t.Errorf("conflicting install must be refused, got: %v", err)
	}
	if err := ctx.Installer.FarmGuard([]string{oldDir}); err != nil {
		t.Errorf("agreeing install must be allowed, got: %v", err)
	}
}
