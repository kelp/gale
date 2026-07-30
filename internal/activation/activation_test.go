package activation

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/provenance"
)

// Real 64-hex hashes: validation rejects anything else, so a
// placeholder would pass only by weakening the check it exercises.
const (
	shaJQ    = "1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a"
	shaOnig  = "2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b"
	shaCC    = "3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c"
	shaOther = "4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d"
)

const platform = "darwin/arm64"

// lockedGraph is the fixture closure: jq depends on oniguruma at
// runtime and is built from cc. jq is source, so its build edge is
// serialized, which is what makes the collected-build-dep case
// meaningful.
func lockedGraph() map[string]lockgraph.Node {
	return map[string]lockgraph.Node{
		"oniguruma@6.9.10-1": {
			Name: "oniguruma", Version: "6.9.10-1",
			GOOS: "darwin", GOARCH: "arm64",
			Method: lockgraph.MethodBinary, SHA256: shaOnig,
		},
		"cc@1.0-1": {
			Name: "cc", Version: "1.0-1",
			GOOS: "darwin", GOARCH: "arm64",
			Method: lockgraph.MethodBinary, SHA256: shaCC,
		},
		"jq@1.8.1-2": {
			Name: "jq", Version: "1.8.1-2",
			GOOS: "darwin", GOARCH: "arm64",
			Method: lockgraph.MethodSource, SHA256: shaJQ,
			Edges: []lockgraph.Edge{
				{Kind: lockgraph.KindRuntime, Name: "oniguruma", Version: "6.9.10-1"},
				{Kind: lockgraph.KindBuild, Name: "cc", Version: "1.0-1"},
			},
		},
	}
}

// v1Doc turns a node graph into the v1 lockfile that records it, with
// roots as the default target. Digests come from lockgraph.Closure so
// the fixture lock is internally consistent.
func v1Doc(t *testing.T, nodes map[string]lockgraph.Node, roots ...string) *lockfile.V1 {
	t.Helper()
	digests, _, err := lockgraph.Closure(nodes)
	if err != nil {
		t.Fatalf("Closure: %v", err)
	}
	pkgs := make(map[string]lockfile.Package, len(nodes))
	for key, n := range nodes {
		pkgs[key] = lockfile.Package{Artifacts: map[string]lockfile.Artifact{
			platform: {
				SHA256:         n.SHA256,
				ManifestDigest: n.ManifestDigest,
				Method:         n.Method,
				RuntimeDeps:    identities(n, lockgraph.KindRuntime),
				BuildDeps:      identities(n, lockgraph.KindBuild),
				GraphDigest:    digests[key],
			},
		}}
	}
	return &lockfile.V1{
		Version:  lockfile.SchemaVersion,
		Targets:  lockfile.Targets{Default: &lockfile.Target{Roots: roots}},
		Packages: pkgs,
	}
}

// writeLock persists a v1 document and returns its path, so every
// test drives Check through the same schema classification a real
// activation does.
func writeLock(t *testing.T, lf *lockfile.V1) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gale.lock")
	if err := lockfile.WriteV1(path, lf); err != nil {
		t.Fatalf("WriteV1: %v", err)
	}
	return path
}

func identities(n lockgraph.Node, kind lockgraph.Kind) []string {
	var out []string
	for _, e := range n.Edges {
		if e.Kind == kind {
			out = append(out, lockgraph.Key(e.Name, e.Version))
		}
	}
	return out
}

// install writes the provenance a locked install would have left for
// one node, into an explicit directory basename so a test can put a
// canonical identity in a differently named directory.
func install(t *testing.T, storeRoot string, nodes map[string]lockgraph.Node, key, dir string) {
	t.Helper()
	digests, _, err := lockgraph.Closure(nodes)
	if err != nil {
		t.Fatalf("Closure: %v", err)
	}
	n := nodes[key]
	target := filepath.Join(storeRoot, n.Name, dir)
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	buildDeps := identities(n, lockgraph.KindBuild)
	if n.Method != lockgraph.MethodSource {
		buildDeps = nil
	}
	rec := provenance.Record{
		Name: n.Name, Version: n.Version,
		Platform: platform, SHA256: n.SHA256,
		ManifestDigest: n.ManifestDigest, Method: n.Method,
		RuntimeDeps: identities(n, lockgraph.KindRuntime),
		BuildDeps:   buildDeps,
		GraphDigest: digests[key],
	}
	if err := provenance.Write(target, rec); err != nil {
		t.Fatalf("Write provenance for %s: %v", key, err)
	}
}

// installClosure provisions every node at its canonical directory.
func installClosure(t *testing.T, storeRoot string, nodes map[string]lockgraph.Node) {
	t.Helper()
	for key, n := range nodes {
		install(t, storeRoot, nodes, key, n.Version)
	}
}

// fixture is the common arrangement: the whole closure installed, the
// lock written, jq the only root.
type fixture struct {
	storeRoot string
	nodes     map[string]lockgraph.Node
	lockPath  string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	f := fixture{storeRoot: t.TempDir(), nodes: lockedGraph()}
	installClosure(t, f.storeRoot, f.nodes)
	f.lockPath = writeLock(t, v1Doc(t, f.nodes, "jq@1.8.1-2"))
	return f
}

func (f fixture) request(installed map[string]string) Request {
	return Request{
		LockPath:  f.lockPath,
		Platform:  platform,
		StoreRoot: f.storeRoot,
		Installed: installed,
	}
}

// wholeGeneration is what a correct sync of this fixture leaves in the
// active generation: the source root plus its runtime dep.
func wholeGeneration() map[string]string {
	return map[string]string{"jq": "1.8.1-2", "oniguruma": "6.9.10-1"}
}

// TestCheckAcceptsAVerifiedGeneration is the baseline: a generation
// whose every runtime store dir carries provenance agreeing with the
// lock activates.
func TestCheckAcceptsAVerifiedGeneration(t *testing.T) {
	f := newFixture(t)
	if err := Check(f.request(wholeGeneration())); err != nil {
		t.Fatalf("Check: %v", err)
	}
}

// TestCheckHashesNothing pins design §12's "the gate reads provenance
// files and the lockfile only; it hashes nothing". A store dir holding
// nothing but its provenance record still activates, which is only
// possible if no artifact bytes are read. The rule is not an
// optimization: the darwin fixup mutates staged bytes after the
// artifact hash is taken, so a directory hash can never equal the
// artifact SHA the lock records.
func TestCheckHashesNothing(t *testing.T) {
	f := newFixture(t)

	entries, err := os.ReadDir(filepath.Join(f.storeRoot, "jq", "1.8.1-2"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("fixture should hold only the provenance file, has %d entries",
			len(entries))
	}

	if err := Check(f.request(wholeGeneration())); err != nil {
		t.Fatalf("Check: %v", err)
	}
}

// TestCheckToleratesCollectedBuildDeps pins that a source root
// verifies after its build dependency has been collected. Design §12
// permits exactly this, which is why the gate compares against a
// digest recomputed from the lock instead of calling
// provenance.VerifyAgainstStore, which would fail here.
func TestCheckToleratesCollectedBuildDeps(t *testing.T) {
	f := newFixture(t)
	if err := os.RemoveAll(filepath.Join(f.storeRoot, "cc")); err != nil {
		t.Fatal(err)
	}
	if err := Check(f.request(wholeGeneration())); err != nil {
		t.Fatalf("Check with cc collected: %v", err)
	}
}

// TestCheckWalksTransitiveRuntimeDeps pins that the walk goes past the
// generation's own entries. A transitive runtime dep is not a
// generation entry at all, so a gate that checked only roots would
// miss a tampered dependency entirely.
func TestCheckWalksTransitiveRuntimeDeps(t *testing.T) {
	f := newFixture(t)
	// oniguruma is reachable only through jq's runtime edge.
	if err := os.Remove(filepath.Join(
		f.storeRoot, "oniguruma", "6.9.10-1", provenance.File,
	)); err != nil {
		t.Fatal(err)
	}

	err := Check(f.request(map[string]string{"jq": "1.8.1-2"}))
	if !errors.Is(err, provenance.ErrInvalid) {
		t.Fatalf("want provenance.ErrInvalid for an unprovenanced "+
			"transitive dep, got %v", err)
	}
	if errors.Is(err, ErrDrift) {
		t.Errorf("a tampered dependency is an integrity conflict, "+
			"not activation drift: %v", err)
	}
}

// TestCheckDetectsIntegrityConflict pins that a provenance record
// disagreeing with the lock is an integrity conflict, kept distinct
// from drift so a carried-forward package never reads as tampering.
func TestCheckDetectsIntegrityConflict(t *testing.T) {
	storeRoot := t.TempDir()
	installClosure(t, storeRoot, lockedGraph())

	// The lock now names a different artifact for oniguruma than the
	// bytes on disk were verified as.
	tampered := lockedGraph()
	onig := tampered["oniguruma@6.9.10-1"]
	onig.SHA256 = shaOther
	tampered["oniguruma@6.9.10-1"] = onig

	err := Check(Request{
		LockPath:  writeLock(t, v1Doc(t, tampered, "jq@1.8.1-2")),
		Platform:  platform,
		StoreRoot: storeRoot,
		Installed: wholeGeneration(),
	})
	if !errors.Is(err, provenance.ErrInvalid) {
		t.Fatalf("want provenance.ErrInvalid, got %v", err)
	}
	if errors.Is(err, ErrDrift) {
		t.Errorf("an artifact conflict is not drift: %v", err)
	}
}

// TestCheckRefusesALegacyLock is acceptance 21's unit half. A legacy
// lock records checksums nothing ever verified, so activation under
// one is refused. Nothing here depends on an mtime: the gate runs on
// every activation, which is what makes a gale upgrade detectable at
// all.
func TestCheckRefusesALegacyLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gale.lock")
	body := "[packages.jq]\nversion = \"1.8.1\"\nsha256 = \"" + shaJQ + "\"\n"
	//nolint:gosec // world-readable like every other lockfile
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	err := Check(Request{
		LockPath:  path,
		Platform:  platform,
		StoreRoot: t.TempDir(),
		Installed: map[string]string{"jq": "1.8.1-1"},
	})
	if !errors.Is(err, lockfile.ErrLegacySchema) {
		t.Fatalf("want lockfile.ErrLegacySchema, got %v", err)
	}
}

// TestCheckWithNoLockIsUnlocked pins that an absent lock is not a
// failure. Unlocked mode has to stay an ordinary path, or every
// project without a lock is refused activation.
func TestCheckWithNoLockIsUnlocked(t *testing.T) {
	err := Check(Request{
		LockPath:  filepath.Join(t.TempDir(), "gale.lock"),
		Platform:  platform,
		StoreRoot: t.TempDir(),
		Installed: map[string]string{"jq": "1.8.1-1"},
	})
	if err != nil {
		t.Fatalf("absent lock should activate unlocked, got %v", err)
	}
}

// TestCheckDetectsCarryForward is acceptance 34's mechanism seen
// through the gate: a generation entry the lock does not name is
// drift, remedied by `gale sync`, and explicitly not an integrity
// conflict.
func TestCheckDetectsCarryForward(t *testing.T) {
	f := newFixture(t)
	// The previous generation's jq, carried forward because the newly
	// pinned one was never installed. Its provenance is perfectly
	// sound, which is the point: nothing was tampered with.
	install(t, f.storeRoot, f.nodes, "jq@1.8.1-2", "1.8.0-1")

	err := Check(f.request(map[string]string{
		"jq": "1.8.0-1", "oniguruma": "6.9.10-1",
	}))
	if !errors.Is(err, ErrDrift) {
		t.Fatalf("want ErrDrift, got %v", err)
	}
	if errors.Is(err, provenance.ErrInvalid) {
		t.Errorf("carry-forward must not read as tampering: %v", err)
	}
	if !strings.Contains(err.Error(), "jq@1.8.0-1") {
		t.Errorf("error should name the unlocked identity, got %q", err)
	}
}

// TestUnlockedVersions is acceptance 34 directly. The predicate
// answers one question — which installed versions does the lock not
// name — and both the gate and locked sync's validation callback ask
// it, so there is exactly one implementation of the comparison.
func TestUnlockedVersions(t *testing.T) {
	storeRoot := t.TempDir()
	// jq exists at two revisions and at a bare pre-revision dir.
	for _, dir := range []string{"1.8.1-2", "1.8.0-1", "1.7"} {
		if err := os.MkdirAll(filepath.Join(storeRoot, "jq", dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	lockedJQ := map[string]string{"jq": "jq@1.8.1-2"}

	cases := []struct {
		name      string
		installed map[string]string
		locked    map[string]string
		want      []string
	}{
		{
			name:      "the locked version",
			installed: map[string]string{"jq": "1.8.1-2"},
			locked:    lockedJQ,
			want:      nil,
		},
		{
			name:      "a carried-forward version",
			installed: map[string]string{"jq": "1.8.0-1"},
			locked:    lockedJQ,
			want:      []string{"jq@1.8.0-1"},
		},
		{
			name:      "a package absent from the lock",
			installed: map[string]string{"ripgrep": "14.1.0-1"},
			locked:    lockedJQ,
			want:      []string{"ripgrep@14.1.0-1"},
		},
		{
			// A canonical "-1" identity legitimately lives in a bare
			// directory for installs predating revisions. Comparing
			// basenames verbatim would report every one of those as
			// unlocked and refuse activation for a correct store.
			name:      "a revision-one identity in a bare directory",
			installed: map[string]string{"jq": "1.7"},
			locked:    map[string]string{"jq": "jq@1.7-1"},
			want:      nil,
		},
		{
			name:      "nothing installed",
			installed: map[string]string{},
			locked:    lockedJQ,
			want:      nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := UnlockedVersions(tc.installed, tc.locked, storeRoot)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("UnlockedVersions = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestUnlockedVersionsIsSorted pins deterministic output: the list
// reaches a user-visible error message, and map iteration order is
// unstable by design.
func TestUnlockedVersionsIsSorted(t *testing.T) {
	got := UnlockedVersions(
		map[string]string{"zsh": "5.9-1", "atuin": "18.0-1", "mise": "2024.1-1"},
		map[string]string{},
		t.TempDir(),
	)
	want := []string{"atuin@18.0-1", "mise@2024.1-1", "zsh@5.9-1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("UnlockedVersions = %v, want %v", got, want)
	}
}

// TestCheckRefusesAnUnmodelableLock covers the two ways a locked node
// cannot answer for what is installed. Both are lock-unusable rather
// than something the gate resolves live: resolving it is exactly what
// the lock exists to prevent.
func TestCheckRefusesAnUnmodelableLock(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(t *testing.T, req *Request, lf *lockfile.V1)
		wantErr error
	}{
		{
			name: "a runtime edge the lock does not define",
			mutate: func(t *testing.T, req *Request, lf *lockfile.V1) {
				t.Helper()
				delete(lf.Packages, "oniguruma@6.9.10-1")
				req.LockPath = writeLock(t, lf)
			},
			wantErr: lockfile.ErrMissingNode,
		},
		{
			name: "no artifact for this platform",
			mutate: func(t *testing.T, req *Request, _ *lockfile.V1) {
				t.Helper()
				req.Platform = "linux/amd64"
			},
			wantErr: lockfile.ErrMissingArtifact,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			req := f.request(map[string]string{"jq": "1.8.1-2"})
			tc.mutate(t, &req, v1Doc(t, f.nodes, "jq@1.8.1-2"))
			if err := Check(req); !errors.Is(err, tc.wantErr) {
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}
		})
	}
}
