package fetch

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/index"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/store"
)

const (
	toolBody  = "just-bytes\n"
	extraBody = "readme\n"
	sha64A    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	treeDummy = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestToStoreTarGzStripPlacesMappedTree(t *testing.T) {
	fx := newFixture(t)
	art := fx.mappedTar()
	dest := mustToStore(t, fx, "1.56.0", art)

	got := filepath.Join(dest, "bin", "just")
	if body, err := os.ReadFile(got); err != nil || string(body) != toolBody {
		t.Errorf("bin/just = %q, %v, want %q", body, err, toolBody)
	}
	if perm := filePerm(t, got); perm != 0o755 {
		t.Errorf("bin/just mode = %o, want 0755", perm)
	}
	if _, err := os.Stat(filepath.Join(dest, "README")); !errors.Is(err, os.ErrNotExist) {
		t.Error("extra archive member was placed")
	}
	digest, err := provenance.DigestTree(context.Background(), dest)
	if err != nil {
		t.Fatalf("DigestTree: %v", err)
	}
	if digest != art.TreeDigest {
		t.Errorf("DigestTree = %q, want %q", digest, art.TreeDigest)
	}
	assertFetchSidecar(t, dest, "1.56.0", art)
	assertNoStaging(t, fx.store.Root)
}

func TestToStoreHashMismatchLeavesDestAbsent(t *testing.T) {
	fx := newFixture(t)
	art := fx.mappedTar()
	art.SHA256 = sha64A
	_, err := fx.fetcher.ToStore(context.Background(), fx.store, "just", "1.56.0", art)
	if !errors.Is(err, download.ErrSHA256Mismatch) {
		t.Fatalf("error = %v, want ErrSHA256Mismatch", err)
	}
	assertDestAbsent(t, fx, art.SHA256)
	assertNoStaging(t, fx.store.Root)
}

func TestToStoreTreeDigestMismatchLeavesDestAbsent(t *testing.T) {
	fx := newFixture(t)
	art := fx.mappedTar()
	art.TreeDigest = treeDummy
	_, err := fx.fetcher.ToStore(context.Background(), fx.store, "just", "1.56.0", art)
	if err == nil {
		t.Fatal("ToStore succeeded, want tree digest error")
	}
	assertDestAbsent(t, fx, art.SHA256)
	assertNoStaging(t, fx.store.Root)
}

func TestToStoreOccupiedDifferentDigestRefuses(t *testing.T) {
	fx := newFixture(t)
	art := fx.mappedTar()
	dest := mustFetchPath(t, fx.store, art.SHA256)
	plantTree(t, dest, map[string]string{"bin/other": "nope\n"}, 0o644)
	marker := filepath.Join(dest, "bin", "other")

	_, err := fx.fetcher.ToStore(context.Background(), fx.store, "just", "1.56.0", art)
	if err == nil {
		t.Fatal("ToStore succeeded, want occupied refuse")
	}
	got, err := os.ReadFile(marker)
	if err != nil || string(got) != "nope\n" {
		t.Errorf("dest mutated: %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "bin", "just")); !errors.Is(err, os.ErrNotExist) {
		t.Error("occupied dest was overwritten")
	}
}

func TestToStoreOccupiedSameDigestIsCacheHit(t *testing.T) {
	fx := newFixture(t)
	art := fx.mappedTar()
	dest := mustFetchPath(t, fx.store, art.SHA256)
	plantMappedJust(t, dest)
	before := fileIdent(t, filepath.Join(dest, "bin", "just"))

	got, err := fx.fetcher.ToStore(context.Background(), fx.store, "just", "1.56.0", art)
	if err != nil {
		t.Fatalf("ToStore: %v", err)
	}
	if got != dest {
		t.Errorf("dest = %q, want %q", got, dest)
	}
	after := fileIdent(t, filepath.Join(dest, "bin", "just"))
	if after != before {
		t.Errorf("payload rewritten on cache hit: %v → %v", before, after)
	}
	assertFetchSidecar(t, dest, "1.56.0", art)
	assertNoStaging(t, fx.store.Root)
}

func TestToStoreOccupiedSymlinkRefuses(t *testing.T) {
	fx := newFixture(t)
	art := fx.mappedTar()
	dest := mustFetchPath(t, fx.store, art.SHA256)
	target := t.TempDir()
	plantMappedJust(t, target)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, dest); err != nil {
		t.Fatal(err)
	}

	_, err := fx.fetcher.ToStore(context.Background(), fx.store, "just", "1.56.0", art)
	if err == nil {
		t.Fatal("ToStore succeeded, want symlink refuse")
	}
	fi, err := os.Lstat(dest)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("dest is no longer a symlink: %v, %v", fi, err)
	}
}

func TestToStoreMapFailuresLeaveDestAbsent(t *testing.T) {
	cases := []struct {
		name    string
		headers []tar.Header
		bodies  []string
		strip   int
		src     string
	}{
		{
			name:    "missing src",
			headers: []tar.Header{{Name: "prefix/README", Mode: 0o644}},
			bodies:  []string{extraBody},
			strip:   1,
			src:     "just",
		},
		{
			name: "directory src",
			headers: []tar.Header{
				{Name: "prefix/just/", Mode: 0o755, Typeflag: tar.TypeDir},
			},
			bodies: []string{""},
			strip:  1,
			src:    "just",
		},
		{
			name: "stripped src collision",
			headers: []tar.Header{
				{Name: "a/just", Mode: 0o755},
				{Name: "b/just", Mode: 0o755},
			},
			bodies: []string{toolBody, toolBody},
			strip:  1,
			src:    "just",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixture(t)
			archive := filepath.Join(t.TempDir(), "bad.tar.gz")
			writeTarGz(t, archive, tc.headers, tc.bodies)
			fx.serveFile(archive)
			art := fx.baseArt("tar.gz", tc.strip, []index.FileEntry{{
				Src: tc.src, Dest: "bin/just", Mode: 0o755,
			}})
			art.SHA256 = hashFile(t, archive)
			art.TreeDigest = treeDummy
			_, err := fx.fetcher.ToStore(context.Background(), fx.store, "just", "1.56.0", art)
			if err == nil {
				t.Fatal("ToStore succeeded, want map error")
			}
			assertDestAbsent(t, fx, art.SHA256)
			assertNoStaging(t, fx.store.Root)
		})
	}
}

func TestToStoreBinaryStripRefuses(t *testing.T) {
	hits := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits++
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(download.SetHTTPClient(srv.Client()))
	t.Cleanup(download.SetProgressEnabled(false))

	st := store.NewStore(t.TempDir())
	f := &Fetcher{AllowHost: func(string) bool { return true }}
	art := index.Artifact{
		URL:        srv.URL + "/just",
		Format:     "binary",
		SHA256:     sha64A,
		TreeDigest: treeDummy,
		Strip:      1,
		Files:      []index.FileEntry{{Src: "just", Dest: "bin/just", Mode: 0o755}},
	}
	_, err := f.ToStore(context.Background(), st, "just", "1.56.0", art)
	if err == nil {
		t.Fatal("ToStore succeeded, want binary strip refuse")
	}
	if hits != 0 {
		t.Errorf("HTTP hits = %d, want 0", hits)
	}
	assertDestAbsentStore(t, st, "1.56.0", art.SHA256)
}

func TestToStoreRefusesHostileArtifactBeforeFetch(t *testing.T) {
	hits := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits++
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(download.SetHTTPClient(srv.Client()))
	t.Cleanup(download.SetProgressEnabled(false))

	okFiles := []index.FileEntry{{Src: "just", Dest: "bin/just", Mode: 0o755}}
	cases := []struct {
		name string
		pkg  string
		art  index.Artifact
	}{
		{"unclean src", "just", artAt(srv.URL, okFiles, func(a *index.Artifact) { a.Files[0].Src = "../just" })},
		{"dest traversal", "just", artAt(srv.URL, okFiles, func(a *index.Artifact) { a.Files[0].Dest = "../x" })},
		{"overlap dest", "just", artAt(srv.URL, []index.FileEntry{
			{Src: "a", Dest: "bin", Mode: 0o755},
			{Src: "b", Dest: "bin/just", Mode: 0o755},
		}, nil)},
		{"bad mode", "just", artAt(srv.URL, okFiles, func(a *index.Artifact) { a.Files[0].Mode = 0o777 })},
		{"sidecar dest", "just", artAt(srv.URL, okFiles, func(a *index.Artifact) { a.Files[0].Dest = ".gale-provenance.toml" })},
		{"empty files", "just", artAt(srv.URL, nil, nil)},
		{"unknown format", "just", artAt(srv.URL, okFiles, func(a *index.Artifact) { a.Format = "tar.zst" })},
		{"bad sha", "just", artAt(srv.URL, okFiles, func(a *index.Artifact) { a.SHA256 = "aa" })},
		{"bad tree", "just", artAt(srv.URL, okFiles, func(a *index.Artifact) { a.TreeDigest = sha64A })},
		{"negative strip", "just", artAt(srv.URL, okFiles, func(a *index.Artifact) { a.Strip = -1 })},
		{"tmp name", ".tmp", artAt(srv.URL, okFiles, nil)},
		{"tmp prefix", ".tmp-x", artAt(srv.URL, okFiles, nil)},
		{"http url", "just", artAt("http://github.com/x/just", okFiles, nil)},
		{"credentials", "just", artAt("https://user:pass@github.com/x/just", okFiles, nil)},
		{"disallowed host", "just", artAt("https://example.com/just", okFiles, nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits = 0
			st := store.NewStore(t.TempDir())
			f := &Fetcher{}
			if tc.name != "disallowed host" && tc.name != "http url" && tc.name != "credentials" {
				f.AllowHost = func(string) bool { return true }
			}
			before := hits
			_, err := f.ToStore(context.Background(), st, tc.pkg, "1.56.0", tc.art)
			if err == nil {
				t.Fatal("ToStore succeeded, want refuse")
			}
			if hits != before {
				t.Errorf("HTTP hits = %d, want 0", hits)
			}
		})
	}
}

func TestToStoreSendsNoAuthorization(t *testing.T) {
	t.Setenv("GALE_GITHUB_TOKEN", "secret-token")
	t.Setenv("GITHUB_TOKEN", "secret-token")
	fx := newFixture(t)
	fx.sawAuth = new(bool)
	art := fx.mappedTar()
	mustToStore(t, fx, "1.56.0", art)
	if *fx.sawAuth {
		t.Error("request carried Authorization")
	}
}

func TestToStoreCanceledCtxLeavesDestAbsent(t *testing.T) {
	fx := newFixture(t)
	art := fx.mappedTar()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := fx.fetcher.ToStore(ctx, fx.store, "just", "1.56.0", art)
	if err == nil {
		t.Fatal("ToStore succeeded on canceled ctx")
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "canceled") {
		t.Errorf("error = %v, want canceled", err)
	}
	assertDestAbsent(t, fx, art.SHA256)
}

func TestToStoreBinaryAndZip(t *testing.T) {
	t.Run("binary", func(t *testing.T) {
		body := []byte(toolBody)
		fx := newRawFixture(t, "just", body)
		want := mappedDigest(t, map[string]fileSpec{"bin/just": {toolBody, 0o755}})
		art := fx.baseArt("binary", 0, []index.FileEntry{{
			Src: "just", Dest: "bin/just", Mode: 0o755,
		}})
		art.SHA256 = hexSHA(body)
		art.TreeDigest = want
		dest := mustToStore(t, fx, "1.0.0", art)
		got, err := os.ReadFile(filepath.Join(dest, "bin", "just"))
		if err != nil || string(got) != toolBody {
			t.Errorf("bin/just = %q, %v", got, err)
		}
		assertFetchSidecar(t, dest, "1.0.0", art)
	})
	t.Run("zip", func(t *testing.T) {
		archive := filepath.Join(t.TempDir(), "just.zip")
		writeZip(t, archive, map[string]string{
			"prefix/just":   toolBody,
			"prefix/README": extraBody,
		})
		fx := newRawFixture(t, "just.zip", mustRead(t, archive))
		want := mappedDigest(t, map[string]fileSpec{"bin/just": {toolBody, 0o755}})
		art := fx.baseArt("zip", 1, []index.FileEntry{{
			Src: "just", Dest: "bin/just", Mode: 0o755,
		}})
		art.SHA256 = hashFile(t, archive)
		art.TreeDigest = want
		dest := mustToStore(t, fx, "1.0.0", art)
		if _, err := os.Stat(filepath.Join(dest, "README")); !errors.Is(err, os.ErrNotExist) {
			t.Error("zip extra member was placed")
		}
		assertFetchSidecar(t, dest, "1.0.0", art)
	})
}

func TestToStoreEmptyLeftoverDestIsReplaced(t *testing.T) {
	fx := newFixture(t)
	art := fx.mappedTar()
	dest := mustFetchPath(t, fx.store, art.SHA256)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	got := mustToStore(t, fx, "1.56.0", art)
	if got != dest {
		t.Errorf("dest = %q, want %q", got, dest)
	}
	if _, err := os.Stat(filepath.Join(dest, "bin", "just")); err != nil {
		t.Errorf("mapped file missing: %v", err)
	}
}

func TestToStoreWriteFetchFailureKeepsPayload(t *testing.T) {
	fx := newFixture(t)
	art := fx.mappedTar()
	dest := mustFetchPath(t, fx.store, art.SHA256)
	plantMappedJust(t, dest)
	fx.fetcher.WriteFetch = func(string, provenance.FetchRecord) error {
		return errors.New("sidecar write failed")
	}
	_, err := fx.fetcher.ToStore(context.Background(), fx.store, "just", "1.56.0", art)
	if err == nil || !strings.Contains(err.Error(), "sidecar write failed") {
		t.Fatalf("error = %v, want sidecar write failed", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "bin", "just")); err != nil {
		t.Errorf("payload missing after sidecar failure: %v", err)
	}
	assertNoStaging(t, fx.store.Root)
}

type fixture struct {
	t       *testing.T
	store   *store.Store
	fetcher *Fetcher
	srv     *httptest.Server
	file    []byte
	urlPath string
	sawAuth *bool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	archive := filepath.Join(t.TempDir(), "just.tar.gz")
	writeTarGz(t, archive, []tar.Header{
		{Name: "prefix/just", Mode: 0o755},
		{Name: "prefix/README", Mode: 0o644},
	}, []string{toolBody, extraBody})
	return newRawFixture(t, "just.tar.gz", mustRead(t, archive))
}

func newRawFixture(t *testing.T, name string, body []byte) *fixture {
	t.Helper()
	fx := &fixture{
		t:       t,
		store:   store.NewStore(t.TempDir()),
		fetcher: &Fetcher{AllowHost: func(string) bool { return true }},
		file:    body,
		urlPath: "/" + name,
	}
	fx.startServer(body)
	return fx
}

func (fx *fixture) serveFile(pathOnDisk string) {
	fx.t.Helper()
	fx.startServer(mustRead(fx.t, pathOnDisk))
}

func (fx *fixture) startServer(body []byte) {
	fx.t.Helper()
	fx.file = body
	if fx.srv != nil {
		fx.srv.Close()
	}
	fx.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fx.sawAuth != nil && r.Header.Get("Authorization") != "" {
			*fx.sawAuth = true
		}
		_, _ = w.Write(body)
	}))
	fx.t.Cleanup(fx.srv.Close)
	fx.t.Cleanup(download.SetHTTPClient(fx.srv.Client()))
	fx.t.Cleanup(download.SetProgressEnabled(false))
}

func (fx *fixture) mappedTar() index.Artifact {
	fx.t.Helper()
	art := fx.baseArt("tar.gz", 1, []index.FileEntry{{
		Src: "just", Dest: "bin/just", Mode: 0o755,
	}})
	art.SHA256 = hexSHA(fx.file)
	art.TreeDigest = mappedDigest(fx.t, map[string]fileSpec{
		"bin/just": {toolBody, 0o755},
	})
	return art
}

func (fx *fixture) baseArt(format string, strip int, files []index.FileEntry) index.Artifact {
	return index.Artifact{
		URL:    fx.srv.URL + fx.urlPath,
		Format: format,
		Strip:  strip,
		Files:  files,
	}
}

func artAt(rawURL string, files []index.FileEntry, mut func(*index.Artifact)) index.Artifact {
	copied := append([]index.FileEntry(nil), files...)
	art := index.Artifact{
		URL:        rawURL,
		Format:     "binary",
		SHA256:     sha64A,
		TreeDigest: treeDummy,
		Files:      copied,
	}
	if mut != nil {
		mut(&art)
	}
	return art
}

const pkgJust = "just"

func mustToStore(t *testing.T, fx *fixture, version string, art index.Artifact) string {
	t.Helper()
	dest, err := fx.fetcher.ToStore(context.Background(), fx.store, pkgJust, version, art)
	if err != nil {
		t.Fatalf("ToStore: %v", err)
	}
	want, err := fx.store.FetchPath(pkgJust, version, art.SHA256)
	if err != nil {
		t.Fatalf("FetchPath: %v", err)
	}
	if dest != want {
		t.Errorf("ToStore dest = %q, want %q", dest, want)
	}
	return dest
}

func mustFetchPath(t *testing.T, st *store.Store, sha string) string {
	t.Helper()
	dest, err := st.FetchPath(pkgJust, "1.56.0", sha)
	if err != nil {
		t.Fatalf("FetchPath: %v", err)
	}
	return dest
}

func assertDestAbsent(t *testing.T, fx *fixture, sha string) {
	t.Helper()
	assertDestAbsentStore(t, fx.store, "1.56.0", sha)
}

func assertDestAbsentStore(t *testing.T, st *store.Store, version, sha string) {
	t.Helper()
	ok, err := st.FetchExists(pkgJust, version, sha)
	if err != nil {
		t.Fatalf("FetchExists: %v", err)
	}
	if ok {
		t.Error("FetchExists = true, want dest absent")
	}
}

func assertFetchSidecar(t *testing.T, dest, version string, art index.Artifact) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dest, provenance.File))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if !strings.Contains(string(data), `method = "fetch"`) {
		t.Errorf("sidecar missing method=fetch:\n%s", data)
	}
	if !strings.Contains(string(data), art.TreeDigest) {
		t.Errorf("sidecar missing tree_digest:\n%s", data)
	}
	if !strings.Contains(string(data), pkgJust) || !strings.Contains(string(data), version) {
		t.Errorf("sidecar missing identity %s@%s:\n%s", pkgJust, version, data)
	}
	_, err = provenance.ReadUnverified(dest)
	if !errors.Is(err, provenance.ErrInvalid) {
		t.Errorf("ReadUnverified error = %v, want ErrInvalid", err)
	}
}

func assertNoStaging(t *testing.T, root string) {
	t.Helper()
	ns := filepath.Join(root, store.FetchNamespace)
	entries, err := os.ReadDir(ns)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatalf("readdir fetch: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("staging leftover: %s", e.Name())
		}
	}
}

type fileSpec struct {
	body string
	mode os.FileMode
}

type payloadID struct {
	size int64
	mod  int64
}

func mappedDigest(t *testing.T, files map[string]fileSpec) string {
	t.Helper()
	dir := t.TempDir()
	plantTreeSpecs(t, dir, files)
	digest, err := provenance.DigestTree(context.Background(), dir)
	if err != nil {
		t.Fatalf("DigestTree fixture: %v", err)
	}
	return digest
}

func plantMappedJust(t *testing.T, dest string) {
	t.Helper()
	plantTreeSpecs(t, dest, map[string]fileSpec{"bin/just": {toolBody, 0o755}})
}

func plantTree(t *testing.T, dest string, files map[string]string, mode os.FileMode) {
	t.Helper()
	specs := make(map[string]fileSpec, len(files))
	for p, body := range files {
		specs[p] = fileSpec{body, mode}
	}
	plantTreeSpecs(t, dest, specs)
}

func plantTreeSpecs(t *testing.T, dest string, files map[string]fileSpec) {
	t.Helper()
	for rel, spec := range files {
		p := filepath.Join(dest, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(spec.body), spec.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, spec.mode); err != nil {
			t.Fatal(err)
		}
	}
}

func fileIdent(t *testing.T, path string) payloadID {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return payloadID{size: fi.Size(), mod: fi.ModTime().UnixNano()}
}

func filePerm(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func writeTarGz(t *testing.T, archivePath string, headers []tar.Header, bodies []string) {
	t.Helper()
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	for i, h := range headers {
		hdr := h
		if i < len(bodies) && bodies[i] != "" {
			hdr.Size = int64(len(bodies[i]))
		}
		if err := tw.WriteHeader(&hdr); err != nil {
			t.Fatal(err)
		}
		if i < len(bodies) && bodies[i] != "" {
			if _, err := tw.Write([]byte(bodies[i])); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeZip(t *testing.T, archivePath string, files map[string]string) {
	t.Helper()
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func hashFile(t *testing.T, path string) string {
	t.Helper()
	return hexSHA(mustRead(t, path))
}

func hexSHA(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
