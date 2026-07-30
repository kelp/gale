package lockwrite

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/provenance"
)

// Hashes are the real 64-hex-digit shape. lockgraph rejects anything
// else, and a placeholder would only pass by weakening the check.
const (
	shaJQ   = "1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a"
	shaOnig = "2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b"
	digestM = "sha256:3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c"
)

const platform = "darwin/arm64"

func onigNode() lockgraph.Node {
	return lockgraph.Node{
		Name:    "oniguruma",
		Version: "6.9.10-1",
		GOOS:    "darwin",
		GOARCH:  "arm64",
		Method:  lockgraph.MethodBinary,
		SHA256:  shaOnig,
	}
}

func jqNode() lockgraph.Node {
	return lockgraph.Node{
		Name:           "jq",
		Version:        "1.8.1-2",
		GOOS:           "darwin",
		GOARCH:         "arm64",
		Method:         lockgraph.MethodBinary,
		SHA256:         shaJQ,
		ManifestDigest: digestM,
		Edges: []lockgraph.Edge{
			{Kind: lockgraph.KindRuntime, Name: "oniguruma", Version: "6.9.10-1"},
		},
	}
}

// install writes n's provenance into the store through the real
// writer, so a fixture can never assert a record shape the installer
// would not produce. Dependencies must be installed before their
// parents: a parent's digest is computed from theirs.
func install(t *testing.T, storeRoot string, n lockgraph.Node) provenance.Record {
	t.Helper()
	r, err := provenance.New(storeRoot, n)
	if err != nil {
		t.Fatalf("provenance.New(%s): %v", n.Name, err)
	}
	dir := filepath.Join(storeRoot, n.Name, n.Version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir store dir: %v", err)
	}
	if err := provenance.Write(dir, r); err != nil {
		t.Fatalf("provenance.Write(%s): %v", n.Name, err)
	}
	return r
}

// installChain installs oniguruma then jq, the shape most tests need.
func installChain(t *testing.T, storeRoot string) (onig, jq provenance.Record) {
	t.Helper()
	onig = install(t, storeRoot, onigNode())
	jq = install(t, storeRoot, jqNode())
	return onig, jq
}

// TestBuildEmitsTheWholeClosure: the lock records the closure, not the
// roots. A writer that emitted only roots would produce a lock whose
// every transitive edge points at a node the reader cannot find, which
// plan construction reports as a missing node rather than as the
// writer bug it is.
func TestBuildEmitsTheWholeClosure(t *testing.T) {
	storeRoot := t.TempDir()
	onig, jq := installChain(t, storeRoot)

	res, err := Build(Request{
		StoreRoot: storeRoot,
		Platform:  platform,
		Roots:     map[string]string{"jq": "1.8.1-2"},
		Declared:  map[string]string{"jq": "1.8.1"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := res.Doc

	want := &lockfile.V1{
		Version: lockfile.SchemaVersion,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{Roots: []string{"jq@1.8.1-2"}},
		},
		Packages: map[string]lockfile.Package{
			"jq@1.8.1-2": {Artifacts: map[string]lockfile.Artifact{
				platform: {
					SHA256:         shaJQ,
					ManifestDigest: digestM,
					Method:         lockgraph.MethodBinary,
					RuntimeDeps:    []string{"oniguruma@6.9.10-1"},
					GraphDigest:    jq.GraphDigest,
				},
			}},
			"oniguruma@6.9.10-1": {Artifacts: map[string]lockfile.Artifact{
				platform: {
					SHA256:      shaOnig,
					Method:      lockgraph.MethodBinary,
					GraphDigest: onig.GraphDigest,
				},
			}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Build =\n %#v\nwant\n %#v", got, want)
	}
}

// artifactWith builds a plausible artifact. Its exact values never
// matter; what matters is whether it survives a rewrite.
func artifactWith(sha string, deps []string) lockfile.Artifact {
	return lockfile.Artifact{
		SHA256:      sha,
		Method:      lockgraph.MethodBinary,
		RuntimeDeps: deps,
		GraphDigest: digestM,
	}
}

// TestBuildPreservesOtherTargetsAndDropsOrphans: one target's rewrite
// must not evict another target's graph, and must not accumulate nodes
// no target reaches any more. Reachability is computed over the
// existing document's own edges and crosses platforms: a node only a
// linux target needs is still referenced while darwin is rewritten.
func TestBuildPreservesOtherTargetsAndDropsOrphans(t *testing.T) {
	storeRoot := t.TempDir()
	_, jq := installChain(t, storeRoot)

	existing := &lockfile.V1{
		Version: lockfile.SchemaVersion,
		Targets: lockfile.Targets{
			Host: map[string]lockfile.Target{
				"linux-box": {Roots: []string{"ripgrep@14.1.0-1"}},
			},
		},
		Packages: map[string]lockfile.Package{
			"ripgrep@14.1.0-1": {Artifacts: map[string]lockfile.Artifact{
				"linux/amd64": artifactWith(
					shaOnig, []string{"pcre2@10.44-1"},
				),
			}},
			"pcre2@10.44-1": {Artifacts: map[string]lockfile.Artifact{
				"linux/amd64": artifactWith(shaJQ, nil),
			}},
			// Reached by no target: a previous root that was removed.
			"abandoned@1.0-1": {Artifacts: map[string]lockfile.Artifact{
				"linux/amd64": artifactWith(shaJQ, nil),
			}},
		},
	}

	res, err := Build(Request{
		StoreRoot: storeRoot,
		Platform:  platform,
		Roots:     map[string]string{"jq": "1.8.1-2"},
		Declared:  map[string]string{"jq": "1.8.1"},
		Existing:  existing,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := res.Doc

	wantKeys := []string{
		"jq@1.8.1-2", "oniguruma@6.9.10-1",
		"pcre2@10.44-1", "ripgrep@14.1.0-1",
	}
	gotKeys := slices.Sorted(maps.Keys(got.Packages))
	if !slices.Equal(gotKeys, wantKeys) {
		t.Errorf("package keys = %v, want %v", gotKeys, wantKeys)
	}
	if _, ok := got.Targets.Host["linux-box"]; !ok {
		t.Error("the linux-box target was dropped by a default-target rewrite")
	}
	if got.Packages["jq@1.8.1-2"].Artifacts[platform].GraphDigest != jq.GraphDigest {
		t.Error("jq's rewritten artifact does not carry its verified digest")
	}
}

// TestBuildOtherPlatformArtifacts: a foreign-platform hash is evidence
// about a node, so it survives exactly while that node is unchanged.
// Once the node's own artifact changes, the foreign entry describes a
// package that no longer exists and must be dropped, forcing a re-lock
// on that platform rather than leaving a stale hash that looks locked.
func TestBuildOtherPlatformArtifacts(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(a lockfile.Artifact) lockfile.Artifact
		wantFor bool
	}{
		{
			name:    "unchanged node keeps the foreign artifact",
			mutate:  func(a lockfile.Artifact) lockfile.Artifact { return a },
			wantFor: true,
		},
		{
			name: "changed digest drops it",
			mutate: func(a lockfile.Artifact) lockfile.Artifact {
				a.GraphDigest = digestM
				return a
			},
			wantFor: false,
		},
		{
			name: "changed method drops it",
			mutate: func(a lockfile.Artifact) lockfile.Artifact {
				a.Method = lockgraph.MethodSource
				return a
			},
			wantFor: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			storeRoot := t.TempDir()
			_, jq := installChain(t, storeRoot)

			// The prior document's darwin artifact, which the rewrite
			// replaces. tc.mutate makes it agree or disagree with what
			// the store now says.
			prior := lockfile.Artifact{
				SHA256:         shaJQ,
				ManifestDigest: digestM,
				Method:         lockgraph.MethodBinary,
				RuntimeDeps:    []string{"oniguruma@6.9.10-1"},
				GraphDigest:    jq.GraphDigest,
			}
			existing := &lockfile.V1{
				Version: lockfile.SchemaVersion,
				Targets: lockfile.Targets{
					Default: &lockfile.Target{Roots: []string{"jq@1.8.1-2"}},
				},
				Packages: map[string]lockfile.Package{
					"jq@1.8.1-2": {Artifacts: map[string]lockfile.Artifact{
						platform:      tc.mutate(prior),
						"linux/amd64": artifactWith(shaOnig, nil),
					}},
					"oniguruma@6.9.10-1": {Artifacts: map[string]lockfile.Artifact{
						platform: {
							SHA256:      shaOnig,
							Method:      lockgraph.MethodBinary,
							GraphDigest: onigDigest(t, storeRoot),
						},
					}},
				},
			}

			res, err := Build(Request{
				StoreRoot: storeRoot,
				Platform:  platform,
				Roots:     map[string]string{"jq": "1.8.1-2"},
				Declared:  map[string]string{"jq": "1.8.1"},
				Existing:  existing,
			})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			got := res.Doc
			_, kept := got.Packages["jq@1.8.1-2"].Artifacts["linux/amd64"]
			if kept != tc.wantFor {
				t.Errorf("foreign artifact kept = %v, want %v", kept, tc.wantFor)
			}
			if got.Packages["jq@1.8.1-2"].Artifacts[platform].GraphDigest != jq.GraphDigest {
				t.Error("the rewritten darwin artifact is not the verified one")
			}
		})
	}
}

// onigDigest reads the dependency's verified digest back out of the
// store, so a fixture never hard-codes a digest the formula owns.
func onigDigest(t *testing.T, storeRoot string) string {
	t.Helper()
	r, err := provenance.ReadUnverified(
		filepath.Join(storeRoot, "oniguruma", "6.9.10-1"),
	)
	if err != nil {
		t.Fatalf("read dep provenance: %v", err)
	}
	return r.GraphDigest
}

// TestBuildDoesNotMutateExisting: the caller's Existing is the
// document it read from disk. A Build that failed after mutating it
// would leave the caller believing the file on disk holds something it
// does not, and the atomic-write rule rests on that belief.
func TestBuildDoesNotMutateExisting(t *testing.T) {
	storeRoot := t.TempDir()
	installChain(t, storeRoot)

	existing := &lockfile.V1{
		Version: lockfile.SchemaVersion,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{Roots: []string{"ripgrep@14.1.0-1"}},
			Host: map[string]lockfile.Target{
				"linux-box": {Roots: []string{"ripgrep@14.1.0-1"}},
			},
		},
		Packages: map[string]lockfile.Package{
			"ripgrep@14.1.0-1": {Artifacts: map[string]lockfile.Artifact{
				"linux/amd64": artifactWith(shaOnig, nil),
			}},
		},
	}
	before := deepCopy(t, existing)

	if _, err := Build(Request{
		StoreRoot: storeRoot,
		Platform:  platform,
		Target:    "darwin-box",
		Roots:     map[string]string{"jq": "1.8.1-2"},
		Declared:  map[string]string{"jq": "1.8.1"},
		Existing:  existing,
	}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !reflect.DeepEqual(existing, before) {
		t.Errorf("Build mutated Existing:\n got %#v\nwant %#v", existing, before)
	}
}

// deepCopy round-trips through the schema's own encoder, so the
// comparison covers exactly what would be written.
func deepCopy(t *testing.T, lf *lockfile.V1) *lockfile.V1 {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gale.lock")
	if err := lockfile.WriteV1(path, lf); err != nil {
		t.Fatalf("WriteV1: %v", err)
	}
	got, err := lockfile.ReadV1(path)
	if err != nil {
		t.Fatalf("ReadV1: %v", err)
	}
	return got
}

// existingWithRipgrep returns a prior document whose default target
// roots jq and ripgrep, with ripgrep's own subgraph recorded. dep is
// the identity ripgrep depends on, so a caller can make that edge
// dangle by naming a node the document does not define.
func existingWithRipgrep(dep string) *lockfile.V1 {
	pkgs := map[string]lockfile.Package{
		"ripgrep@14.1.0-1": {Artifacts: map[string]lockfile.Artifact{
			platform: artifactWith(shaOnig, []string{dep}),
		}},
	}
	if dep == "pcre2@10.44-1" {
		pkgs[dep] = lockfile.Package{Artifacts: map[string]lockfile.Artifact{
			platform: artifactWith(shaJQ, nil),
		}}
	}
	return &lockfile.V1{
		Version: lockfile.SchemaVersion,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{
				Roots: []string{"jq@1.8.1-2", "ripgrep@14.1.0-1"},
			},
		},
		Packages: pkgs,
	}
}

// TestBuildCarriesForwardRootsItDidNotInstall: installing one package
// must not degrade a complete committed lock into a one-root one.
//
// This is the fresh-clone case the codebase already accommodates
// elsewhere with a lenient generation rebuild (gh#23): gale.toml
// declares packages this host has not installed, so their closures
// cannot be verified here. Their prior locked subgraphs are still
// valid evidence, so they are carried forward rather than dropped.
func TestBuildCarriesForwardRootsItDidNotInstall(t *testing.T) {
	storeRoot := t.TempDir()
	installChain(t, storeRoot)

	res, err := Build(Request{
		StoreRoot: storeRoot,
		Platform:  platform,
		Roots:     map[string]string{"jq": "1.8.1-2"},
		Declared:  map[string]string{"jq": "1.8.1", "ripgrep": "14.1.0"},
		Existing:  existingWithRipgrep("pcre2@10.44-1"),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	wantRoots := []string{"jq@1.8.1-2", "ripgrep@14.1.0-1"}
	if !slices.Equal(res.Doc.Targets.Default.Roots, wantRoots) {
		t.Errorf("roots = %v, want %v", res.Doc.Targets.Default.Roots, wantRoots)
	}
	// The carried root is worthless without its own subgraph: a root
	// with no node reads as a tampered lock, not a stale one.
	for _, key := range []string{"ripgrep@14.1.0-1", "pcre2@10.44-1"} {
		if _, ok := res.Doc.Packages[key]; !ok {
			t.Errorf("carried subgraph lost %s", key)
		}
	}
	if len(res.Unlocked) != 0 {
		t.Errorf("Unlocked = %v, want none", res.Unlocked)
	}
}

// TestBuildOmitsWhatItCannotBack: a declared package the writer can
// back neither by fresh verification nor by a complete carried
// subgraph is omitted from roots entirely.
//
// Omitting produces a stale lock, which is a defined and recoverable
// state with named remedies. The alternative, writing the root anyway,
// produces a lock that looks like it covers the package and must fail
// when read: the same defect the design rejects for partially
// derivable platforms.
func TestBuildOmitsWhatItCannotBack(t *testing.T) {
	tests := []struct {
		name         string
		declared     map[string]string
		existing     *lockfile.V1
		wantRoots    []string
		wantUnlocked []string
		wantAbsent   []string
	}{
		{
			name: "declared but never locked and not installed",
			declared: map[string]string{
				"jq": "1.8.1", "fd": "9.0.0",
			},
			existing:     existingWithRipgrep("pcre2@10.44-1"),
			wantRoots:    []string{"jq@1.8.1-2"},
			wantUnlocked: []string{"fd"},
		},
		{
			name: "carried root whose declared pin changed",
			declared: map[string]string{
				"jq": "1.8.1", "ripgrep": "15.0.0",
			},
			existing:     existingWithRipgrep("pcre2@10.44-1"),
			wantRoots:    []string{"jq@1.8.1-2"},
			wantUnlocked: []string{"ripgrep"},
			wantAbsent:   []string{"ripgrep@14.1.0-1", "pcre2@10.44-1"},
		},
		{
			name: "carried root with a dangling edge",
			declared: map[string]string{
				"jq": "1.8.1", "ripgrep": "14.1.0",
			},
			existing:     existingWithRipgrep("vanished@9.9-1"),
			wantRoots:    []string{"jq@1.8.1-2"},
			wantUnlocked: []string{"ripgrep"},
			wantAbsent:   []string{"ripgrep@14.1.0-1", "vanished@9.9-1"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			storeRoot := t.TempDir()
			installChain(t, storeRoot)

			res, err := Build(Request{
				StoreRoot: storeRoot,
				Platform:  platform,
				Roots:     map[string]string{"jq": "1.8.1-2"},
				Declared:  tc.declared,
				Existing:  tc.existing,
			})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if !slices.Equal(res.Doc.Targets.Default.Roots, tc.wantRoots) {
				t.Errorf("roots = %v, want %v",
					res.Doc.Targets.Default.Roots, tc.wantRoots)
			}
			if !slices.Equal(res.Unlocked, tc.wantUnlocked) {
				t.Errorf("Unlocked = %v, want %v", res.Unlocked, tc.wantUnlocked)
			}
			// A partial subgraph must not be written: it is exactly the
			// "looks locked and must fail" document.
			for _, key := range tc.wantAbsent {
				if _, ok := res.Doc.Packages[key]; ok {
					t.Errorf("%s was written for an unbacked root", key)
				}
			}
		})
	}
}

// TestBuildRetainedForeignArtifactKeepsItsClosure: a retained foreign
// artifact carries its own dependency edges, so its nodes must be
// retained with it.
//
// Preserving the artifact while dropping the node it points at produces
// exactly the document this writer must never emit: a recorded edge
// with no node behind it, which reads as a tampered lock rather than a
// stale one. When the prior document cannot supply that closure, the
// honest move is to drop the foreign artifact and force a re-lock on
// that platform.
func TestBuildRetainedForeignArtifactKeepsItsClosure(t *testing.T) {
	tests := []struct {
		name string
		// depPlatforms are the platforms the dependency node records.
		// nil means the node is absent from the document entirely.
		depPlatforms []string
		wantKept     bool
	}{
		{
			name:         "foreign dep present for that platform is retained",
			depPlatforms: []string{"linux/amd64"},
			wantKept:     true,
		},
		{
			name:         "foreign dep node missing drops the artifact",
			depPlatforms: nil,
			wantKept:     false,
		},
		{
			// The node exists but records nothing for the platform whose
			// artifact depends on it, so planning that platform fails
			// with ErrMissingArtifact. Node presence is not enough.
			name:         "foreign dep lacking that platform drops the artifact",
			depPlatforms: []string{"darwin/arm64"},
			wantKept:     false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			storeRoot := t.TempDir()
			_, jq := installChain(t, storeRoot)

			pkgs := map[string]lockfile.Package{
				"jq@1.8.1-2": {Artifacts: map[string]lockfile.Artifact{
					// Byte-identical to what the store now says, so the
					// node counts as unchanged and foreign artifacts are
					// eligible for retention.
					platform: {
						SHA256:         shaJQ,
						ManifestDigest: digestM,
						Method:         lockgraph.MethodBinary,
						RuntimeDeps:    []string{"oniguruma@6.9.10-1"},
						GraphDigest:    jq.GraphDigest,
					},
					// The linux build depends on a node no target and no
					// carried root reaches.
					"linux/amd64": artifactWith(
						shaOnig, []string{"pcre2@10.44-1"},
					),
				}},
				"oniguruma@6.9.10-1": {Artifacts: map[string]lockfile.Artifact{
					platform: {
						SHA256:      shaOnig,
						Method:      lockgraph.MethodBinary,
						GraphDigest: onigDigest(t, storeRoot),
					},
				}},
			}
			if tc.depPlatforms != nil {
				arts := make(map[string]lockfile.Artifact, len(tc.depPlatforms))
				for _, p := range tc.depPlatforms {
					arts[p] = artifactWith(shaJQ, nil)
				}
				pkgs["pcre2@10.44-1"] = lockfile.Package{Artifacts: arts}
			}

			res, err := Build(Request{
				StoreRoot: storeRoot,
				Platform:  platform,
				Roots:     map[string]string{"jq": "1.8.1-2"},
				Declared:  map[string]string{"jq": "1.8.1"},
				Existing: &lockfile.V1{
					Version: lockfile.SchemaVersion,
					Targets: lockfile.Targets{
						Default: &lockfile.Target{Roots: []string{"jq@1.8.1-2"}},
					},
					Packages: pkgs,
				},
			})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			_, kept := res.Doc.Packages["jq@1.8.1-2"].Artifacts["linux/amd64"]
			if kept != tc.wantKept {
				t.Errorf("foreign artifact kept = %v, want %v", kept, tc.wantKept)
			}
			if _, hasDep := res.Doc.Packages["pcre2@10.44-1"]; hasDep != tc.wantKept {
				t.Errorf("foreign dep node present = %v, want %v", hasDep, tc.wantKept)
			}
		})
	}
}

// TestBuildRejectsTwoVersionsOfOnePackage: the store holds several
// versions of a package happily, so a verified closure can contain two.
// A generation links exactly one, and lockplan.traverse rejects such a
// graph outright, so emitting it would produce a document this project's
// own reader refuses. The writer must fail instead of writing it.
func TestBuildRejectsTwoVersionsOfOnePackage(t *testing.T) {
	storeRoot := t.TempDir()
	dep := func(version, sha string) lockgraph.Node {
		return lockgraph.Node{
			Name: "libb", Version: version,
			GOOS: "darwin", GOARCH: "arm64",
			Method: lockgraph.MethodBinary, SHA256: sha,
		}
	}
	parent := func(name, depVersion string) lockgraph.Node {
		return lockgraph.Node{
			Name: name, Version: "1.0.0-1",
			GOOS: "darwin", GOARCH: "arm64",
			Method: lockgraph.MethodBinary, SHA256: shaJQ,
			Edges: []lockgraph.Edge{{
				Kind: lockgraph.KindRuntime, Name: "libb", Version: depVersion,
			}},
		}
	}
	install(t, storeRoot, dep("1.0.0-1", shaOnig))
	install(t, storeRoot, dep("2.0.0-1", shaJQ))
	install(t, storeRoot, parent("alpha", "1.0.0-1"))
	install(t, storeRoot, parent("beta", "2.0.0-1"))

	_, err := Build(Request{
		StoreRoot: storeRoot,
		Platform:  platform,
		Roots:     map[string]string{"alpha": "1.0.0-1", "beta": "1.0.0-1"},
		Declared:  map[string]string{"alpha": "1.0.0", "beta": "1.0.0"},
	})
	if !errors.Is(err, lockfile.ErrVersionConflict) {
		t.Fatalf("err = %v, want lockfile.ErrVersionConflict", err)
	}
}

// TestBuildCarriesRootWithUnserializedBinaryBuildEdge: a binary lock
// artifact may legitimately record build dependencies —
// lockplan.validateEdges permits it and validates them against the
// recipe — but neither lockgraph nor lockplan.traverse follows them,
// because a prebuilt artifact was not produced from them here.
//
// So a build edge on a binary artifact must not be walked. Walking it
// would drop a carried root whose build dependency was collected long
// ago, and would report version conflicts through edges the reader
// never traverses.
func TestBuildCarriesRootWithUnserializedBinaryBuildEdge(t *testing.T) {
	storeRoot := t.TempDir()
	installChain(t, storeRoot)

	existing := &lockfile.V1{
		Version: lockfile.SchemaVersion,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{
				Roots: []string{"jq@1.8.1-2", "ripgrep@14.1.0-1"},
			},
		},
		Packages: map[string]lockfile.Package{
			"ripgrep@14.1.0-1": {Artifacts: map[string]lockfile.Artifact{
				platform: {
					SHA256:      shaOnig,
					Method:      lockgraph.MethodBinary,
					BuildDeps:   []string{"ghost@1.0.0-1"},
					GraphDigest: digestM,
				},
			}},
		},
	}

	res, err := Build(Request{
		StoreRoot: storeRoot,
		Platform:  platform,
		Roots:     map[string]string{"jq": "1.8.1-2"},
		Declared:  map[string]string{"jq": "1.8.1", "ripgrep": "14.1.0"},
		Existing:  existing,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	wantRoots := []string{"jq@1.8.1-2", "ripgrep@14.1.0-1"}
	if !slices.Equal(res.Doc.Targets.Default.Roots, wantRoots) {
		t.Errorf("roots = %v, want %v", res.Doc.Targets.Default.Roots, wantRoots)
	}
	if len(res.Unlocked) != 0 {
		t.Errorf("Unlocked = %v, want none", res.Unlocked)
	}
}

// TestBuildRejectsConflictOnAForeignPlatform: retaining a foreign
// artifact asserts it, so a conflict that only exists on that platform is
// this writer's to refuse. Checking only the platform being written would
// emit a lock that works on darwin and is unusable on linux.
func TestBuildRejectsConflictOnAForeignPlatform(t *testing.T) {
	const linux = "linux/amd64"
	storeRoot := t.TempDir()
	_, jq := installChain(t, storeRoot)

	existing := &lockfile.V1{
		Version: lockfile.SchemaVersion,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{
				Roots: []string{"jq@1.8.1-2", "ripgrep@14.1.0-1"},
			},
		},
		Packages: map[string]lockfile.Package{
			"jq@1.8.1-2": {Artifacts: map[string]lockfile.Artifact{
				// Identical to the store, so the linux artifact is
				// retained rather than dropped.
				platform: {
					SHA256:         shaJQ,
					ManifestDigest: digestM,
					Method:         lockgraph.MethodBinary,
					RuntimeDeps:    []string{"oniguruma@6.9.10-1"},
					GraphDigest:    jq.GraphDigest,
				},
				linux: artifactWith(shaJQ, []string{"libz@1.0.0-1"}),
			}},
			"oniguruma@6.9.10-1": {Artifacts: map[string]lockfile.Artifact{
				platform: {
					SHA256:      shaOnig,
					Method:      lockgraph.MethodBinary,
					GraphDigest: onigDigest(t, storeRoot),
				},
			}},
			// Carried root: compatible with jq on darwin, incompatible
			// on linux.
			"ripgrep@14.1.0-1": {Artifacts: map[string]lockfile.Artifact{
				platform: artifactWith(shaOnig, nil),
				linux:    artifactWith(shaOnig, []string{"libz@2.0.0-1"}),
			}},
			"libz@1.0.0-1": {Artifacts: map[string]lockfile.Artifact{
				linux: artifactWith(shaOnig, nil),
			}},
			"libz@2.0.0-1": {Artifacts: map[string]lockfile.Artifact{
				linux: artifactWith(shaJQ, nil),
			}},
		},
	}

	_, err := Build(Request{
		StoreRoot: storeRoot,
		Platform:  platform,
		Roots:     map[string]string{"jq": "1.8.1-2"},
		Declared:  map[string]string{"jq": "1.8.1", "ripgrep": "14.1.0"},
		Existing:  existing,
	})
	if !errors.Is(err, lockfile.ErrVersionConflict) {
		t.Fatalf("err = %v, want lockfile.ErrVersionConflict", err)
	}
}

// TestBuildRefusesUnmodelableCarriedRoots: a carried root must be
// assertable, and two prior-document shapes are not.
//
// A node with no artifacts describes nothing on any platform, so a reader
// planning any platform reports ErrMissingArtifact. A serialized cycle
// has no commit order, so lockgraph rejects it outright. Both are
// vacuously "complete" under a walk that only asks whether keys resolve,
// which is why each needs its own case.
func TestBuildRefusesUnmodelableCarriedRoots(t *testing.T) {
	tests := []struct {
		name  string
		nodes map[string]lockfile.Package
	}{
		{
			name: "carried root with no artifacts",
			nodes: map[string]lockfile.Package{
				"ripgrep@14.1.0-1": {Artifacts: map[string]lockfile.Artifact{}},
			},
		},
		{
			name: "carried root in a dependency cycle",
			nodes: map[string]lockfile.Package{
				"ripgrep@14.1.0-1": {Artifacts: map[string]lockfile.Artifact{
					platform: artifactWith(shaOnig, []string{"cyc@1.0.0-1"}),
				}},
				"cyc@1.0.0-1": {Artifacts: map[string]lockfile.Artifact{
					platform: artifactWith(shaJQ, []string{"ripgrep@14.1.0-1"}),
				}},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			storeRoot := t.TempDir()
			installChain(t, storeRoot)

			res, err := Build(Request{
				StoreRoot: storeRoot,
				Platform:  platform,
				Roots:     map[string]string{"jq": "1.8.1-2"},
				Declared:  map[string]string{"jq": "1.8.1", "ripgrep": "14.1.0"},
				Existing: &lockfile.V1{
					Version: lockfile.SchemaVersion,
					Targets: lockfile.Targets{
						Default: &lockfile.Target{
							Roots: []string{"jq@1.8.1-2", "ripgrep@14.1.0-1"},
						},
					},
					Packages: tc.nodes,
				},
			})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if !slices.Equal(res.Doc.Targets.Default.Roots, []string{"jq@1.8.1-2"}) {
				t.Errorf("roots = %v, want only jq", res.Doc.Targets.Default.Roots)
			}
			if !slices.Equal(res.Unlocked, []string{"ripgrep"}) {
				t.Errorf("Unlocked = %v, want [ripgrep]", res.Unlocked)
			}
		})
	}
}

// TestBuildFallbackDoesNotCollapseRootsByName: the undecidable-selector
// fallback must over-approximate, and collapsing roots by name does the
// opposite.
//
// Default roots beta, which needs libb@2. `host?` roots alpha@1, which
// needs libb@1. Exact `hostx` roots alpha@2, which needs no libb. The
// machine `hosty` sees the default and `host?` only, so it genuinely
// conflicts. A fallback that indexed roots by name would let alpha@2
// replace alpha@1, drop libb@1, and pass.
func TestBuildFallbackDoesNotCollapseRootsByName(t *testing.T) {
	storeRoot := t.TempDir()
	install(t, storeRoot, lockgraph.Node{
		Name: "libb", Version: "2.0.0-1",
		GOOS: "darwin", GOARCH: "arm64",
		Method: lockgraph.MethodBinary, SHA256: shaOnig,
	})
	install(t, storeRoot, lockgraph.Node{
		Name: "beta", Version: "1.0.0-1",
		GOOS: "darwin", GOARCH: "arm64",
		Method: lockgraph.MethodBinary, SHA256: shaJQ,
		Edges: []lockgraph.Edge{{
			Kind: lockgraph.KindRuntime, Name: "libb", Version: "2.0.0-1",
		}},
	})

	_, err := Build(Request{
		StoreRoot: storeRoot,
		Platform:  platform,
		Roots:     map[string]string{"beta": "1.0.0-1"},
		Declared:  map[string]string{"beta": "1.0.0"},
		Existing: &lockfile.V1{
			Version: lockfile.SchemaVersion,
			Targets: lockfile.Targets{
				Host: map[string]lockfile.Target{
					// Undecidable, so enumeration is impossible.
					"host?": {Roots: []string{"alpha@1.0.0-1"}},
					"hostx": {Roots: []string{"alpha@2.0.0-1"}},
				},
			},
			Packages: map[string]lockfile.Package{
				"alpha@1.0.0-1": {Artifacts: map[string]lockfile.Artifact{
					platform: artifactWith(shaJQ, []string{"libb@1.0.0-1"}),
				}},
				"alpha@2.0.0-1": {Artifacts: map[string]lockfile.Artifact{
					platform: artifactWith(shaJQ, nil),
				}},
				"libb@1.0.0-1": {Artifacts: map[string]lockfile.Artifact{
					platform: artifactWith(shaOnig, nil),
				}},
			},
		},
	})
	if !errors.Is(err, lockfile.ErrVersionConflict) {
		t.Fatalf("err = %v, want lockfile.ErrVersionConflict", err)
	}
}

// installAlphaChain installs alpha@1.0.0-1 depending on libb@1.0.0-1,
// the fresh side of every cross-target conflict case.
func installAlphaChain(t *testing.T, storeRoot string) {
	t.Helper()
	install(t, storeRoot, lockgraph.Node{
		Name: "libb", Version: "1.0.0-1",
		GOOS: "darwin", GOARCH: "arm64",
		Method: lockgraph.MethodBinary, SHA256: shaOnig,
	})
	install(t, storeRoot, lockgraph.Node{
		Name: "alpha", Version: "1.0.0-1",
		GOOS: "darwin", GOARCH: "arm64",
		Method: lockgraph.MethodBinary, SHA256: shaJQ,
		Edges: []lockgraph.Edge{{
			Kind: lockgraph.KindRuntime, Name: "libb", Version: "1.0.0-1",
		}},
	})
}

// betaNeedingLibb2 is the conflicting side: a root in another target
// whose closure requires a different version of libb.
func betaNeedingLibb2() map[string]lockfile.Package {
	return map[string]lockfile.Package{
		"beta@1.0.0-1": {Artifacts: map[string]lockfile.Artifact{
			platform: artifactWith(shaJQ, []string{"libb@2.0.0-1"}),
		}},
		"libb@2.0.0-1": {Artifacts: map[string]lockfile.Artifact{
			platform: artifactWith(shaOnig, nil),
		}},
	}
}

// TestBuildRejectsCrossTargetConflicts: the reader plans EffectiveRoots,
// merging the default target with every matching host overlay, so a
// document is only sound if every graph a machine can plan from it is.
// Each case is a different way two targets can end up applying together.
func TestBuildRejectsCrossTargetConflicts(t *testing.T) {
	alpha2 := lockfile.Package{Artifacts: map[string]lockfile.Artifact{
		platform: artifactWith(shaJQ, nil),
	}}
	tests := []struct {
		name   string
		target string
		hosts  map[string]lockfile.Target
		extra  map[string]lockfile.Package
	}{
		{
			// An exact overlay and the default target both apply on that
			// host, so their transitive versions must agree.
			name:   "exact overlay against the default target",
			target: "",
			hosts: map[string]lockfile.Target{
				"myhost": {Roots: []string{"beta@1.0.0-1"}},
			},
		},
		{
			// Neither selector is a prefix or suffix of the other, yet
			// work-mbp applies both.
			name:   "overlapping wildcards",
			target: "work-*",
			hosts: map[string]lockfile.Target{
				"*-mbp": {Roots: []string{"beta@1.0.0-1"}},
			},
		},
		{
			// work-mbp matches all three selectors, and the exact one
			// replaces alpha with a version needing no libb, hiding the
			// conflict. work-x-mbp matches only the globs and keeps it, so
			// one witness per selector pair is not enough.
			name:   "conflict masked by a third selector",
			target: "work-*",
			hosts: map[string]lockfile.Target{
				"*-mbp":    {Roots: []string{"beta@1.0.0-1"}},
				"work-mbp": {Roots: []string{"alpha@2.0.0-1"}},
			},
			extra: map[string]lockfile.Package{"alpha@2.0.0-1": alpha2},
		},
		{
			// `?` is outside the documented grammar, so the effective sets
			// cannot be enumerated and coverage falls back to checking
			// every target together rather than skipping the check.
			name:   "undecidable selector falls back to checking everything",
			target: "work-*",
			hosts: map[string]lockfile.Target{
				"host?": {Roots: []string{"beta@1.0.0-1"}},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			storeRoot := t.TempDir()
			installAlphaChain(t, storeRoot)
			pkgs := betaNeedingLibb2()
			maps.Copy(pkgs, tc.extra)

			_, err := Build(Request{
				StoreRoot: storeRoot,
				Platform:  platform,
				Target:    tc.target,
				Roots:     map[string]string{"alpha": "1.0.0-1"},
				Declared:  map[string]string{"alpha": "1.0.0"},
				Existing: &lockfile.V1{
					Version:  lockfile.SchemaVersion,
					Targets:  lockfile.Targets{Host: tc.hosts},
					Packages: pkgs,
				},
			})
			if !errors.Is(err, lockfile.ErrVersionConflict) {
				t.Fatalf("err = %v, want lockfile.ErrVersionConflict", err)
			}
		})
	}
}
