package gitutil

import (
	"fmt"
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
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git ls-remote %s: %w", url, err)
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
