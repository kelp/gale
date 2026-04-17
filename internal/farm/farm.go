// Package farm manages gale's shared dylib farm at
// ~/.gale/lib/. The farm is a directory of symlinks to
// versioned dylibs from installed packages. Binaries that
// rpath the farm get a stable path that survives dep
// upgrades with SONAME-compatible changes (symlink flips
// to new version, binaries keep loading).
//
// Only versioned basenames are farmed — libcurl.4.dylib,
// libssl.so.3, etc. Unversioned basenames like libcurl.dylib
// aren't farmed because they'd collide across major
// versions.
package farm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// darwinVersioned matches libFOO.N.dylib, libFOO.N.M.dylib,
// libFOO.N.M.P.dylib.
var darwinVersioned = regexp.MustCompile(
	`^lib[A-Za-z0-9_+\-]+\.[0-9]+(\.[0-9]+)*\.dylib$`)

// linuxVersioned matches libFOO.so.N, libFOO.so.N.M,
// libFOO.so.N.M.P.
var linuxVersioned = regexp.MustCompile(
	`^lib[A-Za-z0-9_+\-]+\.so\.[0-9]+(\.[0-9]+)*$`)

// Dir returns the farm directory for a given gale dir.
// Typically ~/.gale/lib/.
func Dir(galeDir string) string {
	return filepath.Join(galeDir, "lib")
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

// IsVersionedDylib reports whether a basename matches the
// versioned dylib pattern for the current OS. Unversioned
// basenames (libfoo.dylib, libfoo.so) return false.
func IsVersionedDylib(name string) bool {
	switch runtime.GOOS {
	case "darwin":
		return darwinVersioned.MatchString(name)
	case "linux":
		return linuxVersioned.MatchString(name)
	default:
		return false
	}
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
	libDir := filepath.Join(storeDir, "lib")
	entries, err := os.ReadDir(libDir)
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

	for _, entry := range entries {
		name := entry.Name()
		if !IsVersionedDylib(name) {
			continue
		}
		// Only farm regular files. Versioned aliases like
		// libexpat.1.dylib are symlinks to the real file
		// (libexpat.1.11.3.dylib); farming only the real
		// file avoids redundant entries.
		info, err := os.Lstat(filepath.Join(libDir, name))
		if err != nil {
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}

		target := filepath.Join(libDir, name)
		link := filepath.Join(farmDir, name)

		if existing, err := os.Readlink(link); err == nil {
			// Link already exists.
			if existing == target {
				continue
			}
			existingPkg := packageName(filepath.Dir(
				filepath.Dir(existing)))
			if existingPkg != "" && existingPkg != pkgName {
				return fmt.Errorf(
					"farm conflict: %s claimed by both %q and %q",
					name, existingPkg, pkgName)
			}
			// Same package, different version: overwrite.
			fmt.Fprintf(os.Stderr,
				"farm: replacing %s: %s -> %s\n",
				name, existing, target)
			if err := os.Remove(link); err != nil {
				return fmt.Errorf("remove stale symlink: %w", err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			// Something other than a missing link. Unexpected
			// file type at farm path — not safe to overwrite.
			if _, statErr := os.Lstat(link); statErr == nil {
				return fmt.Errorf(
					"farm path %q exists but is not a symlink",
					link)
			}
		}

		if err := os.Symlink(target, link); err != nil {
			return fmt.Errorf("create symlink %s: %w",
				name, err)
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

	storeDirClean := filepath.Clean(storeDir)
	for _, entry := range entries {
		link := filepath.Join(farmDir, entry.Name())
		target, err := os.Readlink(link)
		if err != nil {
			continue // not a symlink — don't touch
		}
		if !strings.HasPrefix(
			filepath.Clean(target), storeDirClean+string(filepath.Separator),
		) {
			continue
		}
		if err := os.Remove(link); err != nil {
			return fmt.Errorf("remove %s: %w", link, err)
		}
	}
	return nil
}

// Repopulate wipes farmDir and rebuilds it by walking every
// installed package under storeRoot. Called on generation
// rollback and by `gale rebuild-farm`.
func Repopulate(storeRoot, farmDir string) error {
	if err := os.RemoveAll(farmDir); err != nil {
		return fmt.Errorf("clear farm dir: %w", err)
	}
	if err := os.MkdirAll(farmDir, 0o755); err != nil {
		return fmt.Errorf("create farm dir: %w", err)
	}

	nameEntries, err := os.ReadDir(storeRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read store root: %w", err)
	}

	for _, ne := range nameEntries {
		if !ne.IsDir() {
			continue
		}
		nameDir := filepath.Join(storeRoot, ne.Name())
		verEntries, err := os.ReadDir(nameDir)
		if err != nil {
			continue
		}
		for _, ve := range verEntries {
			if !ve.IsDir() {
				continue
			}
			// Skip the same .build-* staging dirs that
			// Store.List skips.
			if strings.HasPrefix(ve.Name(), ".build-") {
				continue
			}
			storeDir := filepath.Join(nameDir, ve.Name())
			if err := Populate(storeDir, farmDir); err != nil {
				// Don't fail the whole repopulation on a
				// single package's conflict — warn and keep
				// going. A genuine conflict will show up in
				// `gale inspect` anyway.
				fmt.Fprintf(os.Stderr,
					"farm: populate %s: %v\n", storeDir, err)
			}
		}
	}
	return nil
}

// CheckDrift reports farm entries that don't match the
// current store state. Each returned string describes one
// drift item in a form suitable for a `gale doctor` line.
// Returns nil if the farm is in sync.
func CheckDrift(storeRoot, farmDir string) ([]string, error) {
	entries, err := os.ReadDir(farmDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read farm dir: %w", err)
	}

	var issues []string

	// Drift type 1: farm symlinks whose target no longer
	// exists (or isn't a regular file).
	for _, e := range entries {
		link := filepath.Join(farmDir, e.Name())
		info, err := os.Stat(link) // follows symlink
		if err != nil {
			issues = append(issues, fmt.Sprintf(
				"broken symlink: %s", e.Name()))
			continue
		}
		if !info.Mode().IsRegular() {
			issues = append(issues, fmt.Sprintf(
				"symlink target is not a regular file: %s",
				e.Name()))
		}
	}

	// Drift type 2: installed packages whose versioned
	// dylibs aren't represented in the farm.
	nameEntries, err := os.ReadDir(storeRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return issues, nil
		}
		return nil, fmt.Errorf("read store root: %w", err)
	}
	for _, ne := range nameEntries {
		if !ne.IsDir() {
			continue
		}
		versionEntries, err := os.ReadDir(
			filepath.Join(storeRoot, ne.Name()))
		if err != nil {
			continue
		}
		for _, ve := range versionEntries {
			if !ve.IsDir() {
				continue
			}
			if strings.HasPrefix(ve.Name(), ".build-") {
				continue
			}
			libDir := filepath.Join(
				storeRoot, ne.Name(), ve.Name(), "lib")
			libs, err := os.ReadDir(libDir)
			if err != nil {
				continue
			}
			for _, l := range libs {
				if !IsVersionedDylib(l.Name()) {
					continue
				}
				// Only check regular files — skip versioned
				// aliases (symlinks) to avoid flagging them
				// as missing when only the real file is farmed.
				lp := filepath.Join(libDir, l.Name())
				lInfo, err := os.Lstat(lp)
				if err != nil || !lInfo.Mode().IsRegular() {
					continue
				}
				link := filepath.Join(farmDir, l.Name())
				target, err := os.Readlink(link)
				if err != nil {
					issues = append(issues, fmt.Sprintf(
						"missing farm entry for %s@%s: %s",
						ne.Name(), ve.Name(), l.Name()))
					continue
				}
				// If the symlink points elsewhere, the
				// conflict with another pkg claiming the
				// same basename — surface it.
				if filepath.Clean(target) != lp {
					if !strings.HasPrefix(
						filepath.Clean(target)+string(filepath.Separator),
						filepath.Clean(filepath.Join(
							storeRoot, ne.Name()))+string(filepath.Separator),
					) {
						issues = append(issues, fmt.Sprintf(
							"%s claimed by another package (farm -> %s)",
							l.Name(), target))
					}
				}
			}
		}
	}

	return issues, nil
}

// packageName extracts the package name from a store dir
// path like .../pkg/<name>/<version>. Returns "" on
// unexpected shapes.
func packageName(storeDir string) string {
	// storeDir = <root>/<name>/<version>. Name is the
	// parent dir's basename.
	parent := filepath.Dir(storeDir)
	if filepath.Base(parent) == "" {
		return ""
	}
	return filepath.Base(parent)
}
