package fetch

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/index"
)

// PlaceMapped extracts archive and copies art.Files into treeDir.
// A directory src is copied when dest mode is 0755; empty
// directories are refused. archive must sit in a fresh work
// directory: extract lands at dirname(archive)/extract and
// is not cleared.
func PlaceMapped(ctx context.Context, archive, treeDir string, art index.Artifact) error {
	return placeMapped(ctx, archive, treeDir, art)
}

func placeMapped(ctx context.Context, archive, treeDir string, art index.Artifact) error {
	extractDir := filepath.Join(filepath.Dir(archive), "extract")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return fmt.Errorf("create extract dir: %w", err)
	}
	if err := extractSource(ctx, archive, extractDir, art); err != nil {
		return err
	}
	for i, fe := range art.Files {
		src, err := resolveSrc(extractDir, fe.Src, art.Strip)
		if err != nil {
			return fmt.Errorf("files[%d].src: %w", i, err)
		}
		dest := filepath.Join(treeDir, filepath.FromSlash(fe.Dest))
		mode, err := fileMode(fe.Mode)
		if err != nil {
			return fmt.Errorf("files[%d].mode: %w", i, err)
		}
		if err := copyMapped(ctx, src, dest, mode); err != nil {
			return fmt.Errorf("place %s: %w", fe.Dest, err)
		}
	}
	return nil
}

func extractSource(ctx context.Context, archive, extractDir string, art index.Artifact) error {
	if art.Format != "binary" {
		return download.ExtractArtifact(ctx, archive, extractDir, art.Format)
	}
	u, err := url.Parse(art.URL)
	if err != nil {
		return fmt.Errorf("parse URL: %w", err)
	}
	base := path.Base(u.Path)
	if base == "" || base == "." || base == "/" {
		return fmt.Errorf("binary url has no basename")
	}
	return copyMapped(ctx, archive, filepath.Join(extractDir, base), 0o644)
}

func resolveSrc(extractDir, src string, strip int) (string, error) {
	var matches []string
	err := filepath.WalkDir(extractDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == extractDir {
			return nil
		}
		rel, err := filepath.Rel(extractDir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		stripped, ok := stripPrefix(rel, strip)
		if !ok || stripped != src {
			return nil
		}
		matches = append(matches, p)
		if d.IsDir() {
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("%s: not found after strip %d", src, strip)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("%s: %d extract paths collapse after strip", src, len(matches))
	}
}

func stripPrefix(rel string, strip int) (string, bool) {
	parts := strings.Split(rel, "/")
	if strip < 0 || len(parts) <= strip {
		return "", false
	}
	return strings.Join(parts[strip:], "/"), true
}

func copyMapped(ctx context.Context, src, dest string, mode os.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlink")
	}
	if fi.IsDir() {
		return copyMappedDir(ctx, src, dest, mode)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("not a regular file")
	}
	if err := checkMappedFile(src, fi); err != nil {
		return err
	}
	return copyMappedFile(ctx, src, dest, mode)
}

func copyMappedDir(ctx context.Context, src, dest string, mode os.FileMode) error {
	if mode != 0o755 {
		return fmt.Errorf("directory dest mode must be 0755")
	}
	var files int
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		out := dest
		if rel != "." {
			out = filepath.Join(dest, rel)
		}
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s: symlink", rel)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s: not a regular file", rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if err := checkMappedFile(rel, info); err != nil {
			return err
		}
		files++
		return copyMappedFile(ctx, p, out, info.Mode().Perm())
	})
	if err != nil {
		return err
	}
	if files == 0 {
		return fmt.Errorf("directory has no regular files")
	}
	return nil
}

func checkMappedFile(rel string, fi os.FileInfo) error {
	special := os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	if fi.Mode()&special != 0 {
		return fmt.Errorf("%s: setuid, setgid, or sticky", rel)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s: missing stat_t", rel)
	}
	if mappedNlink(st.Nlink) != 1 {
		return fmt.Errorf("%s: hardlink", rel)
	}
	return nil
}

func copyMappedFile(ctx context.Context, src, dest string, mode os.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dest)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dest)
		return err
	}
	return os.Chmod(dest, mode)
}

func mappedNlink[T ~uint16 | ~uint64](n T) uint64 {
	return uint64(n)
}
