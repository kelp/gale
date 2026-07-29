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
			return r, nil
		}
		// A ghcr.io URL, because the default trust policy is sigstore
		// and sigstore requires one. A third-party host here would make
		// every binary fixture unusable.
		r.Binary = map[string]recipe.Binary{
			binaryKey: {URL: "https://ghcr.io/v2/x/blobs/sha256:x", SHA256: art.SHA256},
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
	if err == nil {
		t.Fatal("cyclic lock produced a plan")
	}
	if !errors.Is(err, lockgraph.ErrCycle) {
		t.Fatalf("err = %v, want lockgraph.ErrCycle", err)
	}
	for _, want := range []string{"a@1.0-1", "b@1.0-1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %s: %v", want, err)
		}
	}
}

// TestBuildPlan_LockedTransitiveVersionsWin is acceptance 5. The
// transitive dependency's version and hash come from the lock, and
// the plan commits dependencies before dependents.
func TestBuildPlan_LockedTransitiveVersionsWin(t *testing.T) {
	plan, err := buildFrom(chain(t), map[string]string{"a": "1.0"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	dep, ok := plan.Nodes["b@2.0-1"]
	if !ok {
		t.Fatalf("transitive dep absent from plan: %v", plan.Nodes)
	}
	if dep.Version != "2.0-1" || dep.SHA256 != sha("bb") {
		t.Errorf("dep = %+v, want the locked 2.0-1/bb", dep)
	}
	if got := strings.Join(plan.Order, ","); got != "b@2.0-1,a@1.0-1" {
		t.Errorf("order = %s, want dependencies first", got)
	}
	if len(plan.Roots) != 1 || plan.Roots[0] != "a@1.0-1" {
		t.Errorf("roots = %v, want [a@1.0-1]", plan.Roots)
	}
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
			ErrMissingNode,
		},
		{
			"missing dep entry",
			func(*testing.T) *lockfile.V1 {
				return stampDigests(lockOf([]string{"a@1.0-1"}, map[string]lockfile.Package{
					"a@1.0-1": node(sha("aa"), lockgraph.MethodBinary, []string{"b@2.0-1"}, nil),
				}))
			},
			ErrMissingNode,
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
			ErrMissingArtifact,
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

	plan, err := Build(Request{
		Lock: lock, Host: "work-mb", Platform: testPlatform,
		Declared: map[string]string{"a": "1.0"}, Resolve: resolverFor(lock),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(plan.Roots) != 1 || plan.Roots[0] != "a@3.0-1" {
		t.Fatalf("roots = %v, want the exact-host pin [a@3.0-1]", plan.Roots)
	}
	// The overridden versions are not planned: a selector replaces a
	// pin for the same package rather than adding a second one.
	if len(plan.Nodes) != 1 {
		t.Errorf("planned %d nodes, want only the winning pin", len(plan.Nodes))
	}
}

// TestBuildPlan_StaleLockNamesBothDirections covers roots that no
// longer match the manifest. Both directions are reported because the
// remedies differ, and a message naming only one leaves the user
// guessing at the other.
func TestBuildPlan_StaleLockNamesBothDirections(t *testing.T) {
	_, err := buildFrom(chain(t), map[string]string{"a": "1.0", "jq": "1.8"})
	if !errors.Is(err, ErrStaleLock) {
		t.Fatalf("err = %v, want ErrStaleLock", err)
	}
	if !strings.Contains(err.Error(), "jq") {
		t.Errorf("error does not name the unlocked root: %v", err)
	}

	_, err = buildFrom(chain(t), map[string]string{})
	if !errors.Is(err, ErrStaleLock) {
		t.Fatalf("err = %v, want ErrStaleLock", err)
	}
	if !strings.Contains(err.Error(), "a") {
		t.Errorf("error does not name the orphaned root: %v", err)
	}
}

// TestBuildPlan_VersionDivergenceIsNotStale pins the deliberate
// asymmetry: gale.toml holds a constraint and the lock holds the pin
// it resolved to, so comparing versions would make every lock stale on
// sight. Only the name sets are compared.
func TestBuildPlan_VersionDivergenceIsNotStale(t *testing.T) {
	if _, err := buildFrom(chain(t), map[string]string{"a": "*"}); err != nil {
		t.Fatalf("Build: %v", err)
	}
}

// TestBuildPlan_RecomputesDigests is acceptance 25's plan half: a
// stored digest is recomputed from the locked closure, and a value
// that disagrees is rejected. Believing the file would let a
// hand-edited digest certify itself.
func TestBuildPlan_RecomputesDigests(t *testing.T) {
	lock := chain(t)
	plan, err := buildFrom(lock, map[string]string{"a": "1.0"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for key, n := range plan.Nodes {
		if !strings.HasPrefix(n.GraphDigest, "sha256:") {
			t.Errorf("%s has no digest: %q", key, n.GraphDigest)
		}
	}

	// A dependency's bytes change while its identity does not. The
	// parent's stored digest is now a lie, and only recomputation
	// through the closure catches it.
	art := lock.Packages["b@2.0-1"].Artifacts[testPlatform]
	art.SHA256 = sha("tampered")
	lock.Packages["b@2.0-1"].Artifacts[testPlatform] = art

	_, err = buildFrom(lock, map[string]string{"a": "1.0"})
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("err = %v, want ErrDigestMismatch", err)
	}
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
	first, err := buildFrom(forward, declared)
	if err != nil {
		t.Fatalf("Build forward: %v", err)
	}
	second, err := buildFrom(reversed, declared)
	if err != nil {
		t.Fatalf("Build reversed: %v", err)
	}
	if first.Nodes["a@1.0-1"].GraphDigest != second.Nodes["a@1.0-1"].GraphDigest {
		t.Errorf("edge order moved the digest: %q vs %q",
			first.Nodes["a@1.0-1"].GraphDigest,
			second.Nodes["a@1.0-1"].GraphDigest)
	}
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
		{"wrong sha", func(r *recipe.Recipe) {
			r.Binary[binaryKey] = recipe.Binary{URL: "u", SHA256: "different"}
		}},
		{"no binary for the platform", func(r *recipe.Recipe) { r.Binary = nil }},
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

// TestBuildPlan_SourceNodeValidatesBuildEdges pins the classification
// rule. A source node was produced from its build deps, so those edges
// are validated; the same edges beside a binary node describe what
// would build it, not what produced the bytes being fetched.
func TestBuildPlan_SourceNodeValidatesBuildEdges(t *testing.T) {
	lock := sealDigests(t, lockOf([]string{"a@1.0-1"}, map[string]lockfile.Package{
		"a@1.0-1":     node(sha("aa"), lockgraph.MethodSource, nil, []string{"cmake@3.0-1"}),
		"cmake@3.0-1": node(sha("cc"), lockgraph.MethodBinary, nil, nil),
	}))
	if _, err := buildFrom(lock, map[string]string{"a": "1.0"}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// The build tool is part of a source node's closure, so dropping
	// its entry is a missing node rather than an ignorable edge.
	trimmed := sealDigests(t, lockOf([]string{"a@1.0-1"}, map[string]lockfile.Package{
		"a@1.0-1": node(sha("aa"), lockgraph.MethodBinary, nil, []string{"cmake@3.0-1"}),
	}))
	if _, err := buildFrom(trimmed, map[string]string{"a": "1.0"}); err != nil {
		t.Fatalf("binary node followed a build edge it was not produced from: %v", err)
	}
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
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("err = %v, want ErrVersionConflict", err)
	}
	for _, want := range []string{"b@1.0-1", "b@2.0-1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %s: %v", want, err)
		}
	}
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

// TestBuildPlan_SourceStepsComeFromPlatformBuild pins which build
// configuration decides whether a source node is buildable. Builds run
// BuildForPlatform, so reading the default steps rejects a recipe whose
// steps live only in the target-platform override, and accepts one
// whose override removes them.
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

	if err := plan(nil, []string{"make"}); err != nil {
		t.Errorf("rejected steps that exist only in the platform override: %v", err)
	}
	if err := plan([]string{"make"}, []string{}); !errors.Is(err, ErrRecipeMismatch) {
		t.Errorf("err = %v, want ErrRecipeMismatch for an override with no steps", err)
	}
}

// TestBuildPlan_BinaryMustBeUsable covers availability properly. A
// locked binary cannot fall back to source, so a recipe entry the
// installer will reject later must fail here instead, while the plan
// can still be abandoned cheaply.
func TestBuildPlan_BinaryMustBeUsable(t *testing.T) {
	tests := []struct {
		name string
		bin  recipe.Binary
	}{
		{"no URL", recipe.Binary{Trust: recipe.TrustSHA256Only}},
		{"sigstore trust on a non-GHCR host", recipe.Binary{
			URL: "https://example.test/x.tar.zst",
		}},
		{"unknown trust value", recipe.Binary{
			URL: "https://ghcr.io/x", Trust: "vibes",
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lock := sealDigests(t, lockOf([]string{"a@1.0-1"},
				map[string]lockfile.Package{
					"a@1.0-1": node(sha("aa"), lockgraph.MethodBinary, nil, nil),
				}))
			bin := tc.bin
			bin.SHA256 = sha("aa")
			_, err := Build(Request{
				Lock: lock, Platform: testPlatform,
				Declared: map[string]string{"a": "1.0"},
				Resolve: func(name, _ string) (*recipe.Recipe, error) {
					return &recipe.Recipe{
						Package: recipe.Package{Name: name, Version: "1.0", Revision: 1},
						Binary:  map[string]recipe.Binary{binaryKey: bin},
					}, nil
				},
			})
			if !errors.Is(err, ErrRecipeMismatch) {
				t.Fatalf("err = %v, want ErrRecipeMismatch", err)
			}
		})
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
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("err = %v, want ErrVersionConflict", err)
	}
	for _, want := range []string{"a@1.0-1", "a@2.0-1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %s: %v", want, err)
		}
	}
}
