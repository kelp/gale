package store

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
