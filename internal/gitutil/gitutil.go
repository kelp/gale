package gitutil

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Clone shallow-clones a git repo to destDir and returns
// the short commit hash. If ref is empty, clones the
// default branch. The repo param accepts owner/repo
// shorthand or a full URL.
func Clone(repo, destDir, ref string) (string, error) {
	url := RepoURL(repo)
	args := []string{"clone", "--depth=1"}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	args = append(args, url, destDir)

	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git clone %s: %s: %w",
			url, strings.TrimSpace(string(out)), err)
	}

	// Get short hash from the clone.
	hashCmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	hashCmd.Dir = destDir
	hashOut, err := hashCmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w", err)
	}

	return strings.TrimSpace(string(hashOut)), nil
}

// RemoteHead returns the short commit hash of the remote
// HEAD (or ref) without cloning. Useful for checking if
// an update is available.
func RemoteHead(repo, ref string) (string, error) {
	url := RepoURL(repo)
	target := "HEAD"
	if ref != "" {
		target = "refs/heads/" + ref
	}

	cmd := exec.Command("git", "ls-remote", url, target)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git ls-remote %s: %s: %w",
			url, strings.TrimSpace(stderr.String()), err)
	}

	line := strings.TrimSpace(string(out))
	if line == "" {
		return "", fmt.Errorf("no ref %q found at %s", target, url)
	}

	// Output is "hash\tref". Take first 7 chars of hash.
	fullHash := strings.Fields(line)[0]
	if len(fullHash) < 7 {
		return fullHash, nil
	}
	return fullHash[:7], nil
}

// RepoURL expands an owner/repo shorthand to a GitHub
// HTTPS URL. Full URLs are returned unchanged.
func RepoURL(repo string) string {
	if strings.HasPrefix(repo, "https://") ||
		strings.HasPrefix(repo, "http://") ||
		strings.HasPrefix(repo, "git@") ||
		strings.HasPrefix(repo, "/") {
		return repo
	}
	return "https://github.com/" + repo + ".git"
}

// Head returns the full 40-character SHA of dir's HEAD.
func Head(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w", commandErr(ctx, err))
	}
	return strings.TrimSpace(string(out)), nil
}

// Show returns the bytes of path at commit in dir.
// A missing path wraps os.ErrNotExist. A pin that is not in
// the repo is a hard error, not ErrNotExist.
func Show(ctx context.Context, dir, commit, path string) ([]byte, error) {
	if err := objectExists(ctx, dir, commit); err != nil {
		return nil, err
	}
	spec := commit + ":" + path
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "show", spec)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if isGitMissingPath(msg) {
			return nil, fmt.Errorf("git show %s: %w", spec, os.ErrNotExist)
		}
		return nil, fmt.Errorf("git show %s: %s: %w", spec, msg, commandErr(ctx, err))
	}
	return out, nil
}

func objectExists(ctx context.Context, dir, commit string) error {
	spec := commit + "^{commit}"
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "cat-file", "-e", spec)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		return fmt.Errorf("git cat-file %s: %s: %w", spec, msg, commandErr(ctx, err))
	}
	return nil
}

// RemoteTip returns the full 40-character SHA of the remote
// HEAD (or ref) without cloning.
func RemoteTip(ctx context.Context, repo, ref string) (string, error) {
	url := RepoURL(repo)
	target := "HEAD"
	if ref != "" {
		target = "refs/heads/" + ref
	}
	cmd := exec.CommandContext(ctx, "git", "ls-remote", url, target)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git ls-remote %s: %s: %w",
			url, strings.TrimSpace(stderr.String()), commandErr(ctx, err))
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return "", fmt.Errorf("no ref %q found at %s", target, url)
	}
	return strings.Fields(line)[0], nil
}

func commandErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func isGitMissingPath(stderr string) bool {
	return strings.Contains(stderr, "exists on disk, but not in") ||
		strings.Contains(stderr, "path not in") ||
		(strings.Contains(stderr, "path") && strings.Contains(stderr, "does not exist"))
}
