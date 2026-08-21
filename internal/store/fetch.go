package store

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FetchNamespace is the first-level store directory that holds
// fetched artifacts. Source resolution never reads it. The name
// is reserved: List would otherwise emit {Name: "fetch",
// Version: "<pkg>"}, and gc's removeUnreferencedVersions would
// delete the whole tree.
const FetchNamespace = "fetch"

// ErrReservedName reports that a source-store identity used the
// fetch namespace as a package name.
var ErrReservedName = errors.New("name is reserved")

const (
	sha256HexLen = 64
	sha12Len     = 12
	// reservedResolveSentinel is the extra path component
	// ResolveDir appends for a reserved name. The literal
	// <root>/fetch/<pkg> join exists (the namespace), so
	// callers that Stat or filepath.Join the result need a
	// joinable path that is not that directory.
	reservedResolveSentinel = ".reserved"
)

func isReservedName(name string) bool {
	return name == FetchNamespace
}

// FetchPath returns the store directory for a fetched artifact.
// The path is <root>/fetch/<name>/<version>-<sha12>, where sha12
// is the first 12 hex characters of artifactSHA256. The digest
// must be 64 hex characters with no algorithm prefix. Case is
// normalized so one digest maps to one path. The directory is
// not created.
func (s *Store) FetchPath(name, version, artifactSHA256 string) (string, error) {
	if isReservedName(name) {
		return "", fmt.Errorf("fetch path: %w", ErrReservedName)
	}
	if !safeComponent(name) {
		return "", fmt.Errorf("fetch path: name %q is not a single path component", name)
	}
	if !safeComponent(version) {
		return "", fmt.Errorf("fetch path: version %q is not a single path component", version)
	}
	digest, err := canonicalArtifactSHA256(artifactSHA256)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.Root, FetchNamespace, name, version+"-"+digest[:sha12Len]), nil
}

// FetchExists reports whether the exact fetch identity is present
// and populated. Empty directories count as absent. There is no
// fallback to a source-layout directory or to another digest.
func (s *Store) FetchExists(name, version, artifactSHA256 string) (bool, error) {
	dir, err := s.FetchPath(name, version, artifactSHA256)
	if err != nil {
		return false, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("fetch exists: %w", err)
	}
	return len(entries) > 0, nil
}

// FetchIdentity is one fetch/<name>/<version>-<sha12> directory.
type FetchIdentity struct {
	Name    string
	Version string
	SHA12   string
}

// Rel is the store-relative owner path gc marks and sweeps.
func (id FetchIdentity) Rel() string {
	return filepath.Join(FetchNamespace, id.Name, id.Version+"-"+id.SHA12)
}

// ErrIncompleteFetchIdentity reports a RemoveFetch call that
// is not a complete sha12 identity.
var ErrIncompleteFetchIdentity = errors.New("fetch identity is incomplete")

const fetchStagingPrefix = ".tmp-"

func parseFetchDirName(dir string) (version, sha12 string, ok bool) {
	i := strings.LastIndex(dir, "-")
	if i <= 0 || len(dir)-i-1 != sha12Len {
		return "", "", false
	}
	sha := dir[i+1:]
	if !isHex(sha) {
		return "", "", false
	}
	return dir[:i], strings.ToLower(sha), true
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && len(s)%2 == 0
}

func canonicalFetchSHA12(sha string) (string, error) {
	if len(sha) != sha12Len || !isHex(sha) {
		return "", fmt.Errorf("remove fetch: %w", ErrIncompleteFetchIdentity)
	}
	return strings.ToLower(sha), nil
}

// ListFetch returns every complete fetch/<name>/<version>-<sha12>
// identity. Staging dirs and prefix siblings are omitted.
func (s *Store) ListFetch() ([]FetchIdentity, error) {
	ns := filepath.Join(s.Root, FetchNamespace)
	nameEntries, err := os.ReadDir(ns)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list fetch: %w", err)
	}
	var out []FetchIdentity
	for _, nameEntry := range nameEntries {
		if !nameEntry.IsDir() || strings.HasPrefix(nameEntry.Name(), fetchStagingPrefix) {
			continue
		}
		name := nameEntry.Name()
		if !safeComponent(name) || isReservedName(name) {
			continue
		}
		verEntries, err := os.ReadDir(filepath.Join(ns, name))
		if err != nil {
			return nil, fmt.Errorf("list fetch %s: %w", name, err)
		}
		for _, verEntry := range verEntries {
			if !verEntry.IsDir() {
				continue
			}
			version, sha12, ok := parseFetchDirName(verEntry.Name())
			if !ok || !safeComponent(version) {
				continue
			}
			out = append(out, FetchIdentity{
				Name: name, Version: version, SHA12: sha12,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		if out[i].Version != out[j].Version {
			return out[i].Version < out[j].Version
		}
		return out[i].SHA12 < out[j].SHA12
	})
	return out, nil
}

// RemoveFetch deletes one sha12 identity. It refuses the
// reserved name and any identity that is not version + 12 hex.
func (s *Store) RemoveFetch(name, version, sha string) error {
	if isReservedName(name) {
		return fmt.Errorf("remove fetch %s@%s: %w",
			name, version, ErrReservedName)
	}
	if !safeComponent(name) || !safeComponent(version) {
		return fmt.Errorf("remove fetch %s@%s: %w",
			name, version, ErrIncompleteFetchIdentity)
	}
	sha12, err := canonicalFetchSHA12(sha)
	if err != nil {
		return err
	}
	dir := filepath.Join(s.Root, FetchNamespace, name, version+"-"+sha12)
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove fetch %s@%s-%s: %w",
				name, version, sha12, ErrNotInstalled)
		}
		return fmt.Errorf("stat fetch identity: %w", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove fetch identity: %w", err)
	}
	return cleanupEmptyNameDir(filepath.Join(s.Root, FetchNamespace), name)
}

// FetchStagingAlive reports whether any pkg/fetch/.tmp-*
// entry exists and returns those paths.
func (s *Store) FetchStagingAlive() (bool, []string, error) {
	ns := filepath.Join(s.Root, FetchNamespace)
	entries, err := os.ReadDir(ns)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("list fetch staging: %w", err)
	}
	var paths []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), fetchStagingPrefix) {
			paths = append(paths, filepath.Join(ns, e.Name()))
		}
	}
	sort.Strings(paths)
	return len(paths) > 0, paths, nil
}

func canonicalArtifactSHA256(s string) (string, error) {
	if strings.HasPrefix(s, "sha256:") || len(s) != sha256HexLen {
		return "", fmt.Errorf("fetch path: sha256 must be %d hex characters", sha256HexLen)
	}
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != sha256HexLen/2 {
		return "", fmt.Errorf("fetch path: sha256 must be %d hex characters", sha256HexLen)
	}
	return hex.EncodeToString(b), nil
}
