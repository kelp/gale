package profile

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// --- Behavior 1: Create symlink ---

func TestLinkCreatesSymlinkInBinDir(t *testing.T) {
	binDir := t.TempDir()
	store := t.TempDir()

	target := filepath.Join(store, "jq")
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatalf("failed to create target file: %v", err)
	}

	p := NewProfile(binDir)

	if err := p.Link("jq", target); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	linkPath := filepath.Join(binDir, "jq")
	info, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("symlink does not exist: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected %q to be a symlink", linkPath)
	}
}

func TestLinkSymlinkPointsToTarget(t *testing.T) {
	binDir := t.TempDir()
	store := t.TempDir()

	target := filepath.Join(store, "jq")
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatalf("failed to create target file: %v", err)
	}

	p := NewProfile(binDir)

	if err := p.Link("jq", target); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	linkPath := filepath.Join(binDir, "jq")
	got, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("failed to read symlink: %v", err)
	}
	if got != target {
		t.Errorf("symlink target = %q, want %q", got, target)
	}
}

func TestLinkUsesFilenameAsSymlinkName(t *testing.T) {
	binDir := t.TempDir()
	store := t.TempDir()

	target := filepath.Join(store, "ripgrep")
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatalf("failed to create target file: %v", err)
	}

	p := NewProfile(binDir)

	if err := p.Link("ripgrep", target); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	linkPath := filepath.Join(binDir, "ripgrep")
	if _, err := os.Lstat(linkPath); err != nil {
		t.Errorf("expected symlink at %q: %v", linkPath, err)
	}
}

// --- Behavior 2: Update symlink ---

func TestUpdateReplacesExistingSymlink(t *testing.T) {
	binDir := t.TempDir()
	store := t.TempDir()

	oldTarget := filepath.Join(store, "jq-1.7.1")
	if err := os.WriteFile(oldTarget, []byte("old"), 0o755); err != nil {
		t.Fatalf("failed to create old target: %v", err)
	}
	newTarget := filepath.Join(store, "jq-1.8.0")
	if err := os.WriteFile(newTarget, []byte("new"), 0o755); err != nil {
		t.Fatalf("failed to create new target: %v", err)
	}

	p := NewProfile(binDir)

	if err := p.Link("jq", oldTarget); err != nil {
		t.Fatalf("Link error: %v", err)
	}

	if err := p.Update("jq", newTarget); err != nil {
		t.Fatalf("Update error: %v", err)
	}

	linkPath := filepath.Join(binDir, "jq")
	got, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("failed to read symlink: %v", err)
	}
	if got != newTarget {
		t.Errorf("symlink target = %q, want %q", got, newTarget)
	}
}

func TestUpdateKeepsSymlinkAsSymlink(t *testing.T) {
	binDir := t.TempDir()
	store := t.TempDir()

	oldTarget := filepath.Join(store, "jq-1.7.1")
	if err := os.WriteFile(oldTarget, []byte("old"), 0o755); err != nil {
		t.Fatalf("failed to create old target: %v", err)
	}
	newTarget := filepath.Join(store, "jq-1.8.0")
	if err := os.WriteFile(newTarget, []byte("new"), 0o755); err != nil {
		t.Fatalf("failed to create new target: %v", err)
	}

	p := NewProfile(binDir)

	if err := p.Link("jq", oldTarget); err != nil {
		t.Fatalf("Link error: %v", err)
	}

	if err := p.Update("jq", newTarget); err != nil {
		t.Fatalf("Update error: %v", err)
	}

	linkPath := filepath.Join(binDir, "jq")
	info, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("Lstat error: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected %q to remain a symlink", linkPath)
	}
}

func TestUpdateNonexistentReturnsError(t *testing.T) {
	binDir := t.TempDir()
	store := t.TempDir()

	target := filepath.Join(store, "jq")
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatalf("failed to create target file: %v", err)
	}

	p := NewProfile(binDir)

	err := p.Update("jq", target)
	if err == nil {
		t.Fatal("expected error when updating nonexistent symlink")
	}
}

// --- Behavior 3: Remove symlink ---

func TestRemoveDeletesSymlink(t *testing.T) {
	binDir := t.TempDir()
	store := t.TempDir()

	target := filepath.Join(store, "jq")
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatalf("failed to create target file: %v", err)
	}

	p := NewProfile(binDir)

	if err := p.Link("jq", target); err != nil {
		t.Fatalf("Link error: %v", err)
	}

	// Verify the symlink was created before testing removal.
	linkPath := filepath.Join(binDir, "jq")
	if _, err := os.Lstat(linkPath); err != nil {
		t.Fatalf("Link did not create symlink: %v", err)
	}

	if err := p.Remove("jq"); err != nil {
		t.Fatalf("Remove error: %v", err)
	}

	if _, err := os.Lstat(linkPath); !os.IsNotExist(err) {
		t.Errorf("expected symlink %q to be removed", linkPath)
	}
}

func TestRemoveNonexistentReturnsError(t *testing.T) {
	binDir := t.TempDir()
	p := NewProfile(binDir)

	err := p.Remove("nonexistent")
	if err == nil {
		t.Fatal("expected error when removing nonexistent symlink")
	}
}

// --- Behavior 4: List profile links ---

func TestListReturnsEmptyForEmptyBinDir(t *testing.T) {
	binDir := t.TempDir()
	p := NewProfile(binDir)

	links, err := p.List()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(links) != 0 {
		t.Errorf("List length = %d, want 0", len(links))
	}
}

func TestListReturnsSingleLink(t *testing.T) {
	binDir := t.TempDir()
	store := t.TempDir()

	target := filepath.Join(store, "jq")
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatalf("failed to create target file: %v", err)
	}

	p := NewProfile(binDir)

	if err := p.Link("jq", target); err != nil {
		t.Fatalf("Link error: %v", err)
	}

	links, err := p.List()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("List length = %d, want 1", len(links))
	}
	if links[0].Name != "jq" {
		t.Errorf("Name = %q, want %q", links[0].Name, "jq")
	}
	if links[0].Target != target {
		t.Errorf("Target = %q, want %q", links[0].Target, target)
	}
}

func TestListReturnsMultipleLinks(t *testing.T) {
	binDir := t.TempDir()
	store := t.TempDir()

	targets := map[string]string{
		"jq":      filepath.Join(store, "jq"),
		"ripgrep": filepath.Join(store, "ripgrep"),
		"fd":      filepath.Join(store, "fd"),
	}
	for _, path := range targets {
		if err := os.WriteFile(path, []byte("binary"), 0o755); err != nil {
			t.Fatalf("failed to create target file: %v", err)
		}
	}

	p := NewProfile(binDir)

	for name, tgt := range targets {
		if err := p.Link(name, tgt); err != nil {
			t.Fatalf("Link %q error: %v", name, err)
		}
	}

	links, err := p.List()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(links) != 3 {
		t.Fatalf("List length = %d, want 3", len(links))
	}

	sort.Slice(links, func(i, j int) bool {
		return links[i].Name < links[j].Name
	})
	if links[0].Name != "fd" {
		t.Errorf("links[0].Name = %q, want %q",
			links[0].Name, "fd")
	}
	if links[1].Name != "jq" {
		t.Errorf("links[1].Name = %q, want %q",
			links[1].Name, "jq")
	}
	if links[2].Name != "ripgrep" {
		t.Errorf("links[2].Name = %q, want %q",
			links[2].Name, "ripgrep")
	}
}

func TestListReturnsCorrectTargets(t *testing.T) {
	binDir := t.TempDir()
	store := t.TempDir()

	jqTarget := filepath.Join(store, "jq")
	rgTarget := filepath.Join(store, "ripgrep")
	if err := os.WriteFile(jqTarget, []byte("binary"), 0o755); err != nil {
		t.Fatalf("failed to create target file: %v", err)
	}
	if err := os.WriteFile(rgTarget, []byte("binary"), 0o755); err != nil {
		t.Fatalf("failed to create target file: %v", err)
	}

	p := NewProfile(binDir)

	if err := p.Link("jq", jqTarget); err != nil {
		t.Fatalf("Link error: %v", err)
	}
	if err := p.Link("ripgrep", rgTarget); err != nil {
		t.Fatalf("Link error: %v", err)
	}

	links, err := p.List()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("List length = %d, want 2", len(links))
	}

	sort.Slice(links, func(i, j int) bool {
		return links[i].Name < links[j].Name
	})
	if links[0].Target != jqTarget {
		t.Errorf("links[0].Target = %q, want %q",
			links[0].Target, jqTarget)
	}
	if links[1].Target != rgTarget {
		t.Errorf("links[1].Target = %q, want %q",
			links[1].Target, rgTarget)
	}
}

// --- Behavior 5: Link all binaries from a package ---

func TestLinkPackageBinariesCreatesSymlinks(t *testing.T) {
	binDir := t.TempDir()
	pkgBinDir := filepath.Join(t.TempDir(), "jq", "1.7.1", "bin")
	if err := os.MkdirAll(pkgBinDir, 0o755); err != nil {
		t.Fatalf("failed to create pkg bin dir: %v", err)
	}

	if err := os.WriteFile(
		filepath.Join(pkgBinDir, "jq"), []byte("binary"), 0o755,
	); err != nil {
		t.Fatalf("failed to create executable: %v", err)
	}

	p := NewProfile(binDir)

	if err := p.LinkPackageBinaries(pkgBinDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	linkPath := filepath.Join(binDir, "jq")
	info, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("symlink does not exist: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected %q to be a symlink", linkPath)
	}
}

func TestLinkPackageBinariesPointsToCorrectTarget(t *testing.T) {
	binDir := t.TempDir()
	pkgBinDir := filepath.Join(t.TempDir(), "jq", "1.7.1", "bin")
	if err := os.MkdirAll(pkgBinDir, 0o755); err != nil {
		t.Fatalf("failed to create pkg bin dir: %v", err)
	}

	jqBin := filepath.Join(pkgBinDir, "jq")
	if err := os.WriteFile(jqBin, []byte("binary"), 0o755); err != nil {
		t.Fatalf("failed to create executable: %v", err)
	}

	p := NewProfile(binDir)

	if err := p.LinkPackageBinaries(pkgBinDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	linkPath := filepath.Join(binDir, "jq")
	got, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("failed to read symlink: %v", err)
	}
	if got != jqBin {
		t.Errorf("symlink target = %q, want %q", got, jqBin)
	}
}

func TestLinkPackageBinariesMultipleExecutables(t *testing.T) {
	binDir := t.TempDir()
	pkgBinDir := filepath.Join(t.TempDir(), "pkg", "1.0", "bin")
	if err := os.MkdirAll(pkgBinDir, 0o755); err != nil {
		t.Fatalf("failed to create pkg bin dir: %v", err)
	}

	executables := []string{"alpha", "bravo", "charlie"}
	for _, name := range executables {
		path := filepath.Join(pkgBinDir, name)
		if err := os.WriteFile(path, []byte("binary"), 0o755); err != nil {
			t.Fatalf("failed to create executable %q: %v",
				name, err)
		}
	}

	p := NewProfile(binDir)

	if err := p.LinkPackageBinaries(pkgBinDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, name := range executables {
		linkPath := filepath.Join(binDir, name)
		if _, err := os.Lstat(linkPath); err != nil {
			t.Errorf("expected symlink %q to exist: %v",
				name, err)
		}
	}
}

func TestLinkPackageBinariesSkipsNonExecutable(t *testing.T) {
	binDir := t.TempDir()
	pkgBinDir := filepath.Join(t.TempDir(), "pkg", "1.0", "bin")
	if err := os.MkdirAll(pkgBinDir, 0o755); err != nil {
		t.Fatalf("failed to create pkg bin dir: %v", err)
	}

	// Executable file — should be linked.
	execPath := filepath.Join(pkgBinDir, "tool")
	if err := os.WriteFile(execPath, []byte("exec"), 0o755); err != nil {
		t.Fatalf("failed to write executable: %v", err)
	}

	// Non-executable file — should be skipped.
	dataPath := filepath.Join(pkgBinDir, "data.txt")
	if err := os.WriteFile(dataPath, []byte("data"), 0o644); err != nil {
		t.Fatalf("failed to write data file: %v", err)
	}

	p := NewProfile(binDir)

	if err := p.LinkPackageBinaries(pkgBinDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	links, err := p.List()
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("List length = %d, want 1", len(links))
	}
	if links[0].Name != "tool" {
		t.Errorf("linked name = %q, want %q", links[0].Name, "tool")
	}
}

func TestLinkPackageBinariesEmptyDirNoError(t *testing.T) {
	binDir := t.TempDir()
	pkgBinDir := filepath.Join(t.TempDir(), "empty", "1.0", "bin")
	if err := os.MkdirAll(pkgBinDir, 0o755); err != nil {
		t.Fatalf("failed to create pkg bin dir: %v", err)
	}

	p := NewProfile(binDir)

	if err := p.LinkPackageBinaries(pkgBinDir); err != nil {
		t.Fatalf("unexpected error for empty dir: %v", err)
	}

	links, err := p.List()
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(links) != 0 {
		t.Errorf("List length = %d, want 0", len(links))
	}
}
