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

	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/index"
)

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
		if d.IsDir() {
			return fmt.Errorf("%s is a directory", src)
		}
		matches = append(matches, p)
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
