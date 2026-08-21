package generation

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// FileCollision records one generation-relative path that more than
// one package provides. Existing is the package whose copy the
// generation links; Incoming is the one it shadows.
type FileCollision struct {
	Path     string
	Existing string
	Incoming string
}

// shadowReportDirs lists the store directories whose contents
// ShadowedFiles reports on, as generation-relative paths.
//
// The set is deliberately narrow. share/doc, share/licenses,
// include/ and libexec/ collide constantly and inertly, and
// reporting them is the noise that trains people to ignore the
// report. lib/ is not here either: the farm claims versioned sonames
// and refuses a rebuild on its own conflict, and it farms an
// unversioned alias for neither provider, so arbitrating gen lib/
// would contradict that rule rather than add to it.
//
// Adding a directory is one line here plus a test.
var shadowReportDirs = []string{
	"man",
	filepath.Join("share", "man"),
}

// ShadowedFiles reports the man pages and root-level files that more
// than one of pkgs provides. Unlike bin/, none of these refuse a
// rebuild: two packages shipping man/man1/foo.1 is expected — a
// library and its CLI, a compat shim — and the loser shows the wrong
// docs rather than running the wrong program. Root-level files are
// live rather than inert (Go reads current/go.env through the
// generation symlink), but the realistic collisions are generic names
// whose effect is a wrong string. Both are reported by `gale doctor`,
// never refused (gh#219).
//
// The enumeration mirrors populateGeneration: packages in sorted name
// order, so the package named Existing is the one a rebuild links,
// and the whole tree under each reported directory, since a rebuild
// recurses into it without arbitrating any level. The verdict comes
// from BinArbiter, the arbiter the rebuild decides bin/ by: its key
// is any string and its bookkeeping is directory-agnostic, so a
// report built on it cannot drift from the rule.
//
// A package with no store dir contributes nothing, matching the
// rebuild, which skips it with a warning rather than failing (gh#68).
// Results are sorted by path.
func ShadowedFiles(pkgs map[string]string, storeRoot string) []FileCollision {
	files := NewBinArbiter()
	for _, name := range slices.Sorted(maps.Keys(pkgs)) {
		pkgDir := resolveStoreDir(storeRoot, name, pkgs[name])
		claimRootFiles(files, name, pkgDir)
		for _, dir := range shadowReportDirs {
			if skipTopLevelDirs[topLevelDir(dir)] {
				continue
			}
			claimTree(files, name, pkgDir, dir)
		}
	}

	claimed := files.Collisions()
	if len(claimed) == 0 {
		return nil
	}
	out := make([]FileCollision, 0, len(claimed))
	for _, c := range claimed {
		out = append(out, FileCollision{
			Path:     c.Bin,
			Existing: c.Existing,
			Incoming: c.Incoming,
		})
	}
	return out
}

// claimRootFiles offers pkg's root-level regular files, the entries
// linkStoreEntries mirrors into the generation root. It skips
// directories for the same reason that function does: a directory is
// merged entry by entry, not claimed whole.
func claimRootFiles(files *BinArbiter, pkg, pkgDir string) {
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		files.Claim(pkg, e.Name())
	}
}

// claimTree offers every file under one of pkg's reported
// directories, keyed by its generation-relative path. It recurses the
// way symlinkDir does, and for the same reason: a rebuild mirrors
// man/man1/foo.1 as a leaf under a directory it created, so the leaf
// path is the thing two packages contest.
//
// An absent directory is the ordinary case — most packages ship no
// man pages — and contributes nothing.
func claimTree(files *BinArbiter, pkg, pkgDir, rel string) {
	entries, err := os.ReadDir(filepath.Join(pkgDir, rel))
	if err != nil {
		return
	}
	for _, e := range entries {
		child := filepath.Join(rel, e.Name())
		if e.IsDir() {
			claimTree(files, pkg, pkgDir, child)
			continue
		}
		files.Claim(pkg, child)
	}
}

// topLevelDir returns rel's first path component, the name
// populateGeneration tests against skipTopLevelDirs. A reported
// directory whose top level is skipped is never mirrored, so a
// collision under it cannot exist.
func topLevelDir(rel string) string {
	if i := strings.IndexRune(rel, filepath.Separator); i >= 0 {
		return rel[:i]
	}
	return rel
}
