// Package fetch lands an unused index artifact in the fetch store
// namespace. Source install stays the only installer.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/index"
	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/store"
)

// Fetcher lands one artifact. Tests inject AllowHost and WriteFetch.
type Fetcher struct {
	AllowHost  func(host string) bool
	WriteFetch func(dir string, r provenance.FetchRecord) error
}

// ToStore fetches art into st at FetchPath(name, version, sha256).
func ToStore(ctx context.Context, st *store.Store, name, version string, art index.Artifact) (string, error) {
	return (&Fetcher{}).ToStore(ctx, st, name, version, art)
}

// ToStore fetches art into st at FetchPath(name, version, sha256).
func (f *Fetcher) ToStore(ctx context.Context, st *store.Store, name, version string, art index.Artifact) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := checkName(name); err != nil {
		return "", err
	}
	if err := f.validate(art); err != nil {
		return "", err
	}
	dest, err := st.FetchPath(name, version, art.SHA256)
	if err != nil {
		return "", fmt.Errorf("fetch path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", fmt.Errorf("create fetch parent: %w", err)
	}
	staging, err := os.MkdirTemp(filepath.Join(st.Root, store.FetchNamespace), ".tmp-")
	if err != nil {
		return "", fmt.Errorf("create staging: %w", err)
	}
	defer os.RemoveAll(staging)

	job := landJob{dest: dest, name: name, version: version, art: art}
	if err := f.materialize(ctx, staging, job); err != nil {
		return "", err
	}
	return dest, nil
}

type landJob struct {
	dest    string
	name    string
	version string
	art     index.Artifact
}

func (f *Fetcher) materialize(ctx context.Context, staging string, job landJob) error {
	archive := filepath.Join(staging, "archive")
	if err := download.Fetch(ctx, job.art.URL, archive); err != nil {
		return fmt.Errorf("fetch artifact: %w", err)
	}
	if err := download.VerifySHA256(ctx, archive, job.art.SHA256); err != nil {
		return err
	}
	treeDir := filepath.Join(staging, "tree")
	if err := os.MkdirAll(treeDir, 0o755); err != nil {
		return fmt.Errorf("create mapped tree: %w", err)
	}
	if err := placeMapped(ctx, archive, treeDir, job.art); err != nil {
		return err
	}
	got, err := provenance.DigestTree(ctx, treeDir)
	if err != nil {
		return fmt.Errorf("tree digest: %w", err)
	}
	if got != job.art.TreeDigest {
		return fmt.Errorf("tree digest mismatch: got %s, want %s", got, job.art.TreeDigest)
	}
	if err := publish(ctx, job.dest, treeDir, job.art.TreeDigest); err != nil {
		return err
	}
	return f.writeSidecar(job.dest, job.name, job.version, job.art)
}

func (f *Fetcher) writeSidecar(dest, name, version string, art index.Artifact) error {
	rec := provenance.FetchRecord{
		Name:       name,
		Version:    version,
		SHA256:     art.SHA256,
		TreeDigest: art.TreeDigest,
		Method:     provenance.MethodFetch,
	}
	write := provenance.WriteFetch
	if f != nil && f.WriteFetch != nil {
		write = f.WriteFetch
	}
	if err := write(dest, rec); err != nil {
		return fmt.Errorf("fetch provenance: %w", err)
	}
	return nil
}

// ValidateSpec checks authoring fields (URL, format, strip, files).
// It does not require sha256 or tree_digest; those are produced
// later. allow defaults to index.AllowedHost.
func ValidateSpec(art index.Artifact, allow func(string) bool) error {
	if allow == nil {
		allow = index.AllowedHost
	}
	if err := validateURL(art.URL, allow); err != nil {
		return err
	}
	if !allowedFormat[art.Format] {
		return fmt.Errorf("unsupported artifact format: %q", art.Format)
	}
	if art.Strip < 0 {
		return fmt.Errorf("strip must not be negative")
	}
	if art.Format == "binary" && art.Strip != 0 {
		return fmt.Errorf("binary format requires strip 0")
	}
	return validateFiles(art.Files)
}

func (f *Fetcher) validate(art index.Artifact) error {
	if err := ValidateSpec(art, f.allow()); err != nil {
		return err
	}
	if !lockgraph.IsHexSHA256(art.SHA256) {
		return fmt.Errorf("sha256 must be 64 lowercase hex digits")
	}
	if !lockgraph.IsDigest(art.TreeDigest) {
		return fmt.Errorf("tree_digest must be a sha256 digest")
	}
	return nil
}

func (f *Fetcher) allow() func(string) bool {
	if f != nil && f.AllowHost != nil {
		return f.AllowHost
	}
	return index.AllowedHost
}

func checkName(name string) error {
	if name == ".tmp" || strings.HasPrefix(name, ".tmp-") {
		return fmt.Errorf("name %q is reserved for fetch staging", name)
	}
	return nil
}

func validateURL(raw string, allow func(string) bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("url scheme must be https")
	}
	if u.User != nil {
		return fmt.Errorf("url must not contain credentials")
	}
	if allow == nil || !allow(u.Hostname()) {
		return fmt.Errorf("host %q is not allowed", u.Hostname())
	}
	return nil
}

var allowedFormat = map[string]bool{
	"tar.gz": true,
	"tar.xz": true,
	"zip":    true,
	"binary": true,
}

var errOccupied = errors.New("occupied fetch directory")
