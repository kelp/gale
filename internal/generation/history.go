package generation

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/filelock"
)

// GenInfo describes one generation.
type GenInfo struct {
	Number   int
	Current  bool
	Packages map[string]string // name → version
}

// GenDiff describes differences between two generations.
type GenDiff struct {
	From    int
	To      int
	Added   []string // "name@version"
	Removed []string // "name@version"
}

// List returns all generations sorted by number ascending.
func List(galeDir, storeRoot string) ([]GenInfo, error) {
	genBase := filepath.Join(galeDir, "gen")
	entries, err := os.ReadDir(genBase)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read gen dir: %w", err)
	}

	cur, err := Current(galeDir)
	if err != nil {
		return nil, fmt.Errorf("read current: %w", err)
	}

	var gens []GenInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // skip non-numeric
		}
		genDir := filepath.Join(genBase, e.Name())
		gens = append(gens, GenInfo{
			Number:   n,
			Current:  n == cur,
			Packages: packagesFromGen(genDir, storeRoot),
		})
	}

	sort.Slice(gens, func(i, j int) bool {
		return gens[i].Number < gens[j].Number
	})

	return gens, nil
}

// packagesFromGen resolves symlinks in a generation's bin/
// directory back to the store to determine package names and
// versions. Returns map[name]version.
func packagesFromGen(genDir, storeRoot string) map[string]string {
	binDir := filepath.Join(genDir, "bin")
	entries, err := os.ReadDir(binDir)
	if err != nil {
		return nil
	}

	// Resolve storeRoot through symlinks so relative path
	// computation works on macOS where /var → /private/var.
	absStore, err := filepath.EvalSymlinks(storeRoot)
	if err != nil {
		return nil
	}

	pkgs := make(map[string]string)
	for _, e := range entries {
		linkPath := filepath.Join(binDir, e.Name())
		realPath, err := filepath.EvalSymlinks(linkPath)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(absStore, realPath)
		if err != nil {
			continue
		}
		// rel looks like "name/version/bin/exe".
		parts := strings.SplitN(rel, string(filepath.Separator), 4)
		if len(parts) < 2 {
			continue
		}
		name, version := parts[0], parts[1]
		pkgs[name] = version
	}
	return pkgs
}

// Diff compares two generations and returns the packages
// added and removed between them.
func Diff(galeDir, storeRoot string, from, to int) (*GenDiff, error) {
	genBase := filepath.Join(galeDir, "gen")

	fromDir := filepath.Join(genBase, strconv.Itoa(from))
	if _, err := os.Stat(fromDir); err != nil {
		return nil, fmt.Errorf("generation %d: %w", from, err)
	}
	toDir := filepath.Join(genBase, strconv.Itoa(to))
	if _, err := os.Stat(toDir); err != nil {
		return nil, fmt.Errorf("generation %d: %w", to, err)
	}

	fromPkgs := packagesFromGen(fromDir, storeRoot)
	toPkgs := packagesFromGen(toDir, storeRoot)

	d := &GenDiff{From: from, To: to}

	// Packages in "to" but not "from" → Added.
	// Packages in both but different versions → Added + Removed.
	for name, toVer := range toPkgs {
		fromVer, ok := fromPkgs[name]
		if !ok {
			d.Added = append(d.Added, name+"@"+toVer)
		} else if fromVer != toVer {
			d.Added = append(d.Added, name+"@"+toVer)
			d.Removed = append(d.Removed, name+"@"+fromVer)
		}
	}

	// Packages in "from" but not "to" → Removed.
	for name, fromVer := range fromPkgs {
		if _, ok := toPkgs[name]; !ok {
			d.Removed = append(d.Removed, name+"@"+fromVer)
		}
	}

	sort.Strings(d.Added)
	sort.Strings(d.Removed)

	return d, nil
}

// Rollback atomically swaps the current symlink to point
// at the given generation number and rebuilds the shared
// dylib farm from that generation's package set. Acquires
// the generation lock so it serializes with Build and
// PruneOldGenerations.
func Rollback(galeDir, storeRoot string, target int) error {
	genDir := filepath.Join(
		galeDir, "gen", strconv.Itoa(target),
	)

	lockPath := filepath.Join(filepath.Dir(storeRoot), "generation.lock")
	return filelock.With(lockPath, func() error {
		// The existence check must run under the lock: a
		// concurrent Build's auto-prune can delete the
		// target gen while Rollback waits for the lock, and
		// checking outside would let the swap land a
		// dangling current symlink while reporting success
		// (gh#45).
		if _, err := os.Stat(genDir); err != nil {
			return fmt.Errorf("generation %d does not exist: %w",
				target, err)
		}
		if err := swapCurrentSymlink(galeDir, target); err != nil {
			return err
		}

		// Rebuild the farm from the rolled-to generation's
		// package set so binaries resolve the dylib
		// revisions they were built against, not the ones
		// the rolled-from generation installed (gh#44).
		// Mirrors Build's post-swap farm rebuild.
		// Best-effort — a farm error does not invalidate
		// the swap.
		pkgs := packagesFromGen(genDir, storeRoot)
		if err := farm.Rebuild(
			FarmStoreDirs(pkgs, storeRoot), farm.Dir(galeDir),
		); err != nil {
			fmt.Fprintf(os.Stderr,
				"farm: rebuild after rollback: %v\n", err)
		}
		return nil
	})
}
