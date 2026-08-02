package main

// Tests for other-platform minting: `gale lock` derives a
// method = "binary" artifact for every platform whose entire
// effective closure the recipes describe (design §11).

import (
	"bytes"
	"runtime"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/recipe"
)

// captureOutput is the sink for the tests that assert on a
// diagnostic, since a skipped platform is reported and not returned.
func captureOutput(buf *bytes.Buffer) *output.Output {
	return output.New(buf, false)
}

// hasArtifact reports whether a node records an artifact for a
// platform. Used where the expected answer is no, so a missing node
// is not a test failure.
func hasArtifact(lf *lockfile.V1, key, platform string) bool {
	_, ok := lf.Packages[key].Artifacts[platform]
	return ok
}

// Hashes the minting fixtures declare. They differ from testSHA so an
// artifact that carried the locally provenanced hash instead of the
// recipe's would be visible rather than accidentally equal.
var (
	mintRootSHA = strings.Repeat("ab", 32)
	mintDepSHA  = strings.Repeat("cd", 32)
	mintDigest  = "sha256:" + strings.Repeat("ef", 32)
)

// foreignPlatform returns a GOOS/GOARCH this machine is not, so a
// minting test exercises the other-platform path wherever it runs.
// Minting excludes the current platform by definition, so a fixture
// hardcoding linux/amd64 would silently test nothing on a linux
// builder.
func foreignPlatform() (goos, goarch string) {
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		return "darwin", "arm64"
	}
	return "linux", "amd64"
}

// mintRecipe builds a recipe carrying one prebuilt binary for the
// given platform key, the shape minting derives an artifact from.
func mintRecipe(
	name, version, key, sha string, deps ...string,
) *recipe.Recipe {
	r := minimalRecipe(name, version)
	r.Binary = map[string]recipe.Binary{key: {
		URL: "https://ghcr.io/v2/kelp/gale-recipes/" + name +
			"/blobs/sha256:" + sha,
		SHA256: sha,
	}}
	if len(deps) > 0 {
		r.Dependencies = recipe.Dependencies{Runtime: deps}
	}
	return r
}

// withManifestDigest records the OCI manifest digest CI publishes
// beside a prebuilt. It is optional in a recipe, so the fixtures
// exercise both spellings.
func withManifestDigest(r *recipe.Recipe, key, digest string) *recipe.Recipe {
	b := r.Binary[key]
	b.ManifestDigest = digest
	r.Binary[key] = b
	return r
}

// mintResolver answers from a fixed recipe set and fails the test on
// any other name, so a fixture cannot pass by resolving something it
// did not mean to declare.
func mintResolver(
	t *testing.T, recipes map[string]*recipe.Recipe,
) installer.RecipeResolver {
	t.Helper()
	return func(name string) (*recipe.Recipe, error) {
		r, ok := recipes[name]
		if !ok {
			t.Fatalf("resolver asked for an unexpected package %q", name)
		}
		return r, nil
	}
}

// artifactFor fetches one node's artifact for a platform, failing the
// test when either the node or the platform entry is absent.
func artifactFor(
	t *testing.T, lf *lockfile.V1, key, platform string,
) lockfile.Artifact {
	t.Helper()
	p, ok := lf.Packages[key]
	if !ok {
		t.Fatalf("lock has no %s node; packages = %v", key, lf.Packages)
	}
	a, ok := p.Artifacts[platform]
	if !ok {
		t.Fatalf("%s records no %s artifact; artifacts = %v",
			key, platform, p.Artifacts)
	}
	return a
}

// TestLockMintsABinaryOnlyPlatform is §11's minting rule: a project
// whose entire closure has prebuilt binaries for another platform
// gets an artifact for every node of it, while running on this one.
//
// The graph digest is the assertion that matters. It is recomputed
// here over the foreign closure, so an implementation that minted the
// hashes but digested them under the local GOOS/GOARCH — or over the
// local closure's edges — fails, and a lock whose digests do not
// reproduce is one plan construction rejects on that platform.
func TestLockMintsABinaryOnlyPlatform(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("GALE_HOST", "testbox")

	goos, goarch := foreignPlatform()
	key := goos + "-" + goarch
	// The root declares an allowlist naming both platforms, so this
	// also pins the spelling the allowlist is asked in: it is keyed
	// <goos>-<goarch> like the binaries table, not <goos>/<goarch>
	// like the lockfile, and asking in the wrong one would report a
	// supported platform as excluded.
	localKey := runtime.GOOS + "-" + runtime.GOARCH
	ctx := lockCtxResolver(t, tmp, "[packages]\n  testpkg = \"1.0.0\"\n",
		mintResolver(t, map[string]*recipe.Recipe{
			"testpkg": onlyOnPlatforms(withManifestDigest(mintRecipe(
				"testpkg", "1.0.0", key, mintRootSHA, "libdep",
			), key, mintDigest), localKey, key),
			"libdep": mintRecipe("libdep", "2.0.0", key, mintDepSHA),
		}))
	seedSourceProvenance(t, ctx.StoreRoot, "libdep", "2.0.0-1")
	seedSourceProvenance(t, ctx.StoreRoot, "testpkg", "1.0.0-1", "libdep@2.0.0-1")

	if err := runLock(ctx, "", discardOutput()); err != nil {
		t.Fatalf("runLock: %v", err)
	}

	lf := readLock(t, ctx)
	platform := goos + "/" + goarch
	depWant, err := lockgraph.Digest(lockgraph.Node{
		Name: "libdep", Version: "2.0.0-1", GOOS: goos, GOARCH: goarch,
		Method: lockgraph.MethodBinary, SHA256: mintDepSHA,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rootWant, err := lockgraph.Digest(lockgraph.Node{
		Name: "testpkg", Version: "1.0.0-1", GOOS: goos, GOARCH: goarch,
		Method: lockgraph.MethodBinary, SHA256: mintRootSHA,
		ManifestDigest: mintDigest,
		Edges: []lockgraph.Edge{{
			Kind: lockgraph.KindRuntime, Name: "libdep", Version: "2.0.0-1",
		}},
	}, map[string]string{"libdep@2.0.0-1": depWant})
	if err != nil {
		t.Fatal(err)
	}

	root := artifactFor(t, lf, "testpkg@1.0.0-1", platform)
	if root.Method != lockgraph.MethodBinary || root.SHA256 != mintRootSHA ||
		root.ManifestDigest != mintDigest {
		t.Errorf("testpkg %s artifact = %+v, want the recipe's binary",
			platform, root)
	}
	if len(root.RuntimeDeps) != 1 || root.RuntimeDeps[0] != "libdep@2.0.0-1" {
		t.Errorf("testpkg %s runtime_deps = %v, want [libdep@2.0.0-1]",
			platform, root.RuntimeDeps)
	}
	if root.GraphDigest != rootWant {
		t.Errorf("testpkg %s graph_digest = %s, want %s",
			platform, root.GraphDigest, rootWant)
	}
	dep := artifactFor(t, lf, "libdep@2.0.0-1", platform)
	if dep.SHA256 != mintDepSHA || dep.GraphDigest != depWant {
		t.Errorf("libdep %s artifact = %+v, want sha %s digest %s",
			platform, dep, mintDepSHA, depWant)
	}
	// The locally verified artifact is what provenance recorded, not
	// what the recipe declares: minting adds platforms, it never
	// restates this one.
	local := artifactFor(t, lf, "testpkg@1.0.0-1", currentPlatform())
	if local.Method != lockgraph.MethodSource || local.SHA256 != testSHA {
		t.Errorf("local artifact = %+v, want the source record", local)
	}
}

// TestLockSkipsAPlatformASourceOnlyNodeBlocks is §11's rule that a
// closure containing a node with no binary artifact gets no entry at
// all for that platform, not a partial one.
//
// Both halves matter. A partial entry would leave a lock that reads
// as supported for that platform and must fail on it, while a
// recursive graph digest cannot be computed over the dangling closure
// anyway. And the skip is a diagnostic, not a failure: the run
// succeeds, so the lockfile still lands for every platform it does
// describe.
func TestLockSkipsAPlatformASourceOnlyNodeBlocks(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("GALE_HOST", "testbox")

	goos, goarch := foreignPlatform()
	key := goos + "-" + goarch
	ctx := lockCtxResolver(t, tmp, "[packages]\n  testpkg = \"1.0.0\"\n",
		mintResolver(t, map[string]*recipe.Recipe{
			"testpkg": mintRecipe("testpkg", "1.0.0", key, mintRootSHA, "srcdep"),
			// Built from source everywhere: no [binary] table at all.
			"srcdep": minimalRecipe("srcdep", "3.0.0"),
		}))
	seedSourceProvenance(t, ctx.StoreRoot, "srcdep", "3.0.0-1")
	seedSourceProvenance(t, ctx.StoreRoot, "testpkg", "1.0.0-1", "srcdep@3.0.0-1")

	var buf bytes.Buffer
	if err := runLock(ctx, "", captureOutput(&buf)); err != nil {
		t.Fatalf("runLock: %v", err)
	}

	lf := readLock(t, ctx)
	platform := goos + "/" + goarch
	// The root is prebuilt for the platform and still gets nothing:
	// its dependency is what makes the closure underivable.
	if hasArtifact(lf, "testpkg@1.0.0-1", platform) {
		t.Errorf("testpkg records a %s artifact; artifacts = %v",
			platform, lf.Packages["testpkg@1.0.0-1"].Artifacts)
	}
	if hasArtifact(lf, "srcdep@3.0.0-1", platform) {
		t.Errorf("srcdep records a %s artifact; artifacts = %v",
			platform, lf.Packages["srcdep@3.0.0-1"].Artifacts)
	}
	if !hasArtifact(lf, "testpkg@1.0.0-1", currentPlatform()) {
		t.Errorf("the verified %s artifact is missing: %v",
			currentPlatform(), lf.Packages)
	}
	// The reason is asserted, not just the name: a node missing from a
	// derived closure also surfaces as a dangling edge, and that error
	// happens to name it too. Only the blocker reason distinguishes the
	// two, and only it survives when the blocked node is a root, which
	// no edge points at.
	got := buf.String()
	if !strings.Contains(got, platform) ||
		!strings.Contains(got, "no prebuilt binary for srcdep@3.0.0-1") {
		t.Errorf("diagnostic = %q, want it to name %s and say srcdep@3.0.0-1 "+
			"has no prebuilt binary", got, platform)
	}
}

// TestLockSucceedsWhenTheDerivedGraphIsCyclic is §11's "opportunistic
// and never fatal" clause at its sharpest: the recipes describe a
// closure that has no commit order, and the lockfile is still written
// for the platform this run verified.
//
// Failing instead would, under the atomic-write rule, cost the user
// the whole lockfile over a platform they may not even use, and the
// broken graph is the registry's to fix, not this run's.
func TestLockSucceedsWhenTheDerivedGraphIsCyclic(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("GALE_HOST", "testbox")

	goos, goarch := foreignPlatform()
	key := goos + "-" + goarch
	ctx := lockCtxResolver(t, tmp, "[packages]\n  pkga = \"1.0.0\"\n",
		mintResolver(t, map[string]*recipe.Recipe{
			"pkga": mintRecipe("pkga", "1.0.0", key, mintRootSHA, "pkgb"),
			"pkgb": mintRecipe("pkgb", "2.0.0", key, mintDepSHA, "pkga"),
		}))
	seedSourceProvenance(t, ctx.StoreRoot, "pkga", "1.0.0-1")

	var buf bytes.Buffer
	if err := runLock(ctx, "", captureOutput(&buf)); err != nil {
		t.Fatalf("runLock: %v", err)
	}

	lf := readLock(t, ctx)
	platform := goos + "/" + goarch
	if !hasArtifact(lf, "pkga@1.0.0-1", currentPlatform()) {
		t.Errorf("the verified %s artifact is missing: %v",
			currentPlatform(), lf.Packages)
	}
	if hasArtifact(lf, "pkga@1.0.0-1", platform) {
		t.Errorf("pkga records a %s artifact derived from a cyclic graph",
			platform)
	}
	if got := buf.String(); !strings.Contains(got, platform) {
		t.Errorf("diagnostic = %q, want it to name %s", got, platform)
	}
}

// TestRelockingKeepsAMintedPlatform: a second `gale lock` over an
// unchanged closure reproduces the document byte for byte and reports
// nothing skipped.
//
// This is where minting and retention have to compose. The second run
// retains the first run's foreign artifacts and offers the same mint
// again, so an equality test that called them different would report
// a conflict on every run, and a retention path that dropped what it
// did not verify would lose the platform.
func TestRelockingKeepsAMintedPlatform(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("GALE_HOST", "testbox")

	goos, goarch := foreignPlatform()
	key := goos + "-" + goarch
	config := "[packages]\n  testpkg = \"1.0.0\"\n"
	resolve := mintResolver(t, map[string]*recipe.Recipe{
		"testpkg": withManifestDigest(mintRecipe(
			"testpkg", "1.0.0", key, mintRootSHA, "libdep",
		), key, mintDigest),
		"libdep": mintRecipe("libdep", "2.0.0", key, mintDepSHA),
	})
	ctx := lockCtxResolver(t, tmp, config, resolve)
	seedSourceProvenance(t, ctx.StoreRoot, "libdep", "2.0.0-1")
	seedSourceProvenance(t, ctx.StoreRoot, "testpkg", "1.0.0-1", "libdep@2.0.0-1")
	if err := runLock(ctx, "", discardOutput()); err != nil {
		t.Fatalf("first runLock: %v", err)
	}
	lp, err := lockfilePath(ctx.GalePath)
	if err != nil {
		t.Fatal(err)
	}
	first := readFileOrFail(t, lp)

	// A fresh context, because the accumulated mints and skips belong
	// to one invocation.
	second := lockCtxResolver(t, tmp, config, resolve)
	var buf bytes.Buffer
	if err := runLock(second, "", captureOutput(&buf)); err != nil {
		t.Fatalf("second runLock: %v", err)
	}
	if again := readFileOrFail(t, lp); !bytes.Equal(first, again) {
		t.Errorf("relocking rewrote gale.lock:\n%s\nwas:\n%s", again, first)
	}
	if strings.Contains(buf.String(), "records no") {
		t.Errorf("relocking reported a skipped platform: %q", buf.String())
	}
}

// TestLockNeverMintsThisMachinesPlatform: the platform in hand is the
// write's own to record, from what it verified, and a recipe never
// gets a say in it.
//
// The recipe here declares a prebuilt for this machine whose hash
// differs from the installed one. Deriving it would restate a
// verified artifact from a declared value, and even refusing to
// restate it would report this machine's own platform as one the lock
// does not cover, which is the opposite of true.
func TestLockNeverMintsThisMachinesPlatform(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("GALE_HOST", "testbox")

	key := runtime.GOOS + "-" + runtime.GOARCH
	ctx := lockCtxResolver(t, tmp, "[packages]\n  testpkg = \"1.0.0\"\n",
		mintResolver(t, map[string]*recipe.Recipe{
			"testpkg": mintRecipe("testpkg", "1.0.0", key, mintRootSHA),
		}))
	seedSourceProvenance(t, ctx.StoreRoot, "testpkg", "1.0.0-1")

	var buf bytes.Buffer
	if err := runLock(ctx, "", captureOutput(&buf)); err != nil {
		t.Fatalf("runLock: %v", err)
	}

	lf := readLock(t, ctx)
	local := artifactFor(t, lf, "testpkg@1.0.0-1", currentPlatform())
	if local.Method != lockgraph.MethodSource || local.SHA256 != testSHA {
		t.Errorf("local artifact = %+v, want the source record", local)
	}
	if strings.Contains(buf.String(), "records no") {
		t.Errorf("this machine's platform was reported as skipped: %q",
			buf.String())
	}
}

// TestMintable is the guard on what a derived entry may be built
// from: everything plan construction will later demand of the
// artifact, asked before the entry exists.
//
// A locked binary never falls back to source, so an entry derived
// from a URL-less, untrusted, or malformed prebuilt would be a
// platform that reads as supported and must fail on use — the state
// §11 forbids minting into being.
func TestMintable(t *testing.T) {
	const ghcr = "https://ghcr.io/v2/kelp/gale-recipes/jq/blobs/sha256:x"
	tests := []struct {
		name string
		bin  *recipe.Binary
		want bool
	}{
		{"no entry at all", nil, false},
		{"prebuilt with a url", &recipe.Binary{
			URL: ghcr, SHA256: mintRootSHA,
		}, true},
		{"prebuilt with a manifest digest", &recipe.Binary{
			URL: ghcr, SHA256: mintRootSHA, ManifestDigest: mintDigest,
		}, true},
		{"no url to fetch it from", &recipe.Binary{
			SHA256: mintRootSHA,
		}, false},
		{"no hash", &recipe.Binary{URL: ghcr}, false},
		{"a hash in the wrong case", &recipe.Binary{
			URL: ghcr, SHA256: strings.ToUpper(mintRootSHA),
		}, false},
		{"a malformed manifest digest", &recipe.Binary{
			URL: ghcr, SHA256: mintRootSHA, ManifestDigest: "sha256:nope",
		}, false},
		{"a non-ghcr host under the default trust", &recipe.Binary{
			URL: "https://example.invalid/jq.tar.gz", SHA256: mintRootSHA,
		}, false},
		{"a non-ghcr host that opted out", &recipe.Binary{
			URL:    "https://example.invalid/jq.tar.gz",
			SHA256: mintRootSHA, Trust: recipe.TrustSHA256Only,
		}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := mintable(tc.bin); got != tc.want {
				t.Errorf("mintable(%+v) = %v, want %v", tc.bin, got, tc.want)
			}
		})
	}
}

// assertPlatformSkipped locks a project rooted at testpkg over the
// given recipes and asserts the other platform got no entry and the
// diagnostic says why.
//
// The root's own artifact is checked too: a skip must cost the
// platform that could not be derived and nothing else.
func assertPlatformSkipped(
	t *testing.T, recipes map[string]*recipe.Recipe, wantIn []string,
) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("GALE_HOST", "testbox")

	goos, goarch := foreignPlatform()
	ctx := lockCtxResolver(t, tmp, "[packages]\n  testpkg = \"1.0.0\"\n",
		mintResolver(t, recipes))
	seedSourceProvenance(t, ctx.StoreRoot, "testpkg", "1.0.0-1")

	var buf bytes.Buffer
	if err := runLock(ctx, "", captureOutput(&buf)); err != nil {
		t.Fatalf("runLock: %v", err)
	}
	lf := readLock(t, ctx)
	platform := goos + "/" + goarch
	if hasArtifact(lf, "testpkg@1.0.0-1", platform) {
		t.Errorf("testpkg records a %s artifact; artifacts = %v",
			platform, lf.Packages["testpkg@1.0.0-1"].Artifacts)
	}
	if !hasArtifact(lf, "testpkg@1.0.0-1", currentPlatform()) {
		t.Errorf("the verified %s artifact is missing: %v",
			currentPlatform(), lf.Packages)
	}
	for _, want := range append([]string{platform}, wantIn...) {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("diagnostic = %q, want it to name %s", buf.String(), want)
		}
	}
}

// withConstraint records a version constraint the recipe declares on
// one of its runtime dependencies.
func withConstraint(r *recipe.Recipe, dep, expr string) *recipe.Recipe {
	if r.Dependencies.Constraints == nil {
		r.Dependencies.Constraints = make(map[string]string, 1)
	}
	r.Dependencies.Constraints[dep] = expr
	return r
}

// withPlatformConstraint records a constraint that applies on one
// platform only, the shape DependenciesForPlatform merges in.
func withPlatformConstraint(
	r *recipe.Recipe, key, dep, expr string,
) *recipe.Recipe {
	r.Dependencies.Platform = map[string]recipe.PlatformDependencies{
		key: {Constraints: map[string]string{dep: expr}},
	}
	return r
}

// TestLockSkipsAPlatformAConstraintForbids: a derived closure is held
// to the version constraints its recipes declare, the same ones an
// install enforces.
//
// Nothing downstream would catch a violation: locked planning
// compares dependency names, not constraints, so an unconstrained
// mint would commit a closure that an install on that platform
// refuses to build. The two cases are the ones a name-only
// traversal drops silently — a constraint that applies on the derived
// platform alone, and one a dependency declares rather than the root.
func TestLockSkipsAPlatformAConstraintForbids(t *testing.T) {
	goos, goarch := foreignPlatform()
	key := goos + "-" + goarch
	tests := []struct {
		name    string
		recipes map[string]*recipe.Recipe
		wantIn  []string
	}{
		{
			name: "a constraint scoped to the derived platform",
			recipes: map[string]*recipe.Recipe{
				"testpkg": withPlatformConstraint(
					mintRecipe("testpkg", "1.0.0", key, mintRootSHA, "libdep"),
					key, "libdep", "<1.5.0",
				),
				"libdep": mintRecipe("libdep", "2.0.0", key, mintDepSHA),
			},
			wantIn: []string{"libdep", "<1.5.0"},
		},
		{
			// Unparsable is skipped, not ignored: a constraint nothing
			// can evaluate is not evidence that the resolved version
			// satisfies it.
			name: "a constraint that cannot be parsed",
			recipes: map[string]*recipe.Recipe{
				"testpkg": withConstraint(
					mintRecipe("testpkg", "1.0.0", key, mintRootSHA, "libdep"),
					"libdep", "not-a-version",
				),
				"libdep": mintRecipe("libdep", "2.0.0", key, mintDepSHA),
			},
			wantIn: []string{"libdep", "not-a-version"},
		},
		{
			name: "a constraint a dependency declares",
			recipes: map[string]*recipe.Recipe{
				"testpkg": mintRecipe(
					"testpkg", "1.0.0", key, mintRootSHA, "mid",
				),
				"mid": withConstraint(
					mintRecipe("mid", "2.0.0", key, mintDepSHA, "leaf"),
					"leaf", ">=3.0.0",
				),
				"leaf": mintRecipe("leaf", "1.0.0", key, mintDepSHA),
			},
			wantIn: []string{"leaf", ">=3.0.0"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertPlatformSkipped(t, tc.recipes, tc.wantIn)
		})
	}
}

// TestLockSkipsAPlatformTheRecipesExclude: a package's declared
// platform allowlist decides what may be locked for a platform, not
// what its binaries table happens to carry.
//
// Index merging can leave an entry for a platform [package].platforms
// excludes, and planning on that machine rejects the node. Minting it
// would commit coverage that must fail on use, which is the state §11
// forbids. Both a root and a transitive dependency can carry the
// allowlist, and only a check on every node catches the second.
func TestLockSkipsAPlatformTheRecipesExclude(t *testing.T) {
	goos, goarch := foreignPlatform()
	key := goos + "-" + goarch
	local := runtime.GOOS + "-" + runtime.GOARCH
	tests := []struct {
		name    string
		recipes map[string]*recipe.Recipe
		wantIn  []string
	}{
		{
			name: "the root excludes it",
			recipes: map[string]*recipe.Recipe{
				"testpkg": onlyOnPlatforms(
					mintRecipe("testpkg", "1.0.0", key, mintRootSHA), local,
				),
			},
			wantIn: []string{"testpkg@1.0.0-1"},
		},
		{
			name: "a dependency excludes it",
			recipes: map[string]*recipe.Recipe{
				"testpkg": mintRecipe(
					"testpkg", "1.0.0", key, mintRootSHA, "libdep",
				),
				"libdep": onlyOnPlatforms(
					mintRecipe("libdep", "2.0.0", key, mintDepSHA), local,
				),
			},
			wantIn: []string{"libdep@2.0.0-1"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertPlatformSkipped(t, tc.recipes, tc.wantIn)
		})
	}
}

// onlyOnPlatforms restricts a recipe to the given platform keys, the
// [package] platforms allowlist.
func onlyOnPlatforms(r *recipe.Recipe, keys ...string) *recipe.Recipe {
	r.Package.Platforms = keys
	return r
}
