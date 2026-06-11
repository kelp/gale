package projects

import (
	"os"
	"path/filepath"
	"testing"
)

// writeGaleToml drops a minimal gale.toml in dir so the
// path counts as a live project for Prune.
func writeGaleToml(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(
		filepath.Join(dir, "gale.toml"),
		[]byte("[packages]\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterAndList(t *testing.T) {
	galeHome := filepath.Join(t.TempDir(), ".gale")
	proj1 := t.TempDir()
	proj2 := t.TempDir()

	if err := Register(galeHome, proj1); err != nil {
		t.Fatalf("Register(%s): %v", proj1, err)
	}
	if err := Register(galeHome, proj2); err != nil {
		t.Fatalf("Register(%s): %v", proj2, err)
	}

	got, err := List(galeHome)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 projects, got %d: %v", len(got), got)
	}
	want1, _ := filepath.EvalSymlinks(proj1)
	want2, _ := filepath.EvalSymlinks(proj2)
	if got[0] != want1 || got[1] != want2 {
		t.Errorf("want [%s %s], got %v", want1, want2, got)
	}
}

func TestRegisterDedupes(t *testing.T) {
	galeHome := filepath.Join(t.TempDir(), ".gale")
	proj := t.TempDir()

	for i := 0; i < 3; i++ {
		if err := Register(galeHome, proj); err != nil {
			t.Fatalf("Register #%d: %v", i, err)
		}
	}

	got, err := List(galeHome)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("want 1 entry after repeat registers, got %d: %v",
			len(got), got)
	}
}

// TestRegisterCanonicalizesSymlinks verifies that registering
// the same project via a symlinked spelling and via its real
// path produces one entry. macOS /var vs /private/var is the
// motivating case.
func TestRegisterCanonicalizesSymlinks(t *testing.T) {
	galeHome := filepath.Join(t.TempDir(), ".gale")
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	if err := Register(galeHome, real); err != nil {
		t.Fatal(err)
	}
	if err := Register(galeHome, link); err != nil {
		t.Fatal(err)
	}

	got, err := List(galeHome)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("symlinked and real spelling must dedupe to "+
			"1 entry, got %d: %v", len(got), got)
	}
}

func TestListMissingFileReturnsEmpty(t *testing.T) {
	got, err := List(filepath.Join(t.TempDir(), ".gale"))
	if err != nil {
		t.Fatalf("List on missing file must not error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want empty list, got %v", got)
	}
}

func TestRegisterCreatesGaleHome(t *testing.T) {
	galeHome := filepath.Join(t.TempDir(), "deep", ".gale")
	proj := t.TempDir()
	if err := Register(galeHome, proj); err != nil {
		t.Fatalf("Register must create gale home: %v", err)
	}
	got, err := List(galeHome)
	if err != nil || len(got) != 1 {
		t.Fatalf("want 1 entry, got %v (err %v)", got, err)
	}
}

func TestPruneRemovesVanishedProjects(t *testing.T) {
	galeHome := filepath.Join(t.TempDir(), ".gale")
	live := t.TempDir()
	writeGaleToml(t, live)
	ghost := t.TempDir() // no gale.toml — vanished project

	if err := Register(galeHome, live); err != nil {
		t.Fatal(err)
	}
	if err := Register(galeHome, ghost); err != nil {
		t.Fatal(err)
	}

	if err := Prune(galeHome); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	got, err := List(galeHome)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	wantLive, _ := filepath.EvalSymlinks(live)
	if len(got) != 1 || got[0] != wantLive {
		t.Errorf("want [%s] after prune, got %v", wantLive, got)
	}
}

// TestPruneKeepsToolVersionsProjects verifies that a project
// managed via .tool-versions (no gale.toml) is still treated
// as live — gale's config loading falls back to
// .tool-versions, so its generation deserves retention too.
func TestPruneKeepsToolVersionsProjects(t *testing.T) {
	galeHome := filepath.Join(t.TempDir(), ".gale")
	proj := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(proj, ".tool-versions"),
		[]byte("jq 1.7\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	if err := Register(galeHome, proj); err != nil {
		t.Fatal(err)
	}
	if err := Prune(galeHome); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	got, err := List(galeHome)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Errorf(".tool-versions project must survive prune, "+
			"got %v", got)
	}
}

func TestPruneMissingFileIsNoop(t *testing.T) {
	if err := Prune(filepath.Join(t.TempDir(), ".gale")); err != nil {
		t.Fatalf("Prune on missing registry must not error: %v", err)
	}
}
