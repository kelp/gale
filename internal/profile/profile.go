package profile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Link represents a symlink in the profile.
type Link struct {
	Name   string // symlink name (binary name)
	Target string // absolute path the symlink points to
}

// Profile manages symlinks in a bin directory.
type Profile struct {
	BinDir string
}

// NewProfile creates a Profile for the given bin directory.
func NewProfile(binDir string) *Profile {
	return &Profile{BinDir: binDir}
}

// Link creates a symlink named name pointing to target.
func (p *Profile) Link(name, target string) error {
	if err := os.MkdirAll(p.BinDir, 0o755); err != nil {
		return fmt.Errorf("create bin directory: %w", err)
	}
	linkPath := filepath.Join(p.BinDir, name)
	if err := os.Symlink(target, linkPath); err != nil {
		return fmt.Errorf("create symlink %s: %w", name, err)
	}
	return nil
}

// Update replaces an existing symlink with a new target.
func (p *Profile) Update(name, target string) error {
	linkPath := filepath.Join(p.BinDir, name)
	info, err := os.Lstat(linkPath)
	if err != nil {
		return fmt.Errorf("update symlink %s: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("update symlink %s: not a symlink", name)
	}
	if err := os.Remove(linkPath); err != nil {
		return fmt.Errorf("remove old symlink %s: %w", name, err)
	}
	if err := os.Symlink(target, linkPath); err != nil {
		return fmt.Errorf("create new symlink %s: %w", name, err)
	}
	return nil
}

// Remove removes a symlink by name.
func (p *Profile) Remove(name string) error {
	linkPath := filepath.Join(p.BinDir, name)
	info, err := os.Lstat(linkPath)
	if err != nil {
		return fmt.Errorf("remove symlink %s: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("remove symlink %s: not a symlink", name)
	}
	if err := os.Remove(linkPath); err != nil {
		return fmt.Errorf("remove symlink %s: %w", name, err)
	}
	return nil
}

// List returns all symlinks in the profile.
func (p *Profile) List() ([]Link, error) {
	entries, err := os.ReadDir(p.BinDir)
	if err != nil {
		return nil, fmt.Errorf("read bin directory: %w", err)
	}

	var links []Link
	for _, entry := range entries {
		linkPath := filepath.Join(p.BinDir, entry.Name())
		info, err := os.Lstat(linkPath)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", entry.Name(), err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		target, err := os.Readlink(linkPath)
		if err != nil {
			return nil, fmt.Errorf("read symlink %s: %w",
				entry.Name(), err)
		}
		links = append(links, Link{
			Name:   entry.Name(),
			Target: target,
		})
	}
	return links, nil
}

// LinkPackageBinaries creates symlinks for all executables
// in the given package bin directory.
func (p *Profile) LinkPackageBinaries(pkgBinDir string) error {
	entries, err := os.ReadDir(pkgBinDir)
	if err != nil {
		return fmt.Errorf("read package bin directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", entry.Name(), err)
		}
		if info.Mode()&0o111 == 0 {
			continue
		}
		target := filepath.Join(pkgBinDir, entry.Name())
		linkPath := filepath.Join(p.BinDir, entry.Name())
		// Remove existing symlink if present (upgrade).
		os.Remove(linkPath)
		if err := p.Link(entry.Name(), target); err != nil {
			return fmt.Errorf("link package binary %s: %w",
				entry.Name(), err)
		}
	}
	return nil
}
