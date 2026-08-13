package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kelp/gale/internal/filelock"
)

// ErrNotInstalled is returned when removing a package that does not exist.
var ErrNotInstalled = errors.New("package not installed")

// InstalledPackage represents a package in the store.
type InstalledPackage struct {
	Name    string
	Version string
}

// Store manages the package store directory.
type Store struct {
	Root string
}

// NewStore creates a Store with the given root directory.
func NewStore(root string) *Store {
	return &Store{Root: root}
}

// isTransientStoreEntry reports whether a sibling under
// <root>/<name>/ is a transient artifact of a forced reinstall
// rather than a real installed version. ".build-*" staging dirs,
// "<version>.bak" backups, and "<version>.stream" streaming-extract
// staging dirs can appear briefly while commitStaged /
// replaceStoreDir / FetchAndExtractTarZstd runs, and non-locking
// readers must skip them.
func isTransientStoreEntry(name string) bool {
	return strings.HasPrefix(name, ".build-") ||
		strings.HasSuffix(name, ".bak") ||
		strings.HasSuffix(name, ".stream")
}

// Create creates the directory for a package version.
// It is idempotent; calling it for an existing version succeeds.
// Returns the full path to the version directory.
func (s *Store) Create(name, version string) (string, error) {
	dir := filepath.Join(s.Root, name, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create store directory: %w", err)
	}
	return dir, nil
}

// StorePath returns the absolute path to the store dir for
// (name, version) if one exists, with fallback to the bare
// version dir for "-1" suffixed requests. Returns ("", false)
// if no directory exists.
func (s *Store) StorePath(name, version string) (string, bool) {
	resolved, ok := s.resolveVersion(name, version)
	if !ok {
		return "", false
	}
	return filepath.Join(s.Root, name, resolved), true
}

// resolveVersion returns the actual version directory name to use for
// name+version, along with whether that directory exists.
//
// Resolution order:
//  1. A bare version (no "-") returns the highest "<v>-<N>" on disk.
//     This matches the CLAUDE.md contract "a bare @version resolves
//     to the highest revision known", so a recipe's revision bump
//     starts flowing through config/lookup without needing users to
//     re-pin revision numbers in gale.toml.
//  2. Exact match on the requested version.
//  3. A "<v>-1" suffix falls back to a bare "<v>" dir — that's where
//     pre-revision installs live.
//  4. A bare version falls back to the bare dir itself (legacy
//     pre-revision installs) if no "<v>-<N>" dirs exist.
//
// Implementation: a single os.ReadDir on <root>/<name>/ answers every
// question above from one atomic directory listing. The previous
// implementation chained up to three os.Stat calls plus a ReadDir,
// which raced with a concurrent `gale gc` removing a sibling
// mid-chain (M2 in TODO.md).
func (s *Store) resolveVersion(name, version string) (string, bool) {
	nameDir := filepath.Join(s.Root, name)
	entries, err := os.ReadDir(nameDir)
	if err != nil {
		return version, false
	}
	return resolveVersionFromEntries(nameDir, entries, version)
}

// ResolveDir returns the on-disk store dir that (name, version)
// resolves to, applying the resolution order documented on
// resolveVersion. When nothing on disk matches, the literal
// <root>/<name>/<version> join is returned so callers can Stat
// it and report the missing path. This is the canonical resolver
// shared by internal/generation and cmd/gale — do not duplicate
// the resolution rules elsewhere.
func (s *Store) ResolveDir(name, version string) string {
	resolved, _ := s.resolveVersion(name, version)
	return filepath.Join(s.Root, name, resolved)
}

// resolveVersionFromEntries applies the resolution order documented
// on resolveVersion to a directory listing of <root>/<name>/.
// Broken out so callers that already hold the listing (e.g. future
// batch lookups) can reuse the logic without a redundant syscall.
func resolveVersionFromEntries(nameDir string, entries []os.DirEntry, version string) (string, bool) {
	// "Bare" means no trailing "-<digits>" revision suffix. A
	// dash from a semver pre-release/dev tag like "0.16.2-dev.70+sha"
	// still counts as bare; the revision goes on the END.
	hasNumericRev := HasNumericRevisionSuffix(version)
	prefix := version + "-"

	hasExact := false
	hasBareForDashOne := false
	var bare string
	if strings.HasSuffix(version, "-1") {
		bare = strings.TrimSuffix(version, "-1")
	}

	type revDir struct {
		rev  int
		name string
	}
	var revDirs []revDir

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n := e.Name()
		if isTransientStoreEntry(n) {
			continue
		}
		if n == version {
			hasExact = true
		}
		if bare != "" && n == bare {
			hasBareForDashOne = true
		}
		if !hasNumericRev && strings.HasPrefix(n, prefix) {
			rev, err := strconv.Atoi(n[len(prefix):])
			if err == nil && rev >= 0 {
				revDirs = append(revDirs, revDir{rev: rev, name: n})
			}
		}
	}

	// Bare request: highest POPULATED "<v>-<N>" wins over bare
	// "<v>". An empty revision dir is an in-flight install
	// (Store.Create pre-creates the canonical dir before the
	// download runs) or debris from a killed install; resolving
	// to it would emit zero generation symlinks and silently
	// drop the package from PATH (gh#76).
	if !hasNumericRev && len(revDirs) > 0 {
		sort.Slice(revDirs, func(i, j int) bool {
			return revDirs[i].rev > revDirs[j].rev
		})
		for _, c := range revDirs {
			if dirHasEntries(filepath.Join(nameDir, c.name)) {
				return c.name, true
			}
		}
		// Every revision dir is empty: prefer a populated bare
		// "<v>" (legacy pre-revision install), else keep the old
		// highest-revision answer so existence semantics for a
		// fresh in-flight install are unchanged (IsInstalled
		// still reports false via its own emptiness check).
		if hasExact && dirHasEntries(filepath.Join(nameDir, version)) {
			return version, true
		}
		return revDirs[0].name, true
	}
	// Exact match.
	if hasExact {
		return version, true
	}
	// "-1" fallback to bare "<v>".
	if hasBareForDashOne {
		return bare, true
	}
	return version, false
}

// dirHasEntries reports whether dir exists and contains at
// least one entry. Used to distinguish a real install from an
// empty in-flight store dir.
func dirHasEntries(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

// HasNumericRevisionSuffix reports whether version ends in a
// "-<digits>" revision suffix. Distinguishes a bare semver-with-
// dev-tag like "0.16.2-dev.70+676b646" (no revision) from an
// explicit pinned form like "0.16.2-dev.70+676b646-1" (revision 1).
// resolveVersionFromEntries uses this to decide whether to scan
// for the highest revision on disk (bare → scan) or treat the
// version as an exact-match request (numeric suffix → exact).
// This is the canonical home for the classification —
// internal/generation, internal/registry, and cmd/gale route
// through it instead of keeping local copies.
func HasNumericRevisionSuffix(version string) bool {
	i := strings.LastIndex(version, "-")
	if i < 0 || i == len(version)-1 {
		return false
	}
	for _, r := range version[i+1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ErrNotCanonical reports an identity that is not spelled as a
// canonical version-revision, or that does not address exactly one
// store directory.
var ErrNotCanonical = errors.New("identity is not canonical")

// CheckIdentity rejects an identity that is not spelled
// name@<version>-<revision>, and that does not address exactly one
// store directory.
//
// It lives here because that is what the rule is about: every caller
// validating an identity is protecting a join onto the store root,
// and HasNumericRevisionSuffix, the revision classifier it rests on,
// is already this package's. Three packages consume it — provenance
// screening dependency edges read off disk, the installer screening
// an archive's metadata, and lockplan screening a lockfile before a
// local --recipes directory joins a name onto a path.
//
// The path-component rule is a boundary, not a style check. These
// identities arrive from files gale did not write, so a dependency
// named "../../outside" would otherwise resolve and be read from
// anywhere the process can reach.
func CheckIdentity(name, version string) error {
	if strings.Contains(name, "@") {
		return fmt.Errorf("%w: name %q contains @", ErrNotCanonical, name)
	}
	if !safeComponent(name) {
		return fmt.Errorf(
			"%w: name %q is not a single path component",
			ErrNotCanonical, name,
		)
	}
	if !safeComponent(version) {
		return fmt.Errorf(
			"%w: version %q is not a single path component",
			ErrNotCanonical, version,
		)
	}
	if !HasNumericRevisionSuffix(version) {
		return fmt.Errorf(
			"%w: %s@%s has no revision suffix", ErrNotCanonical, name, version,
		)
	}
	return nil
}

// SafeComponent reports whether s addresses exactly one directory
// entry. Exported for readers that validate a path component without
// a full canonical identity to hand — dependency metadata records a
// bare version and its revision separately, and those values are
// joined onto the store root, so a value that is not one component
// escapes the store.
func SafeComponent(s string) bool { return safeComponent(s) }

// safeComponent reports whether s addresses exactly one directory
// entry: non-empty, no separator, and not a traversal element.
func safeComponent(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	if strings.ContainsAny(s, `/\`) {
		return false
	}
	// Belt and braces against anything Clean would rewrite, so the
	// string that is validated is the string that is joined.
	return filepath.Clean(s) == s
}

// SplitRevision splits a "<base>-<N>" version into (base, N).
// A version without a numeric revision suffix is returned
// unchanged with revision 1 — the recipe default (an absent
// revision means 1, see recipe.Package.Full).
func SplitRevision(version string) (string, int) {
	if !HasNumericRevisionSuffix(version) {
		return version, 1
	}
	i := strings.LastIndex(version, "-")
	n, err := strconv.Atoi(version[i+1:])
	if err != nil {
		return version, 1
	}
	return version[:i], n
}

// contentTagMarker is the dot-segment that introduces a content
// tag inside a version's semver build-metadata segment.
const contentTagMarker = "h"

// contentDigestLen is the length of a content tag's digest, in
// hex characters. It matches dirtLen in cmd/gale/install.go so
// the two abbreviated digests a store path can carry read alike.
const contentDigestLen = 12

// TagVersion spells the content-tagged sibling of a canonical
// version-revision: a terminal "h.<12 lowercase hex>" dot-segment
// at the end of the semver build-metadata segment, immediately
// preceding the "-<revision>" suffix.
//
// The digest rides in the BUILD-METADATA segment, never in a
// "-<hex>" suffix, for the reason devVersionWithDirt states:
// HasNumericRevisionSuffix and SplitRevision read a trailing
// "-<digits>" as a Debian revision, so an all-digit digest after
// a dash would parse as revision 12345678.
//
// The tag composes with an existing metadata segment rather than
// opening a second one, mirroring devVersionWithDirt exactly. A
// dirty dev build's sibling is therefore spelled
// "0.2.0-dev.7+5395b8f.dirty.<hex>.h.<hex>-1", never "+h.<hex>".
// An empty digest returns canonical unchanged.
//
//	("1.8.1-2", "abc123def456")             → "1.8.1+h.abc123def456-2"
//	("0.2.0-dev.7+5395b8f-1", "abc…")       → "0.2.0-dev.7+5395b8f.h.abc…-1"
func TagVersion(canonical, digest string) string {
	if digest == "" {
		return canonical
	}
	base, suffix := splitRevisionSuffix(canonical)
	sep := "+" + contentTagMarker + "."
	if strings.Contains(base, "+") {
		sep = "." + contentTagMarker + "."
	}
	return base + sep + digest + suffix
}

// SplitContentTag reverses TagVersion: it recovers the canonical
// version-revision a store basename addresses, plus the content
// digest tagging it. An untagged basename is returned unchanged
// with an empty digest, so every path→identity reversal site can
// route through this blindly.
//
// The revision is stripped with SplitRevision's rule FIRST, then
// the terminal segment is matched, then the revision is
// re-attached. Matching before the strip would read the revision
// as part of the metadata.
//
// The match is deliberately narrow: exactly "h" followed by
// exactly contentDigestLen lowercase hex characters, as the last
// two dot-segments of the build metadata. An upstream version
// whose own build metadata happens to end that way would alias;
// the splitter is a store-layout convenience and
// .gale-provenance.toml remains the authority on what a directory
// actually holds.
func SplitContentTag(base string) (string, string) {
	stripped, suffix := splitRevisionSuffix(base)
	plus := strings.Index(stripped, "+")
	if plus < 0 {
		return base, ""
	}
	segs := strings.Split(stripped[plus+1:], ".")
	n := len(segs)
	if n < 2 || segs[n-2] != contentTagMarker ||
		!isContentDigest(segs[n-1]) {
		return base, ""
	}
	canonical := stripped[:plus]
	if rest := strings.Join(segs[:n-2], "."); rest != "" {
		canonical += "+" + rest
	}
	return canonical + suffix, segs[n-1]
}

// splitRevisionSuffix splits version into its base and its
// literal "-<N>" suffix, keeping the suffix's exact spelling so a
// caller can re-attach it byte for byte. A version without a
// numeric revision suffix yields an empty suffix — unlike
// SplitRevision, which reports the default revision 1 and cannot
// tell the two spellings apart.
func splitRevisionSuffix(version string) (string, string) {
	if !HasNumericRevisionSuffix(version) {
		return version, ""
	}
	i := strings.LastIndex(version, "-")
	return version[:i], version[i:]
}

// isContentDigest reports whether s is a content tag's digest:
// exactly contentDigestLen lowercase hex characters.
func isContentDigest(s string) bool {
	if len(s) != contentDigestLen {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// IsInstalled checks if a package version exists in the store.
// Returns false for empty directories left by failed installs.
// When version ends with "-1" and the exact directory is absent,
// falls back to the bare version directory (back-compat).
func (s *Store) IsInstalled(name, version string) bool {
	resolved, ok := s.resolveVersion(name, version)
	if !ok {
		return false
	}
	dir := filepath.Join(s.Root, name, resolved)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	return len(entries) > 0
}

// List returns all installed packages in the store.
func (s *Store) List() ([]InstalledPackage, error) {
	nameEntries, err := os.ReadDir(s.Root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list store root: %w", err)
	}

	var pkgs []InstalledPackage
	for _, nameEntry := range nameEntries {
		if !nameEntry.IsDir() {
			continue
		}
		versionEntries, err := os.ReadDir(
			filepath.Join(s.Root, nameEntry.Name()),
		)
		if err != nil {
			return nil, fmt.Errorf("list versions for %s: %w",
				nameEntry.Name(), err)
		}
		for _, versionEntry := range versionEntries {
			if !versionEntry.IsDir() {
				continue
			}
			// Skip transient siblings created by reinstall:
			// ".build-*" staging dirs and "<version>.bak"
			// backups that replaceStoreDir leaves visible
			// during its rename window.
			if isTransientStoreEntry(versionEntry.Name()) {
				continue
			}
			pkgs = append(pkgs, InstalledPackage{
				Name:    nameEntry.Name(),
				Version: versionEntry.Name(),
			})
		}
	}
	return pkgs, nil
}

// SweepTransient removes crash-leftover transient entries
// (".build-*" staging dirs, "<version>.bak" backups,
// "<version>.stream" extract dirs) under every <root>/<name>/
// directory. Only entries whose mtime is older than maxAge are
// touched, and a name dir whose lock file is concurrently
// flock-held is skipped entirely — an in-flight install of any
// version of that package may still be using its staging dirs
// (gh#78). Returns the swept paths; in dry mode, the paths that
// would be swept.
func (s *Store) SweepTransient(maxAge time.Duration, dry bool) []string {
	nameEntries, err := os.ReadDir(s.Root)
	if err != nil {
		return nil
	}
	cutoff := time.Now().Add(-maxAge)
	var swept []string
	for _, nameEntry := range nameEntries {
		if !nameEntry.IsDir() {
			continue
		}
		nameDir := filepath.Join(s.Root, nameEntry.Name())
		entries, err := os.ReadDir(nameDir)
		if err != nil {
			continue
		}
		if anyLockHeld(nameDir, entries) {
			// An install of some version of this package is in
			// flight; its transient siblings are live.
			continue
		}
		for _, e := range entries {
			if !isTransientStoreEntry(e.Name()) {
				continue
			}
			info, err := e.Info()
			if err != nil || info.ModTime().After(cutoff) {
				continue
			}
			path := filepath.Join(nameDir, e.Name())
			if !dry {
				if err := os.RemoveAll(path); err != nil {
					continue
				}
			}
			swept = append(swept, path)
		}
	}
	return swept
}

// AnyLockHeld reports whether any per-package lock file under
// the store is currently flock-held — i.e. an install or build
// is in flight somewhere in the store. Used by gc to veto
// sweeps of scratch space that cannot be attributed to a
// specific package (gh#79).
func (s *Store) AnyLockHeld() bool {
	nameEntries, err := os.ReadDir(s.Root)
	if err != nil {
		return false
	}
	for _, nameEntry := range nameEntries {
		if !nameEntry.IsDir() {
			continue
		}
		nameDir := filepath.Join(s.Root, nameEntry.Name())
		entries, err := os.ReadDir(nameDir)
		if err != nil {
			continue
		}
		if anyLockHeld(nameDir, entries) {
			return true
		}
	}
	return false
}

// anyLockHeld reports whether any *.lock file among entries
// (a listing of dir) is concurrently flock-held.
func anyLockHeld(dir string, entries []os.DirEntry) bool {
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".lock") {
			continue
		}
		if lockHeld(filepath.Join(dir, e.Name())) {
			return true
		}
	}
	return false
}

// lockHeld probes whether some process currently holds an
// exclusive flock on path. The probe lock is released
// immediately; an unopenable path counts as unheld.
func lockHeld(path string) bool {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer f.Close() // releases the probe flock
	if err := syscall.Flock(
		int(f.Fd()), //nolint:gosec // fd fits int on all supported platforms
		syscall.LOCK_EX|syscall.LOCK_NB,
	); err != nil {
		return true
	}
	return false
}

// Remove removes a package version from the store. Prefers
// exact match — callers that pass an on-disk name (e.g. from
// List) must get exactly that directory removed. Falls back
// to the back-compat patterns (bare dir for a "-1" request,
// highest-revision for a bare request) only when no exact
// match exists, so user-facing commands like
// `gale remove jq@1.8.1` still work regardless of which
// revision layout the store actually holds.
func (s *Store) Remove(name, version string) error {
	return s.RemoveWithin(name, version,
		func(_ string, drop func() error) error { return drop() })
}

// RemoveWithin is Remove with a caller-supplied critical section: it
// resolves name@version, holds that version's lock for the whole of
// fn, and cleans the parent name dir after releasing. fn receives
// the resolved dir and drop, the deletion itself, and decides where
// in its own work the deletion happens.
//
// It exists so a caller that must order this lock against another
// one can put its acquisition on the outside. The version lock is
// the same file the installer holds for a whole install
// (lockPackage), and an install takes the generation lock underneath
// it. A caller that took the generation lock first and reached the
// deletion second would close an AB-BA cycle on two blocking flock
// calls. Calling Remove from inside such a section does not help
// either: flock is per open-file-description, so re-acquiring in one
// process blocks on itself.
//
// drop tolerates ENOENT: two concurrent removers can race past the
// resolution above, and the loser must not fail (gh#77).
func (s *Store) RemoveWithin(
	name, version string, fn func(dir string, drop func() error) error,
) error {
	dir := filepath.Join(s.Root, name, version)
	if _, err := os.Stat(dir); err != nil {
		resolved, ok := s.resolveVersion(name, version)
		if !ok {
			return fmt.Errorf("remove %s@%s: %w",
				name, version, ErrNotInstalled)
		}
		dir = filepath.Join(s.Root, name, resolved)
		if _, err := os.Stat(dir); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove %s@%s: %w",
					name, version, ErrNotInstalled)
			}
			return fmt.Errorf("stat version directory: %w", err)
		}
	}

	drop := func() error {
		if err := os.RemoveAll(dir); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove version directory: %w", err)
		}
		return nil
	}
	if err := filelock.With(dir+".lock", func() error {
		return fn(dir, drop)
	}); err != nil {
		return err
	}
	// Cleanup runs after the lock is released so the flock
	// probe inside cleanupEmptyNameDir does not see it as
	// held (gh#77).
	return cleanupEmptyNameDir(s.Root, name)
}

// cleanupEmptyNameDir removes the parent <root>/<name> dir
// if no version dirs remain. Missing parent is not an error.
//
// A lock file (*.lock) that a concurrent process holds via
// flock is NEVER deleted (gh#77): mutual exclusion relies on
// every contender opening the same inode. Unlinking a held
// lock file lets the next filelock.Acquire create a fresh
// inode and succeed immediately while the original holder
// still "owns" the orphaned one — two installers of the same
// name@version then run concurrently. Residual unheld lock
// files are swept via a non-blocking flock probe (see
// tryRemoveUnheldLockFile); held ones survive, in which case
// the name dir is left in place and reaped by a later remove
// or gc once the holders are gone.
//
// Callers must NOT hold any lock file in the directory when
// calling this: the probe opens a separate fd, and flock is
// per open-file-description, so the caller's own held lock
// would (correctly) be treated as concurrently held and never
// be swept.
func cleanupEmptyNameDir(root, name string) error {
	nameDir := filepath.Join(root, name)
	entries, err := os.ReadDir(nameDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read name directory: %w", err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".lock") {
			// Real entries remain — keep everything.
			return nil
		}
	}
	// Only lock files remain. Sweep the unheld ones; any held
	// by a concurrent process survive and ENOTEMPTY below
	// keeps the name dir in place for them.
	for _, e := range entries {
		tryRemoveUnheldLockFile(filepath.Join(nameDir, e.Name()))
	}
	if err := os.Remove(nameDir); err != nil &&
		!errors.Is(err, os.ErrNotExist) &&
		!errors.Is(err, syscall.ENOTEMPTY) {
		return fmt.Errorf(
			"remove empty name directory: %w", err,
		)
	}
	return nil
}

// tryRemoveUnheldLockFile unlinks a lock file only when no
// process currently holds it: it takes a non-blocking flock
// probe and unlinks while still holding the probe, so any
// contender that opened the file before the unlink serializes
// behind the probe on the same inode. A residual window
// remains — a contender parked between its open and flock at
// the instant of the unlink later acquires the orphaned
// inode — but that requires a contender to arrive in the
// microseconds the probe is held on an unheld lock, versus
// the old behavior of unconditionally unlinking locks held
// for entire multi-minute installs.
func tryRemoveUnheldLockFile(path string) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return
	}
	defer f.Close() // releases the probe flock
	if err := syscall.Flock(
		int(f.Fd()), //nolint:gosec // fd fits int on all supported platforms
		syscall.LOCK_EX|syscall.LOCK_NB,
	); err != nil {
		// Held by a concurrent process — never delete (gh#77).
		return
	}
	_ = os.Remove(path)
}
