package installer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/recipe"
)

// fakeSHA is a well-formed hash for a dependency whose bytes no test
// ever fetches. Provenance only requires 64 hex digits here; what the
// digest binds is the graph, not the archive.
const fakeSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// binaryFixture is one served archive: the tar entries, the URL that
// serves them, and the hash the recipe must declare.
type binaryFixture struct {
	url    string
	sha256 string
}

// serveBinary publishes a tar.zst over a test server and returns what
// a recipe needs to fetch it.
func serveBinary(t *testing.T, entries []u3TarEntry) binaryFixture {
	t.Helper()
	archive := createU3TarZstd(t, entries)
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, archive)
		},
	))
	t.Cleanup(srv.Close)
	return binaryFixture{
		url:    srv.URL + "/pkg.tar.zst",
		sha256: hashFile(t, archive),
	}
}

// binaryRecipe is a recipe whose only install path is the fixture's
// prebuilt archive, trusted by hash alone so no attestation is needed.
func binaryRecipe(name, version string, f binaryFixture) *recipe.Recipe {
	return &recipe.Recipe{
		Package: recipe.Package{Name: name, Version: version},
		Binary: map[string]recipe.Binary{
			runtime.GOOS + "-" + runtime.GOARCH: {
				URL:    f.url,
				SHA256: f.sha256,
				Trust:  recipe.TrustSHA256Only,
			},
		},
	}
}

// stagedInstall is one binary install to run: which package, into
// which store, from which archive, over which declared dependencies.
type stagedInstall struct {
	storeRoot string
	name      string
	version   string // canonical version-revision
	fixture   binaryFixture
	deps      []depsmeta.ResolvedDep
}

// installBinary runs one staged binary install and returns the staging
// dir it committed into. Staged (inPlace = false) is the mode under
// test on purpose: installBinaryTo does not promote the package into
// the store there, so a record found afterwards was written before the
// canonical directory existed.
func installBinary(t *testing.T, s stagedInstall) string {
	t.Helper()
	staging := filepath.Join(t.TempDir(), "staged")
	canonical := filepath.Join(s.storeRoot, s.name, s.version)
	inst := &Installer{}
	if err := inst.installBinaryTo(
		context.Background(),
		binaryRecipe(s.name, strings.TrimSuffix(s.version, "-1"), s.fixture),
		extractDest{staging, canonical, false}, s.deps,
	); err != nil {
		t.Fatalf("install %s: %v", s.name, err)
	}
	return staging
}

// provenanceDep installs a dependency directory carrying a truthful
// leaf provenance record, so a package above it can be attested.
func provenanceDep(t *testing.T, storeRoot, name, version string) provenance.Record {
	t.Helper()
	dir := filepath.Join(storeRoot, name, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create dep dir: %v", err)
	}
	rec, err := provenance.New(storeRoot, leafNode(name, version))
	if err != nil {
		t.Fatalf("build dep record: %v", err)
	}
	if err := provenance.Write(dir, rec); err != nil {
		t.Fatalf("write dep record: %v", err)
	}
	return rec
}

// sourceOf is a minimal, valid source artifact for a test that
// exercises extraction rather than provenance. recordProvenance treats
// an unusable *closure* as "no record" but an unnamable artifact as a
// caller bug, so callers must pass a real identity. No hash: the
// extraction path takes it from the build result.
func sourceOf(name, version string) commitArtifact {
	return commitArtifact{
		Name:    name,
		Version: version,
		Method:  lockgraph.MethodSource,
	}
}

// leafNode is a dependency-free binary node for this platform.
func leafNode(name, version string) lockgraph.Node {
	return lockgraph.Node{
		Name:    name,
		Version: version,
		GOOS:    runtime.GOOS,
		GOARCH:  runtime.GOARCH,
		Method:  lockgraph.MethodBinary,
		SHA256:  fakeSHA,
	}
}

// TestInstallBinary_WritesProvenanceBeforeCommit is the first P3b
// case: a serialized leaf. A binary with no runtime dependencies
// serializes no edges, so its record can always be completed, and it
// must be in the directory before that directory is promoted.
func TestInstallBinary_WritesProvenanceBeforeCommit(t *testing.T) {
	storeRoot := filepath.Join(t.TempDir(), "pkg")
	f := serveBinary(t, []u3TarEntry{
		{name: "bin/tool", content: "#!/bin/sh\necho hi\n", mode: 0o755},
	})
	staging := installBinary(t, stagedInstall{
		storeRoot: storeRoot, name: "leafbin", version: "1.0-1", fixture: f,
	})

	got, err := provenance.ReadUnverified(staging)
	if err != nil {
		t.Fatalf("read provenance from staged dir: %v", err)
	}
	// The digest is taken over the archive's real hash, not
	// leafNode's placeholder, so rebuild the expectation from what
	// was actually served.
	n := leafNode("leafbin", "1.0-1")
	n.SHA256 = f.sha256
	want, err := lockgraph.Digest(n, nil)
	if err != nil {
		t.Fatalf("expected digest: %v", err)
	}

	if got.Key() != "leafbin@1.0-1" {
		t.Errorf("key = %q, want leafbin@1.0-1", got.Key())
	}
	if got.Method != lockgraph.MethodBinary {
		t.Errorf("method = %q, want binary", got.Method)
	}
	if got.SHA256 != f.sha256 {
		t.Errorf("sha256 = %q, want %q", got.SHA256, f.sha256)
	}
	if got.Platform != runtime.GOOS+"/"+runtime.GOARCH {
		t.Errorf("platform = %q", got.Platform)
	}
	if len(got.RuntimeDeps) != 0 || len(got.BuildDeps) != 0 {
		t.Errorf("leaf record carries deps: %+v", got)
	}
	if got.GraphDigest != want {
		t.Errorf("graph_digest = %q, want %q", got.GraphDigest, want)
	}
	if _, err := os.Stat(filepath.Join(storeRoot, "leafbin", "1.0-1")); !os.IsNotExist(err) {
		t.Errorf("canonical dir exists before the caller committed: %v", err)
	}
}

// TestInstallBinary_ProvenanceOverProvenancedDep covers a fully fresh
// chain: every serialized edge has usable provenance, so the parent
// gets a record whose digest binds the dependency's own digest rather
// than its identity.
func TestInstallBinary_ProvenanceOverProvenancedDep(t *testing.T) {
	storeRoot := filepath.Join(t.TempDir(), "pkg")
	dep := provenanceDep(t, storeRoot, "leafdep", "1.0-1")

	f := serveBinary(t, []u3TarEntry{
		{name: "bin/parent", content: "#!/bin/sh\n", mode: 0o755},
	})
	staging := installBinary(t, stagedInstall{
		storeRoot: storeRoot, name: "parentbin", version: "2.0-1", fixture: f,
		deps: []depsmeta.ResolvedDep{{Name: "leafdep", Version: "1.0", Revision: 1}},
	})

	got, err := provenance.ReadUnverified(staging)
	if err != nil {
		t.Fatalf("read parent provenance: %v", err)
	}
	if len(got.RuntimeDeps) != 1 || got.RuntimeDeps[0] != "leafdep@1.0-1" {
		t.Fatalf("runtime_deps = %v, want [leafdep@1.0-1]", got.RuntimeDeps)
	}
	n := leafNode("parentbin", "2.0-1")
	n.SHA256 = f.sha256
	n.Edges = []lockgraph.Edge{
		{Kind: lockgraph.KindRuntime, Name: "leafdep", Version: "1.0-1"},
	}
	want, err := lockgraph.Digest(n, map[string]string{
		"leafdep@1.0-1": dep.GraphDigest,
	})
	if err != nil {
		t.Fatalf("expected digest: %v", err)
	}
	if got.GraphDigest != want {
		t.Errorf("graph_digest = %q, want %q", got.GraphDigest, want)
	}
}

// TestInstallBinary_UnprovenancedDepCommitsWithoutProvenance is the
// all-or-nothing rule on an upgraded machine: the dependency predates
// provenance, so no digest exists for the edge. The install still
// succeeds and the artifact commits with no record at all — a partial
// one would attest a closure nothing verified.
func TestInstallBinary_UnprovenancedDepCommitsWithoutProvenance(t *testing.T) {
	storeRoot := filepath.Join(t.TempDir(), "pkg")
	if err := os.MkdirAll(filepath.Join(storeRoot, "legacydep", "1.0-1"), 0o755); err != nil {
		t.Fatalf("create legacy dep: %v", err)
	}

	f := serveBinary(t, []u3TarEntry{
		{name: "bin/parent", content: "#!/bin/sh\n", mode: 0o755},
	})
	staging := installBinary(t, stagedInstall{
		storeRoot: storeRoot, name: "parentbin", version: "2.0-1", fixture: f,
		deps: []depsmeta.ResolvedDep{{Name: "legacydep", Version: "1.0", Revision: 1}},
	})

	if _, err := os.Stat(filepath.Join(staging, provenance.File)); !os.IsNotExist(err) {
		t.Errorf("provenance written over an unprovenanced dep: %v", err)
	}
	if _, err := os.Stat(filepath.Join(staging, "bin", "parent")); err != nil {
		t.Errorf("install did not commit its payload: %v", err)
	}
}

// TestInstallBinary_UnusableDepProvenanceCommitsWithoutProvenance
// pins the other half of "unusable": a dependency that has a record
// which does not survive verification is treated exactly as one with
// no record. Believing it would let a hand-edited file certify an
// arbitrary closure above it.
func TestInstallBinary_UnusableDepProvenanceCommitsWithoutProvenance(t *testing.T) {
	storeRoot := filepath.Join(t.TempDir(), "pkg")
	dep := provenanceDep(t, storeRoot, "liardep", "1.0-1")
	corruptDigest(t, filepath.Join(storeRoot, "liardep", "1.0-1"), dep.GraphDigest)

	f := serveBinary(t, []u3TarEntry{
		{name: "bin/parent", content: "#!/bin/sh\n", mode: 0o755},
	})
	staging := installBinary(t, stagedInstall{
		storeRoot: storeRoot, name: "parentbin", version: "2.0-1", fixture: f,
		deps: []depsmeta.ResolvedDep{{Name: "liardep", Version: "1.0", Revision: 1}},
	})

	if _, err := os.Stat(filepath.Join(staging, provenance.File)); !os.IsNotExist(err) {
		t.Errorf("provenance written over an unusable dep record: %v", err)
	}
}

// TestInstallBinary_ArchiveDepsWinOverFallback proves the record
// describes what the committed directory declares. An archive built by
// `gale build` ships its own .gale-deps.toml, which the installer
// keeps, so reading the caller's fallback list instead would attest a
// closure the package does not name.
//
// The shipped entry is unresolvable on purpose: if the fallback were
// the source of edges, the record would exist.
func TestInstallBinary_ArchiveDepsWinOverFallback(t *testing.T) {
	storeRoot := filepath.Join(t.TempDir(), "pkg")
	provenanceDep(t, storeRoot, "leafdep", "1.0-1")

	f := serveBinary(t, []u3TarEntry{
		{name: "bin/parent", content: "#!/bin/sh\n", mode: 0o755},
		{
			name:    depsmeta.File,
			content: "[[deps]]\nname = \"ghostdep\"\nversion = \"9.9\"\nrevision = 1\n",
			mode:    0o644,
		},
	})
	staging := installBinary(t, stagedInstall{
		storeRoot: storeRoot, name: "parentbin", version: "2.0-1", fixture: f,
		deps: []depsmeta.ResolvedDep{{Name: "leafdep", Version: "1.0", Revision: 1}},
	})

	if _, err := os.Stat(filepath.Join(staging, provenance.File)); !os.IsNotExist(err) {
		t.Errorf("record built from the fallback list, not the staged file: %v", err)
	}
}

// TestInstallBinary_TraversalDepNameCommitsWithoutProvenance covers a
// hostile archive. A .gale-deps.toml naming "../../evil" is untrusted
// input, and a dependency gale cannot name canonically is unusable —
// which under the all-or-nothing rule means no record, not a failed
// install. P3b adds a file; it does not start rejecting packages that
// installed yesterday.
func TestInstallBinary_TraversalDepNameCommitsWithoutProvenance(t *testing.T) {
	storeRoot := filepath.Join(t.TempDir(), "pkg")
	f := serveBinary(t, []u3TarEntry{
		{name: "bin/parent", content: "#!/bin/sh\n", mode: 0o755},
		{
			name:    depsmeta.File,
			content: "[[deps]]\nname = \"../../evil\"\nversion = \"1.0\"\nrevision = 1\n",
			mode:    0o644,
		},
	})
	staging := installBinary(t, stagedInstall{
		storeRoot: storeRoot, name: "parentbin", version: "2.0-1", fixture: f,
	})

	if _, err := os.Stat(filepath.Join(staging, provenance.File)); !os.IsNotExist(err) {
		t.Errorf("record written over a traversal dependency name: %v", err)
	}
	if _, err := os.Stat(filepath.Join(staging, "bin", "parent")); err != nil {
		t.Errorf("hostile metadata failed the install: %v", err)
	}
}

// TestInstallBinary_UnparsableArchiveDepsCommitsWithoutProvenance is
// the same rule for a metadata file that does not decode at all.
// Archives with a malformed .gale-deps.toml install today — this one
// spells revision as a string — and P3b must not start rejecting them.
func TestInstallBinary_UnparsableArchiveDepsCommitsWithoutProvenance(t *testing.T) {
	storeRoot := filepath.Join(t.TempDir(), "pkg")
	f := serveBinary(t, []u3TarEntry{
		{name: "bin/parent", content: "#!/bin/sh\n", mode: 0o755},
		{
			name:    depsmeta.File,
			content: "[[deps]]\nname = \"leafdep\"\nversion = \"1.0\"\nrevision = \"42\"\n",
			mode:    0o644,
		},
	})
	staging := installBinary(t, stagedInstall{
		storeRoot: storeRoot, name: "parentbin", version: "2.0-1", fixture: f,
	})

	if _, err := os.Stat(filepath.Join(staging, provenance.File)); !os.IsNotExist(err) {
		t.Errorf("record written over unreadable dep metadata: %v", err)
	}
	if _, err := os.Stat(filepath.Join(staging, "bin", "parent")); err != nil {
		t.Errorf("malformed metadata failed the install: %v", err)
	}
}

// TestRecordProvenance_ScrubsArchiveSuppliedRecord is the forgery
// case. An archive can ship .gale-provenance.toml. Every branch that
// declines to write a record leaves the staging dir alone, so a
// planted file would be committed as though gale produced it — and the
// file's whole meaning is that gale verified these bytes.
func TestRecordProvenance_ScrubsArchiveSuppliedRecord(t *testing.T) {
	storeRoot := filepath.Join(t.TempDir(), "pkg")
	staging := t.TempDir()
	plant := filepath.Join(staging, provenance.File)
	if err := os.WriteFile(plant, []byte("name = \"forged\"\n"), 0o644); err != nil {
		t.Fatalf("plant record: %v", err)
	}
	// An unresolvable dependency, so nothing overwrites the plant.
	writeStagedDeps(t, staging,
		"[[deps]]\nname = \"ghost\"\nversion = \"9.9\"\nrevision = 1\n")

	a := sourceOf("srcpkg", "2.0-1")
	a.SHA256 = fakeSHA
	if err := recordProvenance(storeRoot, staging, a); err != nil {
		t.Fatalf("recordProvenance: %v", err)
	}
	if _, err := os.Stat(plant); !os.IsNotExist(err) {
		t.Errorf("archive-supplied record survived the commit: %v", err)
	}
}

// TestRecordProvenance_ScrubDoesNotFollowSymlink covers the write
// half. The extractor deliberately lets an absolute symlink through on
// the stated grounds that no later write follows one — its own writes
// pass O_NOFOLLOW. provenance.Write uses os.WriteFile, which does not,
// so the planted link must be unlinked rather than opened.
func TestRecordProvenance_ScrubDoesNotFollowSymlink(t *testing.T) {
	storeRoot := filepath.Join(t.TempDir(), "pkg")
	outside := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(outside, []byte("original"), 0o644); err != nil {
		t.Fatalf("create victim: %v", err)
	}
	staging := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(staging, provenance.File)); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}
	// A leaf closure, so a record really is written.
	writeStagedDeps(t, staging, "")

	a := sourceOf("srcpkg", "2.0-1")
	a.SHA256 = fakeSHA
	if err := recordProvenance(storeRoot, staging, a); err != nil {
		t.Fatalf("recordProvenance: %v", err)
	}
	victim, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if string(victim) != "original" {
		t.Errorf("wrote through the symlink: victim is now %q", victim)
	}
	fi, err := os.Lstat(filepath.Join(staging, provenance.File))
	if err != nil {
		t.Fatalf("lstat staged record: %v", err)
	}
	if !fi.Mode().IsRegular() {
		t.Errorf("staged record is %v, want a regular file", fi.Mode())
	}
}

// TestInstallBinary_DanglingDepsSymlinkStaysInsideStaging closes the
// write half of the metadata boundary. depsmeta.Has uses os.Stat,
// which follows a symlink, so an archive shipping .gale-deps.toml as a
// DANGLING absolute symlink was reported absent and depsmeta.Write's
// os.WriteFile then created the target outside the store. strictDeps
// cannot help: the synthesis runs first.
func TestInstallBinary_DanglingDepsSymlinkStaysInsideStaging(t *testing.T) {
	storeRoot := filepath.Join(t.TempDir(), "pkg")
	victim := filepath.Join(t.TempDir(), "victim.toml")
	f := serveBinary(t, []u3TarEntry{
		{name: "bin/parent", content: "#!/bin/sh\n", mode: 0o755},
		{name: depsmeta.File, link: victim},
	})
	staging := installBinary(t, stagedInstall{
		storeRoot: storeRoot, name: "parentbin", version: "2.0-1", fixture: f,
		deps: []depsmeta.ResolvedDep{{Name: "leafdep", Version: "1.0", Revision: 1}},
	})

	if _, err := os.Lstat(victim); !os.IsNotExist(err) {
		t.Errorf("wrote metadata outside the store: %v", err)
	}
	fi, err := os.Lstat(filepath.Join(staging, depsmeta.File))
	if err != nil {
		t.Fatalf("lstat staged metadata: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("staged metadata is %v, want the archive's symlink preserved", fi.Mode())
	}
	// A closure read through a symlink is not one gale can attest.
	if _, err := os.Stat(filepath.Join(staging, provenance.File)); !os.IsNotExist(err) {
		t.Errorf("record written over symlinked metadata: %v", err)
	}
}

// TestStrictDeps rejects everything a record must not be built from.
// depsmeta.Read accepts most of these, correctly: the farm and gc
// tolerate a partial answer, while a record is a claim about one exact
// closure.
func TestStrictDeps(t *testing.T) {
	valid := "[[deps]]\nname = \"libfoo\"\nversion = \"1.0\"\nrevision = 2\n"
	tests := []struct {
		name    string
		content string
		wantOK  bool
	}{
		{"valid", valid, true},
		{"empty file", "", true},
		{
			"zero revision is the pre-revision default",
			"[[deps]]\nname = \"libfoo\"\nversion = \"1.0\"\n", true,
		},
		{
			"negative revision",
			"[[deps]]\nname = \"libfoo\"\nversion = \"1.0\"\nrevision = -3\n", false,
		},
		{
			"empty version canonicalizes to a plausible \"-1\"",
			"[[deps]]\nname = \"libfoo\"\nversion = \"\"\nrevision = 1\n", false,
		},
		{
			"empty name",
			"[[deps]]\nname = \"\"\nversion = \"1.0\"\nrevision = 1\n", false,
		},
		{"unknown field", valid + "extra = true\n", false},
		{"malformed toml", "[[deps]\nname =", false},
		{
			"revision spelled as a string",
			"[[deps]]\nname = \"libfoo\"\nversion = \"1.0\"\nrevision = \"2\"\n", false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeStagedDeps(t, dir, tc.content)
			if _, ok := strictDeps(dir); ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
			}
		})
	}
}

// TestStrictDeps_AbsentIsAZeroDepPackage separates "no dependencies"
// from "cannot tell": a package with none is completely described, so
// it must still earn a record.
func TestStrictDeps_AbsentIsAZeroDepPackage(t *testing.T) {
	deps, ok := strictDeps(t.TempDir())
	if !ok || len(deps) != 0 {
		t.Errorf("deps = %v, ok = %v; want empty and usable", deps, ok)
	}
}

// TestStrictDeps_SymlinkIsUnusable stops attestation input from being
// read out of the store. os.ReadFile follows a symlink, so without the
// mode check an archive could point this name anywhere readable and
// have the contents describe the package's closure.
func TestStrictDeps_SymlinkIsUnusable(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "elsewhere.toml")
	err := os.WriteFile(outside,
		[]byte("[[deps]]\nname = \"libfoo\"\nversion = \"1.0\"\nrevision = 1\n"), 0o644)
	if err != nil {
		t.Fatalf("create outside file: %v", err)
	}
	dir := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, depsmeta.File)); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}

	if deps, ok := strictDeps(dir); ok {
		t.Errorf("symlinked metadata reported usable as %v", deps)
	}
}

// writeStagedDeps writes a .gale-deps.toml into a staging dir.
func writeStagedDeps(t *testing.T, dir, content string) {
	t.Helper()
	path := filepath.Join(dir, depsmeta.File)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write staged deps: %v", err)
	}
}

// corruptDigest replaces a written record's graph digest with a
// well-formed one that does not match the record's own closure, which
// is what an occupied-but-unusable directory looks like.
func corruptDigest(t *testing.T, dir, digest string) {
	t.Helper()
	path := filepath.Join(dir, provenance.File)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	fake := "sha256:" + strings.Repeat("b", 64)
	out := strings.Replace(string(data), digest, fake, 1)
	if out == string(data) {
		t.Fatalf("digest %q not found in record", digest)
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		t.Fatalf("write corrupted record: %v", err)
	}
}
