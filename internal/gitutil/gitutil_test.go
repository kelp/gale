package gitutil

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
		[]byte("hello"), 0o644,
	); err != nil {
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
		filepath.Join(destDir, "README"),
	); err != nil {
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

// TestRemoteHeadErrorIncludesStderr verifies that when git
// ls-remote fails, the error message includes git's stderr
// text rather than just the bare exit code. A network or
// repo-not-found failure should surface a useful diagnostic.
func TestRemoteHeadErrorIncludesStderr(t *testing.T) {
	// /nonexistent/repo is fast-local, guaranteed to fail
	// with a git error printed to stderr.
	_, err := RemoteHead("/nonexistent/repo", "")
	if err == nil {
		t.Fatal("expected error")
	}
	// git writes diagnostic text to stderr; the error
	// message should contain more than a bare exit code.
	msg := err.Error()
	if msg == "git ls-remote /nonexistent/repo: exit status 128" {
		t.Errorf(
			"error is bare exit code (stderr not captured): %q",
			msg,
		)
	}
}

// TestRemoteHeadStdoutOnlyOnSuccess verifies that stderr
// from git ls-remote is NOT mixed into the parsed stdout.
// git can write warnings to stderr even on success; using
// CombinedOutput would break hash parsing in that case.
func TestRemoteHeadStdoutOnlyOnSuccess(t *testing.T) {
	// A real bare repo guarantees a clean stdout hash.
	repo := setupBareRepo(t)
	hash, err := RemoteHead(repo, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The hash must be a pure hex string, not mixed with
	// any stderr text (e.g. "warning: …\nabc1234").
	for _, c := range hash {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Errorf(
				"hash %q contains non-hex char %q "+
					"(stderr may have been mixed in)",
				hash, c,
			)
		}
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

func setupWorkRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "git", "init")
	run(t, dir, "git", "config", "user.email", "test@test.com")
	run(t, dir, "git", "config", "user.name", "Test")
	run(t, dir, "git", "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "git", "add", "README")
	run(t, dir, "git", "commit", "-m", "initial")
	return dir
}

func fullHead(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestHeadReturnsFullHash(t *testing.T) {
	dir := setupWorkRepo(t)
	want := fullHead(t, dir)
	got, err := Head(t.Context(), dir)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if got != want {
		t.Errorf("Head = %q, want %q", got, want)
	}
	if len(got) != 40 {
		t.Errorf("Head length %d, want 40: %q", len(got), got)
	}
	short, err := RemoteHead(dir, "")
	if err != nil {
		t.Fatalf("RemoteHead: %v", err)
	}
	if got == short {
		t.Errorf("Head must not be RemoteHead's short hash %q", short)
	}
}

func TestHeadCanceled(t *testing.T) {
	dir := setupWorkRepo(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := Head(ctx, dir)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Head canceled: %v, want context.Canceled", err)
	}
}

func TestShowReadsPinnedPath(t *testing.T) {
	dir := setupWorkRepo(t)
	path := filepath.Join("index", "j", "jq.toml")
	if err := os.MkdirAll(filepath.Join(dir, "index", "j"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, path), []byte("committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "git", "add", path)
	run(t, dir, "git", "commit", "-m", "index")
	commit := fullHead(t, dir)
	if err := os.WriteFile(filepath.Join(dir, path), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Show(t.Context(), dir, commit, path)
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if string(got) != "committed\n" {
		t.Errorf("Show = %q, want committed bytes", got)
	}
}

func TestShowMissingIsErrNotExist(t *testing.T) {
	dir := setupWorkRepo(t)
	commit := fullHead(t, dir)
	_, err := Show(t.Context(), dir, commit, "index/j/jq.toml")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Show missing: %v, want os.ErrNotExist", err)
	}
}

func TestShowUnknownCommitIsNotErrNotExist(t *testing.T) {
	dir := setupWorkRepo(t)
	missing := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	_, err := Show(t.Context(), dir, missing, "README")
	if err == nil {
		t.Fatal("Show unknown commit: want error")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unknown commit must not look like a missing path: %v", err)
	}
}

func TestShowCanceled(t *testing.T) {
	dir := setupWorkRepo(t)
	commit := fullHead(t, dir)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := Show(ctx, dir, commit, "README")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Show canceled: %v, want context.Canceled", err)
	}
}

func TestRemoteTipReturnsFullHash(t *testing.T) {
	bare := setupBareRepo(t)
	got, err := RemoteTip(t.Context(), bare, "")
	if err != nil {
		t.Fatalf("RemoteTip: %v", err)
	}
	if len(got) != 40 {
		t.Errorf("RemoteTip length %d, want 40: %q", len(got), got)
	}
	short, err := RemoteHead(bare, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, short) {
		t.Errorf("RemoteTip %q should start with RemoteHead %q", got, short)
	}
}

func TestRemoteTipCanceled(t *testing.T) {
	bare := setupBareRepo(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := RemoteTip(ctx, bare, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RemoteTip canceled: %v, want context.Canceled", err)
	}
}
