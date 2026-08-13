// Package farm manages gale's shared dylib farm at
// ~/.gale/lib/. The farm is a directory of symlinks to
// versioned dylibs from installed packages. Binaries that
// rpath the farm get a stable path that survives dep
// upgrades with SONAME-compatible changes (symlink flips
// to new version, binaries keep loading).
//
// Two classes of basename are farmed.
//
// Versioned sonames — libcurl.4.dylib, libssl.so.3 — carry
// their own ABI promise. One package claims each; a second
// claimant is a recipe bug and a hard error.
//
// Unversioned aliases — libssl.dylib — carry no promise of
// their own, so one is farmed only while exactly one package
// in the closure provides it AND it resolves, one hop inside
// that package's lib dir, to a soname the farm already
// carries, inheriting that soname's promise. Two providers
// (openssl and openssl4 both ship libssl.dylib) means the
// alias is farmed for neither, never that the install fails.
// Mach-O records unversioned names, so darwin only; ELF
// DT_NEEDED records the soname and needs none of this
// (gh#199).
package farm

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
)

// darwinVersioned matches libFOO.N.dylib, libFOO.N.M.dylib,
// libFOO.N.M.P.dylib. The stem class allows '.' so a stem with
// a non-numeric dotted segment like libMagick++-7.Q16HDRI.5.dylib
// (stem "Magick++-7.Q16HDRI") matches; the required trailing
// `.[0-9]+(...)*` plus the `$`-anchored `.dylib` keep the
// version pinned to the final numeric segment, so the stem
// can't swallow it and unversioned dylibs still fail (gh#168).
var darwinVersioned = regexp.MustCompile(
	`^lib[A-Za-z0-9_+.\-]+\.[0-9]+(\.[0-9]+)*\.dylib$`,
)

// linuxVersioned matches libFOO.so.N, libFOO.so.N.M,
// libFOO.so.N.M.P. The stem class allows '.' so dotted-stem
// sonames like libpython3.14.so.1.0 (stem "python3.14") match;
// the required `.so.` plus the `$`-anchored numeric tail keep
// the version pinned to the last `.so.`, so the stem can't
// swallow it (gh#165).
var linuxVersioned = regexp.MustCompile(
	`^lib[A-Za-z0-9_+.\-]+\.so\.[0-9]+(\.[0-9]+)*$`,
)

// CanonicalDir resolves a store directory's spelling without
// touching its version: the one spelling every farm and generation
// reader compares against, so a directory reached through macOS
// /var and one reached through /private/var are the same key.
//
// EvalSymlinks fails on a path that does not exist, and the raw
// spelling is the right answer then: the directory is absent under
// either spelling, and the caller's absence branch handles it.
//
// It lives here because the guard and its claim builders must agree
// on it. Three copies of this rule existed and every consumer of a
// claim had to re-derive which one it was holding; one exported
// spelling is what makes a claim's keys comparable across packages.
func CanonicalDir(dir string) string {
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return resolved
	}
	return dir
}

// DirFromStoreRoot returns the shared farm directory for a store
// root: <parent of storeRoot>/lib, which is ~/.gale/lib.
//
// There is deliberately no accessor taking a gale dir. There used to
// be, and every call site that mattered passed the INITIATING
// scope's gale dir, so a project-scoped rebuild pointed at
// <project>/.gale/lib — a directory nothing resolves through, since
// store binaries carry an rpath derived from the store root
// (DirFromStoreDir). One farm exists, it is shared, and it is
// reached from the store root; taking the argument that cannot
// express the mistake is what keeps it from recurring (design
// revision 6, section 4).
func DirFromStoreRoot(storeRoot string) string {
	return filepath.Join(filepath.Dir(storeRoot), "lib")
}

// DirFromStoreDir derives the farm dir from a store dir
// shaped like <galeDir>/pkg/<name>/<version>. Returns ""
// if the path doesn't fit that layout — callers must skip
// farm wiring in that case.
func DirFromStoreDir(storeDir string) string {
	pkgRoot := filepath.Dir(filepath.Dir(storeDir))
	if filepath.Base(pkgRoot) != "pkg" {
		return ""
	}
	return filepath.Join(filepath.Dir(pkgRoot), "lib")
}

// darwinLibName matches any darwin shared-library basename,
// versioned or not. It exists so the unversioned class can be
// defined as the complement of the versioned one within this
// shape rather than as a third independent regex: two patterns
// that must agree about where the version tail starts is the
// gh#165/gh#168 mistake waiting to be made again, and the two
// classes have to stay disjoint.
var darwinLibName = regexp.MustCompile(
	`^lib[A-Za-z0-9_+.\-]+\.dylib$`,
)

// IsVersionedDylib reports whether a basename matches the
// versioned dylib pattern for the current OS. Unversioned
// basenames (libfoo.dylib, libfoo.so) return false.
func IsVersionedDylib(name string) bool {
	return isVersionedFor(runtime.GOOS, name)
}

// isVersionedFor is IsVersionedDylib with the OS as an
// argument, so both platforms' rules are testable from either.
func isVersionedFor(goos, name string) bool {
	switch goos {
	case "darwin":
		return darwinVersioned.MatchString(name)
	case "linux":
		return linuxVersioned.MatchString(name)
	default:
		return false
	}
}

// IsUnversionedAlias reports whether a basename has the
// unversioned shared-library shape — libssl.dylib, not
// libssl.3.dylib. Shape only: whether such a name is farmable
// also depends on what it points at (see aliasTarget).
func IsUnversionedAlias(name string) bool {
	return isUnversionedAlias(runtime.GOOS, name)
}

// isUnversionedAlias is IsUnversionedAlias with the OS as an
// argument.
//
// Darwin only. Mach-O binaries record unversioned install names
// (git-remote-http references @rpath/libssl.dylib), so the farm
// must be able to answer them. ELF DT_NEEDED records the
// versioned soname instead, leaving libfoo.so a build-time
// devel symlink nothing resolves at runtime — farming it on
// Linux would be dead weight plus collision surface.
func isUnversionedAlias(goos, name string) bool {
	if goos != "darwin" {
		return false
	}
	return darwinLibName.MatchString(name) &&
		!darwinVersioned.MatchString(name)
}

// aliasTarget reports whether name in libDir is a farmable
// unversioned alias, and returns the in-package path a farm
// entry for it must target.
//
// Farmable means all of: unversioned-shaped basename; the entry
// is a SYMLINK (a real unversioned file inherits no soname's
// promise and is not farmed); the link's first hop stays inside
// this lib directory; that hop's basename is itself versioned;
// and the chain ends at a regular file.
//
// ONE hop, then Stat. Resolving the whole chain and demanding
// the real file sit in this lib dir would reject the layout
// spelling_test.go pins, where a farmed versioned soname is
// itself a symlink to a real file outside the store entirely.
//
// The returned path is the ALIAS inside the package, never what
// it resolves to — the rule Populate already applies to
// versioned aliases, and the one UnderStoreDir rests on.
func aliasTarget(libDir, name string) (string, bool) {
	if !IsUnversionedAlias(name) {
		return "", false
	}
	path := filepath.Join(libDir, name)
	hop, err := os.Readlink(path)
	if err != nil {
		return "", false
	}
	if !filepath.IsAbs(hop) {
		hop = filepath.Join(libDir, hop)
	}
	if filepath.Dir(filepath.Clean(hop)) != filepath.Clean(libDir) {
		return "", false
	}
	if !IsVersionedDylib(filepath.Base(hop)) {
		return "", false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return path, true
}

// exports is one store dir's contribution to the farm, split
// by entry class. The map values are the path INSIDE the
// package a farm entry targets — never the file that path
// resolves to.
type exports struct {
	// sonames holds versioned basenames. This is the CLAIM
	// set: what a scope requires the farm to hand it.
	sonames map[string]string
	// aliases holds unversioned basenames that inherit a
	// soname's promise. Farmed, but never claimed — a claimed
	// alias would make the guard refuse every rebuild on a
	// machine holding two packages that ship one.
	aliases map[string]string
}

// libExports enumerates one store dir's farmable lib entries.
// It is the single place the farming predicate is spelled;
// Populate, CheckDrift and the guard all read it, so they
// cannot disagree about what a package contributes. Three
// independent copies of that rule was three chances for the
// diagnostic to disagree with the thing it describes.
//
// The os.ReadDir error is returned unwrapped: callers differ
// on what a missing lib/ means (Populate returns nil,
// CheckDrift skips the dir, the guard reports nothing
// provided), so that decision stays with them.
func libExports(storeDir string) (exports, error) {
	libDir := filepath.Join(storeDir, "lib")
	out := exports{
		sonames: map[string]string{},
		aliases: map[string]string{},
	}
	entries, err := os.ReadDir(libDir)
	if err != nil {
		return out, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !IsVersionedDylib(name) {
			// The classes are disjoint, so this is the only
			// place an unversioned name can be considered.
			if target, ok := aliasTarget(libDir, name); ok {
				out.aliases[name] = target
			}
			continue
		}
		// Farm real files AND versioned symlink aliases.
		// libtool installs the real file as libfoo.so.N.M.P
		// with the soname libfoo.so.N as a symlink to it, and
		// ELF DT_NEEDED / Mach-O install names reference the
		// soname — so the alias must resolve through the farm
		// too. Stat follows the chain, so dangling aliases
		// drop out here.
		path := filepath.Join(libDir, name)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		out.sonames[name] = path
	}
	return out, nil
}

// Populate adds farm symlinks for every versioned dylib in
// <storeDir>/lib/. Creates farmDir if it doesn't exist.
//
// Conflict handling: if a farm entry already exists and
// points into a different package, returns an error. If
// it points into an older version of the same package,
// the entry is overwritten (newer wins) and a message is
// written to stderr.
func Populate(storeDir, farmDir string) error {
	e, err := libExports(storeDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read lib dir: %w", err)
	}

	if err := os.MkdirAll(farmDir, 0o755); err != nil {
		return fmt.Errorf("create farm dir: %w", err)
	}

	pkgName := packageName(storeDir)
	pkgVer := filepath.Base(storeDir)
	var replaced int

	// Sorted, not map order: which conflict surfaces first
	// must not depend on iteration order (940a67a).
	for _, name := range slices.Sorted(maps.Keys(e.sonames)) {
		target := e.sonames[name]
		link := filepath.Join(farmDir, name)

		if existing, err := os.Readlink(link); err == nil {
			// Link already exists.
			if existing == target {
				continue
			}
			existingPkg := packageName(filepath.Dir(
				filepath.Dir(existing),
			))
			if existingPkg != "" && existingPkg != pkgName {
				return fmt.Errorf(
					"farm conflict: %s claimed by both %q and %q",
					name, existingPkg, pkgName,
				)
			}
			// Same package, different version: overwrite.
			replaced++
			if err := os.Remove(link); err != nil {
				return fmt.Errorf("remove stale symlink: %w", err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			// Something other than a missing link. Unexpected
			// file type at farm path — not safe to overwrite.
			if _, statErr := os.Lstat(link); statErr == nil {
				return fmt.Errorf(
					"farm path %q exists but is not a symlink",
					link,
				)
			}
		}

		if err := os.Symlink(target, link); err != nil {
			return fmt.Errorf("create symlink %s: %w",
				name, err)
		}
	}

	if replaced > 0 {
		noun := "dylibs"
		if replaced == 1 {
			noun = "dylib"
		}
		fmt.Fprintf(
			os.Stderr,
			"farm: updated %s@%s (%d %s)\n",
			pkgName, pkgVer, replaced, noun,
		)
	}
	return nil
}

// AliasEntry is one farmable unversioned alias: the basename,
// the single store dir providing it, and the in-package path a
// farm entry carries.
type AliasEntry struct {
	Name     string
	StoreDir string
	Target   string
}

// AliasConflict is an unversioned alias more than one store dir
// in the closure provides. Owners holds sorted store
// identities.
type AliasConflict struct {
	Name   string
	Owners []string
}

// aliasProvider is one store dir's unversioned aliases, the
// input partitionAliases reasons over.
type aliasProvider struct {
	storeDir string
	aliases  map[string]string
}

// partitionAliases splits a closure's unversioned aliases into
// the ones the farm can carry and the ones it must drop.
//
// Order-independent by construction: a pure function of the
// whole set, so the same closure yields the same partition
// whatever order the dirs arrive in and whatever order they
// were installed in (940a67a).
//
// Drop, not error. Two packages shipping libssl.dylib is not
// the recipe bug two packages shipping libssl.3.dylib is:
// gale-recipes stages major ABI breaks as separate recipes
// (openssl, openssl4), both configure --libdir=lib, and both
// therefore ship the alias. Erroring would make that deliberate
// coexistence a hard install failure, gh#42's failure mode.
// Dropping leaves the unversioned name unresolvable — which is
// what it already is today — and costs nothing that worked.
//
// The collision key is the STORE DIR, not the package name: two
// revisions of one package resolving the same alias to
// different sonames are as incompatible as two packages, and no
// versioned conflict fires to catch that.
func partitionAliases(
	providers []aliasProvider,
) ([]AliasEntry, []AliasConflict) {
	byName := map[string]map[string]string{}
	for _, p := range providers {
		for name, target := range p.aliases {
			if byName[name] == nil {
				byName[name] = map[string]string{}
			}
			byName[name][p.storeDir] = target
		}
	}

	var entries []AliasEntry
	var conflicts []AliasConflict
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		dirs := byName[name]
		if len(dirs) == 1 {
			for dir, target := range dirs {
				entries = append(entries, AliasEntry{
					Name: name, StoreDir: dir, Target: target,
				})
			}
			continue
		}
		owners := make([]string, 0, len(dirs))
		for dir := range dirs {
			owners = append(owners, identityOfDir(dir))
		}
		slices.Sort(owners)
		conflicts = append(conflicts, AliasConflict{
			Name: name, Owners: owners,
		})
	}
	return entries, conflicts
}

// FarmableAliases partitions the unversioned aliases a closure
// provides into the ones the farm carries and the ones it drops
// because more than one store dir supplies them. Both results
// are sorted by Name.
func FarmableAliases(
	storeDirs []string,
) ([]AliasEntry, []AliasConflict, error) {
	var providers []aliasProvider
	for _, dir := range storeDirs {
		e, err := libExports(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, nil, fmt.Errorf(
				"enumerating aliases of %s: %w", dir, err,
			)
		}
		if len(e.aliases) == 0 {
			continue
		}
		providers = append(providers, aliasProvider{
			storeDir: dir, aliases: e.aliases,
		})
	}
	entries, conflicts := partitionAliases(providers)
	return entries, conflicts, nil
}

// linkAliases lays a closure's farmable unversioned aliases.
// Runs after every Populate so the versioned entries they
// inherit from are already settled, and never overwrites an
// existing farm entry.
func linkAliases(storeDirs []string, farmDir string) error {
	entries, _, err := FarmableAliases(storeDirs)
	if err != nil {
		return err
	}
	for _, e := range entries {
		link := filepath.Join(farmDir, e.Name)
		if _, err := os.Lstat(link); err == nil {
			continue
		}
		if err := os.Symlink(e.Target, link); err != nil {
			return fmt.Errorf(
				"create alias symlink %s: %w", e.Name, err,
			)
		}
	}
	return nil
}

// Depopulate removes farm symlinks whose target starts with
// storeDir. Called on package remove.
func Depopulate(storeDir, farmDir string) error {
	entries, err := os.ReadDir(farmDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read farm dir: %w", err)
	}

	for _, entry := range entries {
		link := filepath.Join(farmDir, entry.Name())
		target, err := os.Readlink(link)
		if err != nil {
			continue // not a symlink — don't touch
		}
		// The same predicate GuardDepopulate decides on. Two
		// spellings of one comparison would let the guard approve a
		// removal this loop then does not perform, or the reverse.
		if !UnderStoreDir(target, storeDir) {
			continue
		}
		if err := os.Remove(link); err != nil {
			return fmt.Errorf("remove %s: %w", link, err)
		}
	}
	return nil
}

// StagingPrefix names the directories Stage builds farm images in.
// Exported for `gale gc`, which reclaims one left by a killed
// process: they sit beside the farm in ~/.gale, where the stale
// current-new.<pid> swap links are already swept from.
const StagingPrefix = "lib.staging."

// Staged is a complete farm image built beside the farm, resolvable
// by nothing until Publish moves it in.
//
// It exists because the fallible half of a rebuild has to happen
// before its caller's commit point and the visible half after it.
// The generation rebuild swaps the current symlink in between, and
// that swap is what it cannot undo (gh#184).
type Staged struct {
	dir     string
	farmDir string
	// names is the image: every basename staged, read once, so
	// publication never re-enumerates a directory it is mutating.
	names []string
}

// Stage builds the farm image for the given store dirs without
// publishing any of it. Callers pass the resolved store dir for
// every package in the current generation plus every other scope's
// claim; older revisions still on disk are ignored because they're
// not on PATH.
//
// Everything that can realistically fail happens here: reading each
// store dir's lib, resolving the closure's unversioned aliases,
// creating the links, and proving the farm directory itself is
// usable. A caller that has not reached its commit point can
// therefore abandon the whole operation for free.
//
// Errors are COLLECTED rather than returned at the first failure,
// which costs nothing now that nothing is published either way: the
// caller learns about every unreadable package in one run instead of
// one per re-run.
//
// They are returned, not merely logged (design revision 6, section
// 6). An unpopulated shared farm means binaries cannot load their
// dylibs, and a line on stderr inside a direnv hook is invisible.
func Stage(activeStoreDirs []string, farmDir string) (*Staged, error) {
	// The farm directory is proved usable HERE, while a refusal is
	// still free. It used to be wiped and recreated, which quietly
	// replaced whatever occupied the path — a stray file, a symlink
	// pointing elsewhere — and reported success.
	if err := os.MkdirAll(farmDir, 0o755); err != nil {
		return nil, fmt.Errorf("create farm dir: %w", err)
	}
	s := &Staged{dir: stagingPath(farmDir), farmDir: farmDir}
	// A leftover of this PID's own, from a run killed between
	// staging and publication. The same reasoning swapCurrentSymlink
	// applies to current-new.<pid>: another process's staging dir is
	// its business, this one's is ours.
	if err := os.RemoveAll(s.dir); err != nil {
		return nil, fmt.Errorf("clear stale farm staging dir: %w", err)
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return nil, fmt.Errorf("create farm staging dir: %w", err)
	}

	var errs []error
	for _, storeDir := range activeStoreDirs {
		if err := Populate(storeDir, s.dir); err != nil {
			errs = append(errs, fmt.Errorf("populate %s: %w", storeDir, err))
		}
	}
	// Aliases are a property of the closure, not of a package:
	// whether libssl.dylib is farmable depends on whether a
	// second provider exists, which Populate cannot see. Doing
	// it here, once, over the whole set is what makes the drop
	// stable rather than provisional (gh#199).
	if err := linkAliases(activeStoreDirs, s.dir); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		s.Discard()
		return nil, err
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		s.Discard()
		return nil, fmt.Errorf("read staged farm image: %w", err)
	}
	for _, e := range entries {
		s.names = append(s.names, e.Name())
	}
	return s, nil
}

// Publish makes the staged image the farm.
//
// Per name, atomically: an entry the image agrees with is left
// exactly as it is, and any other is renamed over within the one
// directory. Nothing is ever removed first, so no soname is absent
// at any instant it was present before — which is the property a
// running process's late dlopen depends on, and the one a
// wipe-and-recreate could not offer.
//
// Not atomic ACROSS names. A dlopen of two sonames landing mid
// publication can see one old and one new, which the farm's own
// premise makes safe: the entries it carries are SONAME-compatible
// by construction, and the alternative this replaces had both
// absent.
//
// Fail fast, unlike Stage: a rename failing within a directory the
// process just wrote means the filesystem is not cooperating, and
// continuing would only widen the mixed window.
//
// The farm directory is deliberately not fsync'd afterwards, exactly
// as the generation swap does not fsync ~/.gale. The farm is
// derivable from the store and a rebuild reconstructs it, so
// durability across a power loss buys nothing that `gale sync` does
// not already give.
func (s *Staged) Publish() error {
	staged := make(map[string]bool, len(s.names))
	for _, name := range s.names {
		staged[name] = true
		if err := s.publishOne(name); err != nil {
			return err
		}
	}
	if err := s.pruneUnstaged(staged); err != nil {
		return err
	}
	s.Discard()
	return nil
}

// publishOne moves one staged entry into the farm, leaving an
// already-correct entry untouched.
func (s *Staged) publishOne(name string) error {
	from := filepath.Join(s.dir, name)
	want, err := os.Readlink(from)
	if err != nil {
		return fmt.Errorf("read staged farm entry %s: %w", name, err)
	}
	to := filepath.Join(s.farmDir, name)
	if have, rerr := os.Readlink(to); rerr == nil && have == want {
		return nil
	}
	if err := os.Rename(from, to); err != nil {
		return fmt.Errorf("publish farm entry %s: %w", name, err)
	}
	return nil
}

// pruneUnstaged drops the farm entries the image does not carry.
// The image is the whole state being established, so an entry
// outside it is one the closure no longer provides.
func (s *Staged) pruneUnstaged(staged map[string]bool) error {
	entries, err := os.ReadDir(s.farmDir)
	if err != nil {
		return fmt.Errorf("read farm dir: %w", err)
	}
	for _, e := range entries {
		if staged[e.Name()] {
			continue
		}
		if err := os.Remove(filepath.Join(s.farmDir, e.Name())); err != nil {
			return fmt.Errorf("remove superseded farm entry %s: %w",
				e.Name(), err)
		}
	}
	return nil
}

// Discard removes the staged image, published or not. Best effort:
// it runs on paths that already have an error to report, and a
// surviving staging dir is inert — nothing resolves through it, and
// `gale gc` sweeps it.
func (s *Staged) Discard() {
	_ = os.RemoveAll(s.dir)
}

// stagingPath is the directory an image is staged in: a sibling of
// the farm, so the publishing renames stay within one filesystem by
// construction, and PID-scoped exactly as swapCurrentSymlink scopes
// current-new.<pid>.
func stagingPath(farmDir string) string {
	return filepath.Join(
		filepath.Dir(farmDir),
		fmt.Sprintf("%s%d", StagingPrefix, os.Getpid()),
	)
}

// Rebuild rebuilds farmDir from the given store dirs, for callers
// with no commit point of their own to order against: Stage and
// Publish back to back.
func Rebuild(activeStoreDirs []string, farmDir string) error {
	staged, err := Stage(activeStoreDirs, farmDir)
	if err != nil {
		return err
	}
	defer staged.Discard()
	return staged.Publish()
}

// CheckDrift reports farm entries that don't match the
// active generation's store dirs. Each returned string
// describes one drift item in a form suitable for a
// `gale doctor` line. Returns nil if the farm is in sync.
//
// activeStoreDirs is the same slice passed to Rebuild — the
// resolved store dirs for every package in the active
// generation. Older revisions still on disk (awaiting
// `gale gc`) are intentionally ignored because they aren't
// on PATH and must not be in the farm.
func CheckDrift(activeStoreDirs []string, farmDir string) ([]string, error) {
	entries, err := os.ReadDir(farmDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read farm dir: %w", err)
	}

	var issues []string

	// Drift type 1: farm symlinks whose target no longer
	// exists (or isn't a regular file).
	//
	// The farm is shared, and every scope now checks the same one,
	// so a scope sees breakage it did not cause. Each item says
	// which case it is. It does not name an owner: an entry outside
	// this scope's closure may belong to another live scope, or be
	// stale, or be orphaned, and CheckDrift cannot tell those apart
	// (design revision 6, section 6).
	//
	// Either class is repairable from here. A rebuild repopulates
	// the shared farm from the guarded union of every claimant, so
	// `gale doctor --repair` run in any scope restores another
	// scope's entry as long as its store bytes are still present.
	claimed := claimedSonames(activeStoreDirs)
	for _, e := range entries {
		scope := "shared farm entry outside this scope's closure"
		if claimed[e.Name()] {
			scope = "required by this scope"
		}
		link := filepath.Join(farmDir, e.Name())
		info, err := os.Stat(link) // follows symlink
		if err != nil {
			issues = append(issues, fmt.Sprintf(
				"broken symlink: %s (%s)", e.Name(), scope,
			))
			continue
		}
		if !info.Mode().IsRegular() {
			issues = append(issues, fmt.Sprintf(
				"symlink target is not a regular file: %s (%s)",
				e.Name(), scope,
			))
		}
	}

	// Drift type 2: packages in the active generation whose
	// versioned dylibs aren't represented in the farm.
	for _, storeDir := range activeStoreDirs {
		e, err := libExports(storeDir)
		if err != nil {
			continue
		}
		pkgName := packageName(storeDir)
		pkgVer := filepath.Base(storeDir)
		pkgRoot := filepath.Dir(filepath.Dir(storeDir))
		// Versioned sonames only. A MISSING alias is never drift:
		// activeStoreDirs is one scope's closure, while Rebuild
		// partitions the machine-wide guarded union, so a scope
		// holding only openssl computes "libssl.dylib should be
		// here" for a union that legitimately dropped it. Drift
		// renders as an Error telling the user to run
		// `gale doctor --repair`, and no repair can clear that —
		// gh#50 again. Contested aliases are reported separately,
		// as a warning, by the doctor.
		//
		// Sorted so a multi-issue report reads the same way
		// twice (940a67a).
		for _, name := range slices.Sorted(maps.Keys(e.sonames)) {
			lp := e.sonames[name]
			link := filepath.Join(farmDir, name)
			target, err := os.Readlink(link)
			if err != nil {
				issues = append(issues, fmt.Sprintf(
					"missing farm entry for %s@%s: %s",
					pkgName, pkgVer, name,
				))
				continue
			}
			// If the symlink points elsewhere, the basename
			// is claimed by another package — surface it.
			if filepath.Clean(target) != lp {
				pkgPrefix := filepath.Clean(filepath.Join(
					pkgRoot, pkgName,
				)) + string(filepath.Separator)
				if !strings.HasPrefix(
					filepath.Clean(target)+string(filepath.Separator),
					pkgPrefix,
				) {
					issues = append(issues, fmt.Sprintf(
						"%s claimed by another package (farm -> %s)",
						name, target,
					))
				}
			}
		}
	}

	return issues, nil
}

// packageName extracts the package name from a store dir
// path like .../pkg/<name>/<version>. Returns "" on
// unexpected shapes (grandparent is not "pkg").
func packageName(storeDir string) string {
	// storeDir = <root>/pkg/<name>/<version>.
	// parent  = <root>/pkg/<name>
	// grandpa = <root>/pkg
	parent := filepath.Dir(storeDir)
	if filepath.Base(filepath.Dir(parent)) != "pkg" {
		return ""
	}
	return filepath.Base(parent)
}

// claimedSonames names the versioned dylibs the given store dirs
// provide. It answers one question for CheckDrift: is a farm entry
// part of the closure being checked, or does it belong to the wider
// shared farm?
//
// It delegates to libSonames, itself a view over libExports, rather
// than re-deriving the predicate. That enumeration is now spelled
// once: three copies of "which files does this package contribute
// to the farm" was three chances for the diagnostic to disagree
// with the thing it describes, and the first copy already labelled
// dangling aliases as required.
func claimedSonames(storeDirs []string) map[string]bool {
	out := make(map[string]bool)
	for _, storeDir := range storeDirs {
		// libProvides: a farmed alias pointing into this scope's
		// own closure labelled "outside this scope's closure"
		// would be exactly the diagnostic-disagrees-with-reality
		// bug this function's own comment warns about.
		libs, err := libProvides(storeDir)
		if err != nil {
			continue
		}
		for name := range libs {
			out[name] = true
		}
	}
	return out
}
