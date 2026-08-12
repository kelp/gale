package generation

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

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

// genNumbers returns the numbers of the generation directories
// under galeDir/gen, sorted ascending. A missing gen dir yields no
// numbers and no error. Entries that are not directories, and
// directories whose name is not a number, are skipped: only a
// numeric directory is a generation.
//
// The single scan every caller shares — List, PruneOldGenerations,
// and Build's allocation of the next number.
func genNumbers(galeDir string) ([]int, error) {
	entries, err := os.ReadDir(filepath.Join(galeDir, "gen"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read gen dir: %w", err)
	}

	var nums []int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // skip non-numeric
		}
		nums = append(nums, n)
	}
	sort.Ints(nums)
	return nums, nil
}

// List returns all generations sorted by number ascending.
func List(galeDir, storeRoot string) ([]GenInfo, error) {
	nums, err := genNumbers(galeDir)
	if err != nil {
		return nil, err
	}

	cur, err := Current(galeDir)
	if err != nil {
		return nil, fmt.Errorf("read current: %w", err)
	}

	genBase := filepath.Join(galeDir, "gen")
	var gens []GenInfo
	for _, n := range nums {
		genDir := filepath.Join(genBase, strconv.Itoa(n))
		gens = append(gens, GenInfo{
			Number:   n,
			Current:  n == cur,
			Packages: genVersions(genDir, storeRoot),
		})
	}

	return gens, nil
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

	fromPkgs := genVersions(fromDir, storeRoot)
	toPkgs := genVersions(toDir, storeRoot)

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

		// Farm guard before the swap, mirroring Build: the
		// rolled-to closure is this scope's proposed claim, and
		// a refusal must leave the current generation active
		// and the farm untouched (design §4).
		pkgs := genVersions(genDir, storeRoot)
		active, err := guardedRebuildDirs(pkgs, galeDir, storeRoot)
		if err != nil {
			return err
		}

		if err := swapCurrentSymlink(galeDir, target); err != nil {
			return err
		}

		// Rebuild the SHARED farm from the rolled-to
		// generation's package set so binaries resolve the
		// dylib revisions they were built against, not the
		// ones the rolled-from generation installed (gh#44),
		// plus every other scope's claimed closure.
		//
		// The shared farm, not this scope's gale dir: pointing
		// it at a project's own lib dir is why gh#44's repair
		// only ever worked at global scope. A project rollback
		// wiped a directory nothing resolves through and left
		// the farm its binaries actually use untouched.
		//
		// Mirrors Build's post-swap rebuild, including the
		// failure semantics: the swap stands, the error is
		// returned.
		if err := farm.Rebuild(
			active, farm.DirFromStoreRoot(storeRoot),
		); err != nil {
			return fmt.Errorf(
				"generation %d is active, but the shared library "+
					"farm is incomplete; run gale sync to repair "+
					"it: %w", target, err,
			)
		}
		return nil
	})
}
