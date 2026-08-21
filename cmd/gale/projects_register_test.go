package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/projects"
)

// newTestProject creates a project dir with an empty-package
// gale.toml and returns its symlink-resolved path (t.TempDir
// can sit behind symlinks, e.g. macOS /var → /private/var).
func newTestProject(t *testing.T) string {
	t.Helper()
	proj := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(proj, "gale.toml"),
		[]byte("[packages]\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(proj)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// projectLayout is a project that has both gale.toml and
// .gale, the shape rebuildGeneration publishes.
type projectLayout struct {
	home, root, galeDir, configPath, storeRoot string
}

func newProjectLayout(t *testing.T) projectLayout {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := newTestProject(t)
	galeDir := filepath.Join(root, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Store lives under the project galeDir so farm claimants
	// read <project>/.gale/projects, not the machine registry
	// HOME/.gale/projects. The failure test can then break only
	// the machine registry.
	storeRoot := filepath.Join(galeDir, "pkg")
	if err := os.MkdirAll(storeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	return projectLayout{
		home:       home,
		root:       root,
		galeDir:    galeDir,
		configPath: filepath.Join(root, "gale.toml"),
		storeRoot:  storeRoot,
	}
}

// registryContains reports whether the machine-local project
// registry under HOME lists proj.
func registryContains(t *testing.T, home, proj string) bool {
	t.Helper()
	list, err := projects.List(filepath.Join(home, ".gale"))
	if err != nil {
		t.Fatalf("listing registry: %v", err)
	}
	for _, p := range list {
		if p == proj {
			return true
		}
	}
	return false
}

func currentGen(t *testing.T, galeDir string) int {
	t.Helper()
	cur, err := generation.Current(galeDir)
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	return cur
}

// TestNewCmdContextDoesNotRegisterProject pins that resolving
// a project-scoped context is not publication. Read-only
// commands go through newCmdContext and must not write the
// registry (fetch-dont-build §15.9).
func TestNewCmdContextDoesNotRegisterProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := newTestProject(t)
	t.Chdir(proj)

	if _, err := newCmdContext("", false, false); err != nil {
		t.Fatalf("newCmdContext: %v", err)
	}

	if registryContains(t, home, proj) {
		t.Errorf("newCmdContext must not register %s", proj)
	}
}

// TestNewCmdContextSkipsGlobalScope verifies a global-scope
// context (no project anywhere) does not register ~/.gale as
// a project.
func TestNewCmdContextSkipsGlobalScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir()) // neutral cwd, no project

	if _, err := newCmdContext("", false, false); err != nil {
		t.Fatalf("newCmdContext: %v", err)
	}

	list, err := projects.List(filepath.Join(home, ".gale"))
	if err != nil {
		t.Fatalf("listing registry: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("global scope must not register anything, "+
			"got %v", list)
	}
}

// TestEnvCommandDoesNotRegisterProject pins that gale env
// (direnv activation) is read-only and does not write the
// registry.
func TestEnvCommandDoesNotRegisterProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := newTestProject(t)
	t.Chdir(proj)

	envCmd.SetOut(io.Discard)
	t.Cleanup(func() { envCmd.SetOut(nil) })
	if err := envCmd.RunE(envCmd, nil); err != nil {
		t.Fatalf("gale env: %v", err)
	}

	if registryContains(t, home, proj) {
		t.Errorf("gale env must not register %s", proj)
	}
}

// TestDoctorDoesNotRegisterProject pins that doctor, which
// still builds a newCmdContext, does not write the registry.
func TestDoctorDoesNotRegisterProject(t *testing.T) {
	p := newProjectLayout(t)
	t.Chdir(p.root)

	var stdout, stderr bytes.Buffer
	_ = runDoctor(&doctorIO{
		galeDir: p.galeDir,
		cwd:     p.root,
		stdout:  &stdout,
		stderr:  &stderr,
	})

	if registryContains(t, p.home, p.root) {
		t.Errorf("gale doctor must not register %s", p.root)
	}
}

// TestRebuildGenerationRegistersProjectBeforeSwap is the
// publication path: a project layout rebuild records the
// canonical root and swaps current.
func TestRebuildGenerationRegistersProjectBeforeSwap(t *testing.T) {
	p := newProjectLayout(t)

	if err := rebuildGeneration(p.galeDir, p.storeRoot, p.configPath, nil); err != nil {
		t.Fatalf("rebuildGeneration: %v", err)
	}

	if !registryContains(t, p.home, p.root) {
		t.Fatalf("project %s not registered before swap", p.root)
	}
	if cur := currentGen(t, p.galeDir); cur == 0 {
		t.Fatal("current must point at the new generation")
	}
}

// TestRebuildGenerationRegistrationFailureLeavesCurrent pins
// that a registry write failure aborts the swap. ~/.gale/projects
// as a directory makes OpenFile/ReadFile fail as root (EISDIR).
func TestRebuildGenerationRegistrationFailureLeavesCurrent(t *testing.T) {
	p := newProjectLayout(t)

	if err := generation.Build(map[string]string{}, p.galeDir, p.storeRoot); err != nil {
		t.Fatalf("seed generation: %v", err)
	}
	before := currentGen(t, p.galeDir)
	if before == 0 {
		t.Fatal("seed generation must activate current")
	}

	homeGale, err := galeConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(homeGale, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}

	err = rebuildGeneration(p.galeDir, p.storeRoot, p.configPath, nil)
	if err == nil {
		t.Fatal("rebuildGeneration must fail when the registry cannot be written")
	}
	if after := currentGen(t, p.galeDir); after != before {
		t.Fatalf("current moved from gen/%d to gen/%d after a register failure",
			before, after)
	}
}

// TestRebuildGenerationGlobalDoesNotRegister pins that a
// global-layout rebuild (gale.toml inside galeDir) does not
// write the project registry.
func TestRebuildGenerationGlobalDoesNotRegister(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(galeDir, "gale.toml")
	if err := os.WriteFile(configPath, []byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	storeRoot := filepath.Join(galeDir, "pkg")

	if err := rebuildGeneration(galeDir, storeRoot, configPath, nil); err != nil {
		t.Fatalf("rebuildGeneration: %v", err)
	}

	list, err := projects.List(galeDir)
	if err != nil {
		t.Fatalf("listing registry: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("global rebuild must not register, got %v", list)
	}
}

// TestRebuildGenerationSkipsDryRun verifies dry-run does not
// mutate the registry even on the publication helper.
func TestRebuildGenerationSkipsDryRun(t *testing.T) {
	p := newProjectLayout(t)

	dryRun = true
	t.Cleanup(func() { dryRun = false })
	if err := rebuildGeneration(p.galeDir, p.storeRoot, p.configPath, nil); err != nil {
		t.Fatalf("rebuildGeneration: %v", err)
	}

	if registryContains(t, p.home, p.root) {
		t.Errorf("dry-run must not register projects")
	}
}

// TestSyncProjectDirNoopDoesNotRegister pins that an explicit
// projectDir sync which installs nothing and skips rebuild
// (empty packages, no current) does not write the registry.
func TestSyncProjectDirNoopDoesNotRegister(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_OFFLINE", "1")
	proj := newTestProject(t)
	t.Chdir(t.TempDir())

	if err := runSync("", false, false, false, proj); err != nil {
		t.Fatalf("runSync: %v", err)
	}

	if registryContains(t, home, proj) {
		t.Errorf("no-op sync must not register %s", proj)
	}
}

// TestSyncProjectDirPublishesAndRegisters pins the shell/run
// path: retarget at an explicit project, then publish. The
// retargeted root is what must be registered, not cwd.
func TestSyncProjectDirPublishesAndRegisters(t *testing.T) {
	p := newProjectLayout(t)
	t.Chdir(t.TempDir())

	ctx, err := newCmdContext("", false, false)
	if err != nil {
		t.Fatalf("newCmdContext: %v", err)
	}
	retargetSync(ctx, p.root)
	if err := rebuildGeneration(ctx.GaleDir, ctx.StoreRoot, ctx.GalePath, nil); err != nil {
		t.Fatalf("rebuild after retarget: %v", err)
	}

	if !registryContains(t, p.home, p.root) {
		t.Errorf("publishing the retargeted project must register %s", p.root)
	}
}
