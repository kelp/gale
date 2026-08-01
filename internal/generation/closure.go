package generation

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kelp/gale/internal/depsmeta"
)

// AuthoritativeClosure returns the store directories reachable from
// roots, and whether the walk could see all of them.
//
// roots are exact store directories, from
// AuthoritativeGenerationDirs, never a name-and-version set the walk
// re-resolves: re-resolution picks the canonical sibling, so a scope
// loading a pre-revision bare directory would be reported as
// protecting a different one.
//
// It exists for decisions that destroy bytes (design §13's migration
// veto). Those need a scope's references even where its lock cannot
// state them — a legacy lock records roots only, so a transitive
// dependency carries no hash, and the only remaining record of the
// reference is the generation's links plus each directory's own
// dependency metadata.
//
// The completeness flag is the point, and it is why FarmStoreDirs
// cannot serve here. That walk reads through depsmeta.Read, which
// decodes a missing file to an empty Metadata, so a package with
// dependencies and no recorded metadata reads as a leaf and its
// dependencies look unreferenced — the walk reports success while
// having seen none of them. Here an absent, unreadable, non-regular
// or malformed file marks the closure unknown, and a caller deciding
// whether to delete something treats unknown as a refusal.
//
// Directories already known are still returned when the walk is
// incomplete: what is missing is everything BEYOND the unreadable
// node, not the node itself.
func AuthoritativeClosure(
	roots []string, storeRoot string,
) (map[string]bool, bool) {
	// One spelling throughout, matching AuthoritativeGenerationDirs.
	// A caller comparing a directory it resolved itself against these
	// must not miss a match because of macOS /var versus /private/var.
	absStore := storeRoot
	if resolved, err := filepath.EvalSymlinks(storeRoot); err == nil {
		absStore = resolved
	}
	queue := make([]string, 0, len(roots))
	for _, r := range roots {
		queue = append(queue, canonicalDir(r))
	}
	seen := make(map[string]bool, len(queue))
	for _, d := range queue {
		seen[d] = true
	}

	out := make(map[string]bool, len(queue))
	complete := true
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		if _, err := os.Stat(dir); err != nil {
			if os.IsNotExist(err) {
				// Nothing there to protect. Distinct from the case
				// below: absence is an answer, while a stat that fails
				// for any other reason is not.
				continue
			}
			complete = false
			continue
		}
		out[dir] = true

		deps, state := depsmeta.ReadStrict(dir)
		if state != depsmeta.StateRecorded {
			complete = false
			continue
		}
		for _, dep := range deps {
			// By bare version, matching how the farm resolves: the
			// pin holds SONAME identity while the revision floats to
			// whatever is installed (gh#172).
			depDir := canonicalDir(
				resolveStoreDir(absStore, dep.Name, dep.Version),
			)
			if !seen[depDir] {
				seen[depDir] = true
				queue = append(queue, depDir)
			}
		}
	}
	return out, complete
}

// AuthoritativeGenerationDirs returns the store directories a scope's
// active generation links, exactly as linked.
//
// Two differences from CurrentVersions, both of which matter only to a
// caller deciding whether to destroy bytes.
//
// It returns the linked directory rather than a name-and-version pair
// that the caller re-resolves. Re-resolution picks the canonical
// sibling, so a generation linking a pre-revision bare dir while
// "1.7-1" also exists would be reported as protecting "1.7-1" while
// its binaries load "1.7" — and that shape is precisely upgrade day,
// which is when this scan runs.
//
// It fails rather than returning what it managed to read. genVersions
// is a deliberately best-effort walk: it swallows walk errors,
// unreadable links and store stat failures, which is right for
// rebuilding a generation and wrong here, because "I could not read
// this scope" would arrive as "this scope references nothing".
//
// An absent current symlink is still an empty scope, not a failure: a
// registered project that has never synced genuinely references
// nothing.
func AuthoritativeGenerationDirs(galeDir, storeRoot string) ([]string, error) {
	// The link target, not a path rebuilt from its number. Current
	// parses the basename as an integer, so a current pointing at
	// "alternate/1" yields 1 and reconstructing "gen/1" would inspect a
	// directory the scope is not using.
	curPath := filepath.Join(galeDir, "current")
	target, err := os.Readlink(curPath)
	if err != nil {
		if os.IsNotExist(err) {
			// A scope that has never synced references nothing.
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", curPath, err)
	}
	genDir := target
	if !filepath.IsAbs(genDir) {
		genDir = filepath.Join(galeDir, target)
	}
	if _, err := os.Stat(genDir); err != nil {
		return nil, fmt.Errorf(
			"the active generation %s is unreadable: %w", genDir, err,
		)
	}

	// One spelling for every path returned, so a caller comparing
	// against a store directory it resolved itself cannot miss a match
	// because of macOS /var versus /private/var.
	absStore := storeRoot
	if resolved, rerr := filepath.EvalSymlinks(storeRoot); rerr == nil {
		absStore = resolved
	}

	seen := map[string]bool{}
	var out []string
	walkErr := filepath.Walk(genDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return fmt.Errorf("walking %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return nil
		}
		linkTarget, readErr := os.Readlink(path)
		if readErr != nil {
			return fmt.Errorf("reading link %s: %w", path, readErr)
		}
		if !filepath.IsAbs(linkTarget) {
			linkTarget = filepath.Join(filepath.Dir(path), linkTarget)
		}
		// Resolve the link's own directory prefix so a target spelled
		// through /var matches a store root spelled through
		// /private/var, without resolving the leaf: the leaf may be
		// absent while its store directory still exists.
		linkDir := linkTarget
		if resolved, rerr := filepath.EvalSymlinks(filepath.Dir(linkTarget)); rerr == nil {
			linkDir = filepath.Join(resolved, filepath.Base(linkTarget))
		}
		rel := relWithinStore(absStore, linkDir)
		if rel == "" {
			rel = relWithinStore(absStore, linkTarget)
		}
		if rel == "" {
			return nil // outside the store; not ours to protect
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) < 2 {
			return nil
		}
		// Joined from the link text, never re-resolved by version.
		dir := filepath.Join(absStore, parts[0], parts[1])
		if !seen[dir] {
			seen[dir] = true
			out = append(out, dir)
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.Strings(out)
	return out, nil
}

// canonicalDir resolves a store directory's spelling without touching
// its version. EvalSymlinks fails on a path that does not exist, and
// the raw spelling is the right answer then: the directory is absent
// either way, and the caller's absence branch handles it.
func canonicalDir(dir string) string {
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return resolved
	}
	return dir
}
