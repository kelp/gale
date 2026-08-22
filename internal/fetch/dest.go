package fetch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/kelp/gale/internal/provenance"
)

func publish(ctx context.Context, dest, treeDir, want string) error {
	fi, err := os.Lstat(dest)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.Rename(treeDir, dest); err != nil {
			return fmt.Errorf("rename into store: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("stat dest: %w", err)
	case fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir():
		return fmt.Errorf("%w: dest is not a directory", errOccupied)
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		return fmt.Errorf("read dest: %w", err)
	}
	if len(entries) == 0 {
		return replaceEmpty(dest, treeDir)
	}
	got, err := provenance.DigestTree(ctx, dest)
	if err != nil {
		return fmt.Errorf("%w: %w", errOccupied, err)
	}
	if got != want {
		return fmt.Errorf("%w: tree digest is %s, want %s", errOccupied, got, want)
	}
	return nil
}

func replaceEmpty(dest, treeDir string) error {
	entries, err := os.ReadDir(dest)
	if err != nil {
		return fmt.Errorf("recheck dest: %w", err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("%w: dest grew before rename", errOccupied)
	}
	if err := os.Remove(dest); err != nil {
		return fmt.Errorf("remove empty dest: %w", err)
	}
	if err := os.Rename(treeDir, dest); err != nil {
		return fmt.Errorf("rename into store: %w", err)
	}
	return nil
}
