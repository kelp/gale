package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// setupBareRepo creates a bare git repo with one commit
// containing a README file. Returns the repo path.
func setupBareRepo(t *testing.T) string {
	t.Helper()

	// Create a normal repo with a commit.
	workDir := t.TempDir()
	run(t, workDir, "git", "init")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")
	run(t, workDir, "git", "config", "commit.gpgsign", "false")
	if err := os.WriteFile(
		filepath.Join(workDir, "README"),
		[]byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, workDir, "git", "add", "README")
	run(t, workDir, "git", "commit", "-m", "initial")

	// Clone to bare repo for serving.
	bareDir := t.TempDir()
	run(t, "", "git", "clone", "--bare", workDir, bareDir)

	return bareDir
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...) //nolint:gosec
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v failed: %s: %v", args, out, err)
	}
}

// --- Clone tests ---

func TestCloneCreatesDirectory(t *testing.T) {
	repo := setupBareRepo(t)
	destDir := filepath.Join(t.TempDir(), "clone")

	hash, err := Clone(repo, destDir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hash == "" {
		t.Error("expected non-empty hash")
	}

	// Verify README exists in clone.
	if _, err := os.Stat(
		filepath.Join(destDir, "README")); err != nil {
		t.Errorf("README not found in clone: %v", err)
	}
}

func TestCloneReturnsShortHash(t *testing.T) {
	repo := setupBareRepo(t)
	destDir := filepath.Join(t.TempDir(), "clone")

	hash, err := Clone(repo, destDir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hash) < 7 || len(hash) > 12 {
		t.Errorf("hash length %d, want 7-12: %q",
			len(hash), hash)
	}
}

func TestCloneInvalidRepoReturnsError(t *testing.T) {
	destDir := filepath.Join(t.TempDir(), "clone")
	_, err := Clone("/nonexistent/repo", destDir, "")
	if err == nil {
		t.Fatal("expected error for invalid repo")
	}
}

// --- RemoteHead tests ---

func TestRemoteHeadReturnsHash(t *testing.T) {
	repo := setupBareRepo(t)

	hash, err := RemoteHead(repo, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hash) < 7 || len(hash) > 12 {
		t.Errorf("hash length %d, want 7-12: %q",
			len(hash), hash)
	}
}

func TestRemoteHeadMatchesCloneHash(t *testing.T) {
	repo := setupBareRepo(t)

	remoteHash, err := RemoteHead(repo, "")
	if err != nil {
		t.Fatalf("RemoteHead error: %v", err)
	}

	destDir := filepath.Join(t.TempDir(), "clone")
	cloneHash, err := Clone(repo, destDir, "")
	if err != nil {
		t.Fatalf("Clone error: %v", err)
	}

	if remoteHash != cloneHash {
		t.Errorf("RemoteHead %q != Clone %q",
			remoteHash, cloneHash)
	}
}

func TestRemoteHeadInvalidRepoReturnsError(t *testing.T) {
	_, err := RemoteHead("/nonexistent/repo", "")
	if err == nil {
		t.Fatal("expected error for invalid repo")
	}
}

// --- RepoURL tests ---

func TestRepoURLExpandsShorthand(t *testing.T) {
	got := RepoURL("kelp/gale")
	want := "https://github.com/kelp/gale.git"
	if got != want {
		t.Errorf("RepoURL(%q) = %q, want %q",
			"kelp/gale", got, want)
	}
}

func TestRepoURLPassesThroughFullURL(t *testing.T) {
	url := "https://gitlab.com/foo/bar.git"
	got := RepoURL(url)
	if got != url {
		t.Errorf("RepoURL(%q) = %q, want passthrough",
			url, got)
	}
}
