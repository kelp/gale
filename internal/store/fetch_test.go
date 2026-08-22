package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Pre-change trace (tier 2 — store identity).
//
// Invariant: source identity stays <root>/<name>/<version>-<revision>/.
// Fetch trees live only under fetch/ and are never a source package
// named "fetch". Two distinct artifact hashes of one name@version
// are two directories.
//
// Pipeline: none. Store layout only. No install, sync, finalize,
// generation, or lock writer.
//
// Caller grep: store.List → cmd/gale/gc.go
// removeUnreferencedVersions, doctor.go, inspect.go, migrate.go.
// StorePath / ResolveDir / IsInstalled / Remove / Create /
// CheckIdentity stay source-facing. FetchPath / FetchExists have
// no callers yet.
//
// Command surface: no CLI change. gale gc / list / doctor /
// inspect keep working because List skips fetch/.
//
// Test anchors: this file. Package-layer is correct;
// internal/store is not a pipeline-sensitive path.
//
// Blast radius: if List emits fetch/, the next gale gc calls
// Remove("fetch", "<pkg>") and deletes every pin. If the
// reserved name is not enforced, IsInstalled("fetch", "jq")
// is true and Remove deletes the namespace. If FetchPath does
// not validate, a lock field can escape the store.

const (
	fetchSHA256A = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	fetchSHA256B = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	// All-digit prefix: 012345678901 would look like a revision
	// if it lived under <root>/jq/.
	fetchSHA256Digits = "0123456789012345678901234567890123456789012345678901234567890123"
)

func fetchSHA12(sha256 string) string {
	return strings.ToLower(sha256)[:12]
}

func writeMarker(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "marker"), []byte("x"), 0o644); err != nil {
		t.Fatalf("marker %s: %v", dir, err)
	}
}

func fixtureFetchDir(root, name, version, sha256 string) string {
	return filepath.Join(root, FetchNamespace, name, version+"-"+fetchSHA12(sha256))
}

func TestFetchPathSpelling(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	got, err := s.FetchPath("jq", "1.7.1", fetchSHA256A)
	if err != nil {
		t.Fatalf("FetchPath: %v", err)
	}
	want := filepath.Join(root, FetchNamespace, "jq", "1.7.1-"+fetchSHA12(fetchSHA256A))
	if got != want {
		t.Errorf("FetchPath = %q, want %q", got, want)
	}
}

func TestFetchPathDoesNotCreateDirectory(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	got, err := s.FetchPath("jq", "1.7.1", fetchSHA256A)
	if err != nil {
		t.Fatalf("FetchPath: %v", err)
	}
	if _, err := os.Stat(got); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("FetchPath created %q: %v", got, err)
	}
}

func TestFetchPathNormalizesDigestCase(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	lower, err := s.FetchPath("jq", "1.7.1", fetchSHA256A)
	if err != nil {
		t.Fatalf("lowercase: %v", err)
	}
	upper, err := s.FetchPath("jq", "1.7.1", strings.ToUpper(fetchSHA256A))
	if err != nil {
		t.Fatalf("uppercase: %v", err)
	}
	if lower != upper {
		t.Errorf("case folded paths differ: %q vs %q", lower, upper)
	}
}

func TestFetchPathRejectsInvalidIdentity(t *testing.T) {
	s := NewStore(t.TempDir())
	cases := []struct {
		name, version, sha256 string
	}{
		{"../outside", "1.7.1", fetchSHA256A},
		{"foo/bar", "1.7.1", fetchSHA256A},
		{"jq", "..", fetchSHA256A},
		{"jq", "1.7.1", "aaaa"},
		{"jq", "1.7.1", "sha256:" + fetchSHA256A},
		{FetchNamespace, "1.7.1", fetchSHA256A},
	}
	for _, tc := range cases {
		label := tc.name + "@" + tc.version
		if tc.sha256 != fetchSHA256A {
			label += "/" + tc.sha256
		}
		t.Run(label, func(t *testing.T) {
			got, err := s.FetchPath(tc.name, tc.version, tc.sha256)
			if err == nil {
				t.Fatalf("FetchPath = %q, want error", got)
			}
		})
	}
}

func TestFetchExistsTwoArtifactsSameVersion(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	writeMarker(t, fixtureFetchDir(root, "jq", "1.7.1", fetchSHA256A))
	writeMarker(t, fixtureFetchDir(root, "jq", "1.7.1", fetchSHA256B))

	gotA, err := s.FetchExists("jq", "1.7.1", fetchSHA256A)
	if err != nil {
		t.Fatalf("FetchExists A: %v", err)
	}
	gotB, err := s.FetchExists("jq", "1.7.1", fetchSHA256B)
	if err != nil {
		t.Fatalf("FetchExists B: %v", err)
	}
	if !gotA || !gotB {
		t.Errorf("FetchExists A,B = %v,%v, want true,true", gotA, gotB)
	}

	missingSHA := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	gotC, err := s.FetchExists("jq", "1.7.1", missingSHA)
	if err != nil {
		t.Fatalf("FetchExists missing: %v", err)
	}
	if gotC {
		t.Error("FetchExists for other sha = true, want false")
	}
}

func TestFetchExistsDoesNotFallBackToSource(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	writeMarker(t, filepath.Join(root, "jq", "1.7.1-1"))

	got, err := s.FetchExists("jq", "1.7.1", fetchSHA256A)
	if err != nil {
		t.Fatalf("FetchExists: %v", err)
	}
	if got {
		t.Error("FetchExists fell back to source layout")
	}
}

func TestFetchExistsEmptyDirIsAbsent(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	dir := fixtureFetchDir(root, "jq", "1.7.1", fetchSHA256A)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := s.FetchExists("jq", "1.7.1", fetchSHA256A)
	if err != nil {
		t.Fatalf("FetchExists: %v", err)
	}
	if got {
		t.Error("empty fetch dir counted as present")
	}
}

func TestSourceAPIsIgnoreFetchNamespaceMixedLayout(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	source := filepath.Join(root, "jq", "1.7.1-1")
	writeMarker(t, source)
	writeMarker(t, fixtureFetchDir(root, "jq", "1.7.1", fetchSHA256A))

	got, ok := s.resolveVersion("jq", "1.7.1")
	if !ok || got != "1.7.1-1" {
		t.Errorf("resolveVersion = %q, %v, want 1.7.1-1, true", got, ok)
	}
	if !s.IsInstalled("jq", "1.7.1") {
		t.Error("IsInstalled = false, want true")
	}
	path, ok := s.StorePath("jq", "1.7.1")
	if !ok || path != source {
		t.Errorf("StorePath = %q, %v, want %q, true", path, ok, source)
	}

	pkgs, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(pkgs) != 1 || pkgs[0].Name != "jq" || pkgs[0].Version != "1.7.1-1" {
		t.Errorf("List = %+v, want [{jq 1.7.1-1}]", pkgs)
	}
}

func TestSourceAPIsMissFetchOnlyLayout(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	writeMarker(t, fixtureFetchDir(root, "jq", "1.7.1", fetchSHA256A))

	if _, ok := s.StorePath("jq", "1.7.1"); ok {
		t.Error("StorePath found a fetch tree as a source install")
	}
	if s.IsInstalled("jq", "1.7.1") {
		t.Error("IsInstalled = true, want false")
	}
	got := s.ResolveDir("jq", "1.7.1")
	want := filepath.Join(root, "jq", "1.7.1")
	if got != want {
		t.Errorf("ResolveDir = %q, want literal source path %q", got, want)
	}
}

func TestAllDigitSHA12DoesNotWinBareLookup(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	writeMarker(t, fixtureFetchDir(root, "jq", "1.7.1", fetchSHA256Digits))

	got, ok := s.resolveVersion("jq", "1.7.1")
	if ok {
		t.Errorf("resolveVersion = %q, true; all-digit sha12 won a bare lookup", got)
	}
	if s.IsInstalled("jq", "1.7.1") {
		t.Error("IsInstalled = true for fetch-only all-digit sha12")
	}
}

func TestReservedNameRejectedOnSourceAPIs(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	writeMarker(t, fixtureFetchDir(root, "jq", "1.7.1", fetchSHA256A))

	if _, err := s.Create(FetchNamespace, "1.2.3"); !errors.Is(err, ErrReservedName) {
		t.Errorf("Create error = %v, want ErrReservedName", err)
	}
	if err := CheckIdentity(FetchNamespace, "1.0.0-1"); !errors.Is(err, ErrReservedName) {
		t.Errorf("CheckIdentity error = %v, want ErrReservedName", err)
	}
	if s.IsInstalled(FetchNamespace, "jq") {
		t.Error("IsInstalled(fetch, jq) = true, want false")
	}
	if path, ok := s.StorePath(FetchNamespace, "jq"); ok {
		t.Errorf("StorePath(fetch, jq) = %q, true; want not found", path)
	}
	got := s.ResolveDir(FetchNamespace, "jq")
	if got == "" {
		t.Fatal("ResolveDir returned empty string")
	}
	nsDir := filepath.Join(root, FetchNamespace, "jq")
	if got == nsDir {
		t.Errorf("ResolveDir returned the namespace package dir %q", got)
	}
	if _, err := os.Stat(got); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ResolveDir path %q exists: %v", got, err)
	}
	if bin := filepath.Join(got, "bin"); !filepath.IsAbs(bin) {
		t.Errorf("Join(ResolveDir, bin) = %q is relative", bin)
	}
	rel, err := filepath.Rel(root, got)
	if err != nil || strings.HasPrefix(rel, "..") {
		t.Errorf("ResolveDir = %q is not under store root %q", got, root)
	}
	if err := s.Remove(FetchNamespace, "jq"); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("Remove error = %v, want ErrNotInstalled", err)
	}
	if _, err := os.Stat(fixtureFetchDir(root, "jq", "1.7.1", fetchSHA256A)); err != nil {
		t.Errorf("Remove deleted the fetch tree: %v", err)
	}
}

func TestListSkipsFetchNamespace(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	writeMarker(t, fixtureFetchDir(root, "jq", "1.7.1", fetchSHA256A))
	writeMarker(t, fixtureFetchDir(root, "fd", "10.0.0", fetchSHA256B))

	pkgs, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(pkgs) != 0 {
		t.Errorf("List = %+v, want empty (gc must not see fetch/)", pkgs)
	}
}

func TestSweepTransientDoesNotDeleteFetchTree(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	dir := fixtureFetchDir(root, "jq", "1.7.1", fetchSHA256A)
	writeMarker(t, dir)
	// Age the tree so a mistaken sweep would consider it.
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}

	swept := s.SweepTransient(time.Hour, false)
	if len(swept) != 0 {
		t.Errorf("SweepTransient = %v, want none", swept)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("fetch tree missing after sweep: %v", err)
	}
}

func TestListFetchExactIdentities(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	writeMarker(t, fixtureFetchDir(root, "jq", "1.7.1", fetchSHA256A))
	writeMarker(t, fixtureFetchDir(root, "jq", "1.7.1", fetchSHA256B))
	writeMarker(t, fixtureFetchDir(root, "fd", "10.0.0", fetchSHA256A))
	// Prefix sibling and staging must not appear as identities.
	writeMarker(t, filepath.Join(root, FetchNamespace, "jq", "1.7.1"))
	if err := os.Mkdir(filepath.Join(root, FetchNamespace, ".tmp-live"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListFetch()
	if err != nil {
		t.Fatalf("ListFetch: %v", err)
	}
	want := []FetchIdentity{
		{Name: "fd", Version: "10.0.0", SHA12: fetchSHA12(fetchSHA256A)},
		{Name: "jq", Version: "1.7.1", SHA12: fetchSHA12(fetchSHA256A)},
		{Name: "jq", Version: "1.7.1", SHA12: fetchSHA12(fetchSHA256B)},
	}
	if len(got) != len(want) {
		t.Fatalf("ListFetch = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ListFetch[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestListFetchParsesVersionWithHyphens(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	writeMarker(t, fixtureFetchDir(root, "just", "1.48.0-rc.1", fetchSHA256A))

	got, err := s.ListFetch()
	if err != nil {
		t.Fatalf("ListFetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListFetch = %+v, want one identity", got)
	}
	if got[0].Version != "1.48.0-rc.1" || got[0].SHA12 != fetchSHA12(fetchSHA256A) {
		t.Errorf("ListFetch = %+v, want version 1.48.0-rc.1 + sha12", got[0])
	}
}

func TestRemoveFetchDeletesExactIdentity(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	keep := fixtureFetchDir(root, "jq", "1.7.1", fetchSHA256A)
	drop := fixtureFetchDir(root, "jq", "1.7.1", fetchSHA256B)
	writeMarker(t, keep)
	writeMarker(t, drop)

	if err := s.RemoveFetch("jq", "1.7.1", fetchSHA12(fetchSHA256B)); err != nil {
		t.Fatalf("RemoveFetch: %v", err)
	}
	if _, err := os.Stat(drop); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("removed identity still present: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("sibling identity deleted: %v", err)
	}
}

func TestRemoveFetchRefusesIncompleteAndReserved(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	keep := fixtureFetchDir(root, "jq", "1.7.1", fetchSHA256A)
	writeMarker(t, keep)
	prefix := filepath.Join(root, FetchNamespace, "jq", "1.7.1")
	writeMarker(t, prefix)

	cases := []struct {
		name, version, sha string
	}{
		{FetchNamespace, "jq", fetchSHA12(fetchSHA256A)},
		{"jq", "1.7.1", ""},
		{"jq", "1.7.1", "aaaa"},
		{"jq", "1.7.1", fetchSHA256A},
		{"jq", "1.7.1-aaaaaaaaaaaa", ""},
	}
	for _, tc := range cases {
		if err := s.RemoveFetch(tc.name, tc.version, tc.sha); err == nil {
			t.Errorf("RemoveFetch(%q, %q, %q) = nil, want error",
				tc.name, tc.version, tc.sha)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("refused RemoveFetch deleted the identity: %v", err)
	}
	if _, err := os.Stat(prefix); err != nil {
		t.Errorf("refused RemoveFetch deleted the prefix sibling: %v", err)
	}
}

func TestFetchStagingAlive(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	writeMarker(t, fixtureFetchDir(root, "jq", "1.7.1", fetchSHA256A))

	alive, paths, err := s.FetchStagingAlive()
	if err != nil {
		t.Fatalf("FetchStagingAlive empty: %v", err)
	}
	if alive || len(paths) != 0 {
		t.Errorf("FetchStagingAlive = %v, %v; want false, none", alive, paths)
	}

	staging := filepath.Join(root, FetchNamespace, ".tmp-live")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	alive, paths, err = s.FetchStagingAlive()
	if err != nil {
		t.Fatalf("FetchStagingAlive: %v", err)
	}
	if !alive {
		t.Error("FetchStagingAlive = false, want true")
	}
	if len(paths) != 1 || paths[0] != staging {
		t.Errorf("FetchStagingAlive paths = %v, want [%s]", paths, staging)
	}
}
