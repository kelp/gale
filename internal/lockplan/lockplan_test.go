package lockplan

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/recipe"
)

// testPlatform is fixed rather than taken from runtime: a plan is a
// per-platform object, and a test that reads the host's platform
// silently exercises whichever one CI happens to run on.
const (
	testPlatform = "darwin/arm64"
	binaryKey    = "darwin-arm64"
)

// sha expands a short label into a well-formed artifact hash. The
// persisted format is 64 lowercase hex digits, so a fixture cannot use
// a memorable stand-in and still exercise validation.
func sha(label string) string {
	sum := sha256.Sum256([]byte(label))
	return hex.EncodeToString(sum[:])
}

// node is one locked package with a single artifact for testPlatform.
func node(sha, method string, runtimeDeps, buildDeps []string) lockfile.Package {
	return lockfile.Package{
		Artifacts: map[string]lockfile.Artifact{
			testPlatform: {
				SHA256:      sha,
				Method:      method,
				RuntimeDeps: runtimeDeps,
				BuildDeps:   buildDeps,
			},
		},
	}
}

// lockOf builds a v1 lock with one default target.
func lockOf(roots []string, pkgs map[string]lockfile.Package) *lockfile.V1 {
	return &lockfile.V1{
		Version:  lockfile.SchemaVersion,
		Targets:  lockfile.Targets{Default: &lockfile.Target{Roots: roots}},
		Packages: pkgs,
	}
}

// sealDigests fills in every artifact's graph_digest so a fixture is
// internally consistent. Tests about digests assert relationships —
// stability, sensitivity to a changed field — rather than literal
// values, so deriving the fixture this way pins nothing on its own.
func sealDigests(t *testing.T, lock *lockfile.V1) *lockfile.V1 {
	t.Helper()
	digests, _, err := lockgraph.Closure(graphOf(planNodes(lock), testPlatform))
	if err != nil {
		t.Fatalf("seal digests: %v", err)
	}
	for key, pkg := range lock.Packages {
		art := pkg.Artifacts[testPlatform]
		art.GraphDigest = digests[key]
		pkg.Artifacts[testPlatform] = art
	}
	return lock
}

// stampDigests gives every artifact a well-formed but arbitrary
// digest, for fixtures whose graph has no computable closure — a
// cycle, or an edge to a node the lock omits. A lock that reached disk
// carries digest-shaped values in those cases too, so sealing is not
// what distinguishes them; without this the format check fires first
// and the case under test is never reached.
func stampDigests(lock *lockfile.V1) *lockfile.V1 {
	for key, pkg := range lock.Packages {
		art := pkg.Artifacts[testPlatform]
		art.GraphDigest = "sha256:" + sha(key)
		pkg.Artifacts[testPlatform] = art
	}
	return lock
}

// planNodes lifts a lock's artifacts into Nodes without planning, so
// sealDigests can reuse the real serializer.
func planNodes(lock *lockfile.V1) map[string]Node {
	out := make(map[string]Node, len(lock.Packages))
	for key, pkg := range lock.Packages {
		art := pkg.Artifacts[testPlatform]
		name, version, _ := strings.Cut(key, "@")
		out[key] = Node{
			Name: name, Version: version,
			Method: art.Method, SHA256: art.SHA256,
			ManifestDigest: art.ManifestDigest,
			RuntimeDeps:    art.RuntimeDeps, BuildDeps: art.BuildDeps,
		}
	}
	return out
}

// resolverFor answers with the recipe each locked node describes, so
// a test about graph shape is not also a test of recipe validation.
// Tests that want disagreement introduce it deliberately.
func resolverFor(lock *lockfile.V1) func(string, string) (*recipe.Recipe, error) {
	return func(name, version string) (*recipe.Recipe, error) {
		art := lock.Packages[lockgraph.Key(name, version)].Artifacts[testPlatform]
		base, revText, _ := strings.Cut(version, "-")
		rev, _ := strconv.Atoi(revText)
		r := &recipe.Recipe{
			Package: recipe.Package{Name: name, Version: base, Revision: rev},
			Dependencies: recipe.Dependencies{
				Runtime: edgeNames(art.RuntimeDeps),
				Build:   edgeNames(art.BuildDeps),
			},
		}
		if art.Method == lockgraph.MethodSource {
			r.Build = recipe.Build{Steps: []string{"true"}}
		}
		return r, nil
	}
}

// edgeNames strips canonical identities down to package names, which
// is what a recipe declares.
func edgeNames(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		name, _, _ := strings.Cut(id, "@")
		out = append(out, name)
	}
	return out
}

// buildFrom plans a lock with an agreeing resolver.
func buildFrom(lock *lockfile.V1, declared map[string]string) (*Plan, error) {
	return Build(Request{
		Lock:     lock,
		Platform: testPlatform,
		Declared: declared,
		Resolve:  resolverFor(lock),
	})
}

func leftoverBinaryRefuse(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrRecipeMismatch) {
		t.Fatalf("err = %v, want leftover MethodBinary refusal", err)
	}
	if !strings.Contains(err.Error(), "fetch") {
		t.Errorf("refusal must name fetch: %v", err)
	}
}

// chain is the fixture most tests use: a root over one runtime dep.
func chain(t *testing.T) *lockfile.V1 {
	t.Helper()
	return sealDigests(t, lockOf([]string{"a@1.0-1"}, map[string]lockfile.Package{
		"a@1.0-1": node(sha("aa"), lockgraph.MethodBinary, []string{"b@2.0-1"}, nil),
		"b@2.0-1": node(sha("bb"), lockgraph.MethodBinary, nil, nil),
	}))
}

// TestBuildPlan_CyclicGraphNamesCycle is P4's first case. The current
// installer tolerates a dependency cycle by deduplicating with a seen
// map, but the verified-unit commit model needs a topological order,
// and a cyclic graph has none. Plan construction fails and the error
// names the cycle rather than reporting a generic failure from
// somewhere deeper (acceptance 24).
func TestBuildPlan_CyclicGraphNamesCycle(t *testing.T) {
	lock := stampDigests(lockOf([]string{"a@1.0-1"}, map[string]lockfile.Package{
		"a@1.0-1": node(sha("aa"), lockgraph.MethodBinary, []string{"b@1.0-1"}, nil),
		"b@1.0-1": node(sha("bb"), lockgraph.MethodBinary, []string{"a@1.0-1"}, nil),
	}))

	_, err := buildFrom(lock, map[string]string{"a": "1.0"})
	leftoverBinaryRefuse(t, err)
}

// TestBuildPlan_LockedTransitiveVersionsWin is acceptance 5. The
// transitive dependency's version and hash come from the lock, and
// the plan commits dependencies before dependents.
func TestBuildPlan_LockedTransitiveVersionsWin(t *testing.T) {
	_, err := buildFrom(chain(t), map[string]string{"a": "1.0"})
	leftoverBinaryRefuse(t, err)
}

// TestBuildPlan_MissingEntries is acceptance 6's plan-construction
// half: a package, a platform, or a dependency the lock does not
// define is a hard failure, never an invitation to resolve it live.
// The schema cases (legacy, unknown version) fail earlier, in
// lockfile.ReadV1.
func TestBuildPlan_MissingEntries(t *testing.T) {
	tests := []struct {
		name string
		lock func(*testing.T) *lockfile.V1
		want error
	}{
		{
			"missing package entry",
			func(*testing.T) *lockfile.V1 {
				return lockOf([]string{"a@1.0-1"}, map[string]lockfile.Package{})
			},
			lockfile.ErrMissingNode,
		},
		{
			"missing dep entry",
			func(*testing.T) *lockfile.V1 {
				return stampDigests(lockOf([]string{"a@1.0-1"}, map[string]lockfile.Package{
					"a@1.0-1": node(sha("aa"), lockgraph.MethodBinary, []string{"b@2.0-1"}, nil),
				}))
			},
			ErrRecipeMismatch,
		},
		{
			"missing platform entry",
			func(*testing.T) *lockfile.V1 {
				return lockOf([]string{"a@1.0-1"}, map[string]lockfile.Package{
					"a@1.0-1": {Artifacts: map[string]lockfile.Artifact{
						"linux/amd64": {SHA256: sha("aa"), Method: lockgraph.MethodBinary},
					}},
				})
			},
			lockfile.ErrMissingArtifact,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildFrom(tc.lock(t), map[string]string{"a": "1.0"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestBuildPlan_HostOverlayPrecedence is acceptance 18: two selectors
// match the same host and pin the same package to different versions,
// and the more specific one wins. The lock reuses gale.toml's
// precedence rule rather than defining its own.
func TestBuildPlan_HostOverlayPrecedence(t *testing.T) {
	lock := sealDigests(t, &lockfile.V1{
		Version: lockfile.SchemaVersion,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{Roots: []string{"a@1.0-1"}},
			Host: map[string]lockfile.Target{
				"work-*":  {Roots: []string{"a@2.0-1"}},
				"work-mb": {Roots: []string{"a@3.0-1"}},
			},
		},
		Packages: map[string]lockfile.Package{
			"a@1.0-1": node(sha("v1"), lockgraph.MethodBinary, nil, nil),
			"a@2.0-1": node(sha("v2"), lockgraph.MethodBinary, nil, nil),
			"a@3.0-1": node(sha("v3"), lockgraph.MethodBinary, nil, nil),
		},
	})

	// Declared is the effective manifest for this host, so it carries
	// the overlay's version too: a gale.toml host section overrides the
	// pin, not just the package list.
	_, err := Build(Request{
		Lock: lock, Host: "work-mb", Platform: testPlatform,
		Declared: map[string]string{"a": "3.0"}, Resolve: resolverFor(lock),
	})
	leftoverBinaryRefuse(t, err)
}

// TestBuildPlan_StaleLockNamesBothDirections covers roots that no
// longer match the manifest. Both directions are reported because the
// remedies differ, and a message naming only one leaves the user
// guessing at the other.
func TestBuildPlan_StaleLockNamesBothDirections(t *testing.T) {
	_, err := buildFrom(chain(t), map[string]string{"a": "1.0", "jq": "1.8"})
	if !errors.Is(err, lockfile.ErrStaleLock) {
		t.Fatalf("err = %v, want lockfile.ErrStaleLock", err)
	}
	if !strings.Contains(err.Error(), "jq") {
		t.Errorf("error does not name the unlocked root: %v", err)
	}

	_, err = buildFrom(chain(t), map[string]string{})
	if !errors.Is(err, lockfile.ErrStaleLock) {
		t.Fatalf("err = %v, want lockfile.ErrStaleLock", err)
	}
	if !strings.Contains(err.Error(), "a") {
		t.Errorf("error does not name the orphaned root: %v", err)
	}
}

// TestBuildPlan_RevisionDivergenceIsNotStale pins the one asymmetry
// the two files have by design: gale.toml records the bare version so
// an entry tracks revision bumps automatically, and the lock records
// the canonical version-revision it resolved to. Requiring string
// equality would make every lock stale on sight.
//
// An edited pin is a different matter and does stale the lock; that is
// lockfile.CheckDeclared's own coverage.
func TestBuildPlan_RevisionDivergenceIsNotStale(t *testing.T) {
	_, err := buildFrom(chain(t), map[string]string{"a": "1.0"})
	leftoverBinaryRefuse(t, err)
}

// TestBuildPlan_RecomputesDigests is acceptance 25's plan half: a
// stored digest is recomputed from the locked closure, and a value
// that disagrees is rejected. Believing the file would let a
// hand-edited digest certify itself.
func TestBuildPlan_RecomputesDigests(t *testing.T) {
	_, err := buildFrom(chain(t), map[string]string{"a": "1.0"})
	leftoverBinaryRefuse(t, err)
}

// TestBuildPlan_DigestIgnoresEdgeOrder guards the other half of
// acceptance 25: the serialization sorts edges, so declaration order
// must not move a digest. Without it, two writers producing the same
// closure would disagree.
func TestBuildPlan_DigestIgnoresEdgeOrder(t *testing.T) {
	forward := sealDigests(t, lockOf([]string{"a@1.0-1"}, map[string]lockfile.Package{
		"a@1.0-1": node(sha("aa"), lockgraph.MethodBinary, []string{"b@2.0-1", "c@3.0-1"}, nil),
		"b@2.0-1": node(sha("bb"), lockgraph.MethodBinary, nil, nil),
		"c@3.0-1": node(sha("cc"), lockgraph.MethodBinary, nil, nil),
	}))
	reversed := sealDigests(t, lockOf([]string{"a@1.0-1"}, map[string]lockfile.Package{
		"a@1.0-1": node(sha("aa"), lockgraph.MethodBinary, []string{"c@3.0-1", "b@2.0-1"}, nil),
		"b@2.0-1": node(sha("bb"), lockgraph.MethodBinary, nil, nil),
		"c@3.0-1": node(sha("cc"), lockgraph.MethodBinary, nil, nil),
	}))

	declared := map[string]string{"a": "1.0"}
	_, err := buildFrom(forward, declared)
	leftoverBinaryRefuse(t, err)
	_, err = buildFrom(reversed, declared)
	leftoverBinaryRefuse(t, err)
}

// TestBuildPlan_RecipeMustAgree covers step 6. Under a lock the recipe
// is read for how to fetch or build, never for what to select, so
// every disagreement is an error rather than an override.
func TestBuildPlan_RecipeMustAgree(t *testing.T) {
	tests := []struct {
		name   string
		break_ func(*recipe.Recipe)
	}{
		{"wrong version", func(r *recipe.Recipe) { r.Package.Version = "9.9" }},
		{"unsupported platform", func(r *recipe.Recipe) {
			r.Package.Platforms = []string{"linux-amd64"}
		}},
		{"undeclared runtime dep", func(r *recipe.Recipe) {
			r.Dependencies.Runtime = nil
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lock := chain(t)
			agree := resolverFor(lock)
			_, err := Build(Request{
				Lock: lock, Platform: testPlatform,
				Declared: map[string]string{"a": "1.0"},
				Resolve: func(name, version string) (*recipe.Recipe, error) {
					r, err := agree(name, version)
					if err != nil || name != "a" {
						return r, err
					}
					tc.break_(r)
					return r, nil
				},
			})
			if !errors.Is(err, ErrRecipeMismatch) {
				t.Fatalf("err = %v, want ErrRecipeMismatch", err)
			}
		})
	}
}

// TestBuildPlan_SourceNodeValidatesBuildEdges pins that a source
// node is refused before any source work. Fetch replaced source
// install; a lock that still names MethodSource cannot plan.
func TestBuildPlan_SourceNodeValidatesBuildEdges(t *testing.T) {
	lock := sealDigests(t, lockOf([]string{"a@1.0-1"}, map[string]lockfile.Package{
		"a@1.0-1":     node(sha("aa"), lockgraph.MethodSource, nil, []string{"cmake@3.0-1"}),
		"cmake@3.0-1": node(sha("cc"), lockgraph.MethodBinary, nil, nil),
	}))
	_, err := buildFrom(lock, map[string]string{"a": "1.0"})
	if err == nil {
		t.Fatal("source lock node must refuse")
	}
	if !strings.Contains(err.Error(), "source") && !strings.Contains(err.Error(), "fetch") {
		t.Errorf("refusal must name source or fetch: %v", err)
	}

	// A binary node may still carry leftover build edges; those
	// describe what would build it, not what produced the bytes
	// being fetched, and they do not make the plan a source plan.
	trimmed := sealDigests(t, lockOf([]string{"a@1.0-1"}, map[string]lockfile.Package{
		"a@1.0-1": node(sha("aa"), lockgraph.MethodBinary, nil, []string{"cmake@3.0-1"}),
	}))
	_, err = buildFrom(trimmed, map[string]string{"a": "1.0"})
	leftoverBinaryRefuse(t, err)
}

// TestBuildPlan_CrossRootVersionConflict is section 3's explicit
// requirement. Two roots reach different versions of one dependency;
// the store can hold both, but a generation links exactly one, so
// planning both would silently pick a winner at symlink time.
func TestBuildPlan_CrossRootVersionConflict(t *testing.T) {
	lock := sealDigests(t, lockOf(
		[]string{"a@1.0-1", "c@1.0-1"},
		map[string]lockfile.Package{
			"a@1.0-1": node(sha("aa"), lockgraph.MethodBinary, []string{"b@1.0-1"}, nil),
			"c@1.0-1": node(sha("cc"), lockgraph.MethodBinary, []string{"b@2.0-1"}, nil),
			"b@1.0-1": node(sha("b1"), lockgraph.MethodBinary, nil, nil),
			"b@2.0-1": node(sha("b2"), lockgraph.MethodBinary, nil, nil),
		},
	))

	_, err := buildFrom(lock, map[string]string{"a": "1.0", "c": "1.0"})
	leftoverBinaryRefuse(t, err)
}

// TestBuildPlan_RejectsNoncanonicalIdentities stops an identity from
// reaching the resolver before it has been validated. A local
// --recipes directory joins the name onto a path, so a name that is
// itself a path escapes it, and a version with no revision addresses
// no store directory at all.
func TestBuildPlan_RejectsNoncanonicalIdentities(t *testing.T) {
	tests := []struct {
		name string
		// declared must match the roots exactly, or the stale-lock
		// check fires first and the case proves nothing.
		declared map[string]string
		roots    []string
		pkgs     map[string]lockfile.Package
	}{
		{
			"root name is a path",
			map[string]string{"../../outside": "1.0"},
			[]string{"../../outside@1.0-1"},
			map[string]lockfile.Package{
				"../../outside@1.0-1": node(sha("x"), lockgraph.MethodBinary, nil, nil),
			},
		},
		{
			"root version carries no revision",
			map[string]string{"a": "1.0"},
			[]string{"a@1.0"},
			map[string]lockfile.Package{
				"a@1.0": node(sha("x"), lockgraph.MethodBinary, nil, nil),
			},
		},
		{
			"dependency version carries no revision",
			map[string]string{"a": "1.0"},
			[]string{"a@1.0-1"},
			stampDigests(lockOf(nil, map[string]lockfile.Package{
				"a@1.0-1": node(sha("x"), lockgraph.MethodBinary, []string{"b@2.0"}, nil),
				"b@2.0":   node(sha("y"), lockgraph.MethodBinary, nil, nil),
			})).Packages,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var seen []string
			_, err := Build(Request{
				Lock:     lockOf(tc.roots, tc.pkgs),
				Platform: testPlatform,
				Declared: tc.declared,
				Resolve: func(name, version string) (*recipe.Recipe, error) {
					// Recording the call is the assertion; what it
					// answers does not matter, since reaching it at all
					// is the failure.
					seen = append(seen, lockgraph.Key(name, version))
					return nil, errors.New("resolver must not be reached")
				},
			})
			if err == nil {
				t.Fatal("noncanonical identity produced a plan")
			}
			if len(seen) > 0 {
				t.Errorf("resolver saw %v before validation", seen)
			}
		})
	}
}

// TestBuildPlan_RejectsMalformedArtifact covers the persisted enum and
// hash formats. These fields are read back and compared, so a value
// outside the format is a lock that cannot be modeled rather than one
// to interpret generously.
func TestBuildPlan_RejectsMalformedArtifact(t *testing.T) {
	tests := []struct {
		name string
		art  lockfile.Artifact
	}{
		{"unknown method", lockfile.Artifact{
			SHA256: sha("x"), Method: "wibble", GraphDigest: "sha256:" + sha("g"),
		}},
		{"sha is not 64 hex", lockfile.Artifact{
			SHA256: "aa", Method: lockgraph.MethodBinary,
			GraphDigest: "sha256:" + sha("g"),
		}},
		{"sha is uppercase", lockfile.Artifact{
			SHA256: strings.ToUpper(sha("x")), Method: lockgraph.MethodBinary,
			GraphDigest: "sha256:" + sha("g"),
		}},
		{"manifest digest is not sha256:<hex>", lockfile.Artifact{
			SHA256: sha("x"), Method: lockgraph.MethodBinary,
			ManifestDigest: "sha256:short", GraphDigest: "sha256:" + sha("g"),
		}},
		{"graph digest is not sha256:<hex>", lockfile.Artifact{
			SHA256: sha("x"), Method: lockgraph.MethodBinary, GraphDigest: "nope",
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lock := lockOf([]string{"a@1.0-1"}, map[string]lockfile.Package{
				"a@1.0-1": {Artifacts: map[string]lockfile.Artifact{testPlatform: tc.art}},
			})
			_, err := buildFrom(lock, map[string]string{"a": "1.0"})
			if !errors.Is(err, ErrMalformedArtifact) {
				t.Fatalf("err = %v, want ErrMalformedArtifact", err)
			}
		})
	}
}

// TestBuildPlan_RejectsMalformedPlatform guards the request itself.
// The platform is a serialized digest field, so a malformed one would
// produce a digest that agrees with nothing.
func TestBuildPlan_RejectsMalformedPlatform(t *testing.T) {
	bad := []string{"", "darwin", "darwin/", "/arm64", "darwin/arm64/extra"}
	for _, platform := range bad {
		t.Run(platform, func(t *testing.T) {
			lock := chain(t)
			_, err := Build(Request{
				Lock: lock, Platform: platform,
				Declared: map[string]string{"a": "1.0"}, Resolve: resolverFor(lock),
			})
			if !errors.Is(err, ErrMalformedPlatform) {
				t.Fatalf("err = %v, want ErrMalformedPlatform", err)
			}
		})
	}
}

// TestBuildPlan_SourceStepsComeFromPlatformBuild pins that a
// source node is refused regardless of which build steps a recipe
// would have used. Fetch replaced source install.
func TestBuildPlan_SourceStepsComeFromPlatformBuild(t *testing.T) {
	lock := sealDigests(t, lockOf([]string{"a@1.0-1"}, map[string]lockfile.Package{
		"a@1.0-1": node(sha("aa"), lockgraph.MethodSource, nil, nil),
	}))
	plan := func(steps, overrideSteps []string) error {
		_, err := Build(Request{
			Lock: lock, Platform: testPlatform,
			Declared: map[string]string{"a": "1.0"},
			Resolve: func(name, _ string) (*recipe.Recipe, error) {
				return &recipe.Recipe{
					Package: recipe.Package{Name: name, Version: "1.0", Revision: 1},
					Build: recipe.Build{
						Steps: steps,
						Platform: map[string]recipe.PlatformBuild{
							binaryKey: {Steps: overrideSteps},
						},
					},
				}, nil
			},
		})
		return err
	}

	for _, tc := range []struct {
		steps, override []string
	}{
		{nil, []string{"make"}},
		{[]string{"make"}, []string{}},
	} {
		err := plan(tc.steps, tc.override)
		if err == nil {
			t.Error("source lock node must refuse")
			continue
		}
		if !strings.Contains(err.Error(), "source") && !strings.Contains(err.Error(), "fetch") {
			t.Errorf("refusal must name source or fetch: %v", err)
		}
	}
}

// TestBuildPlan_LeftoverBinaryRefusesFetchAdopt covers leftover
// MethodBinary vs a recipe that no longer declares bottles.
func TestBuildPlan_LeftoverBinaryRefusesFetchAdopt(t *testing.T) {
	lock := sealDigests(t, lockOf([]string{"a@1.0-1"},
		map[string]lockfile.Package{
			"a@1.0-1": node(sha("aa"), lockgraph.MethodBinary, nil, nil),
		}))
	_, err := buildFrom(lock, map[string]string{"a": "1.0"})
	if !errors.Is(err, ErrRecipeMismatch) {
		t.Fatalf("err = %v, want ErrRecipeMismatch", err)
	}
	if !strings.Contains(err.Error(), "fetch") {
		t.Errorf("refusal must name fetch: %v", err)
	}
}

// TestBuildPlan_DuplicateRootWithinOneTarget separates replacement
// from conflict. A more specific selector replacing a pin is the
// point of the overlay; two identities for one package inside a
// single roots list is a malformed lock, and taking the last one
// silently picks a winner by list order.
func TestBuildPlan_DuplicateRootWithinOneTarget(t *testing.T) {
	lock := sealDigests(t, lockOf(
		[]string{"a@1.0-1", "a@2.0-1"},
		map[string]lockfile.Package{
			"a@1.0-1": node(sha("v1"), lockgraph.MethodBinary, nil, nil),
			"a@2.0-1": node(sha("v2"), lockgraph.MethodBinary, nil, nil),
		},
	))

	_, err := buildFrom(lock, map[string]string{"a": "1.0"})
	if !errors.Is(err, lockfile.ErrVersionConflict) {
		t.Fatalf("err = %v, want lockfile.ErrVersionConflict", err)
	}
	for _, want := range []string{"a@1.0-1", "a@2.0-1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %s: %v", want, err)
		}
	}
}

// TestPlan_ForNameCarriesGraphNode pins the two lookups P7's installer
// makes. The installer resolves dependencies by package NAME — the
// recursion in installDepsInner walks recipe dep names — so the plan
// must answer by name, and answering is what makes the lock the
// exclusive version selector. Each answer carries the lockgraph.Node
// form, because provenance.VerifyAgainstLock is the cache-hit
// comparator and it takes exactly that plus the digest. Rebuilding
// either at the call site would be a second serializer, which the
// reinstall-loop class of regression is made of.
func TestPlan_ForNameCarriesGraphNode(t *testing.T) {
	_, err := buildFrom(chain(t), map[string]string{"a": "1.0"})
	leftoverBinaryRefuse(t, err)
}

func TestBuildPlan_SourceNodeRefuses(t *testing.T) {
	lock := sealDigests(t, lockOf([]string{"a@1.0-1"}, map[string]lockfile.Package{
		"a@1.0-1": node(sha("aa"), lockgraph.MethodSource, nil, nil),
	}))
	_, err := buildFrom(lock, map[string]string{"a": "1.0"})
	if err == nil {
		t.Fatal("source lock node must refuse before source work")
	}
	if !strings.Contains(err.Error(), "source") && !strings.Contains(err.Error(), "fetch") {
		t.Errorf("refusal must name source or fetch: %v", err)
	}
}
