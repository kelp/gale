package main

// Tests for `gale lock`: the writer that regenerates one lock target
// from what gale.toml declares, without touching gale.toml itself.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/recipe"
)

// seedSourceProvenance seeds a store dir and the source-method
// provenance record beside it. Source provenance is reused as-is by
// `gale lock` (design §11), so a fixture using it exercises the
// target-writing rules without also having to satisfy the recipe
// agreement check a binary record carries.
// Runtime dependencies are given as canonical name@version-revision
// identities and must themselves be seeded first, since provenance.New
// reads each dependency's own record to digest the edge.
func seedSourceProvenance(
	t *testing.T, storeRoot, name, version string, deps ...string,
) {
	t.Helper()
	dir := seedStore(t, storeRoot, name, version)
	edges := make([]lockgraph.Edge, 0, len(deps))
	for _, dep := range deps {
		depName, depVersion, _ := strings.Cut(dep, "@")
		edges = append(edges, lockgraph.Edge{
			Kind: lockgraph.KindRuntime, Name: depName, Version: depVersion,
		})
	}
	rec, err := provenance.New(storeRoot, lockgraph.Node{
		Name:    name,
		Version: version,
		GOOS:    runtime.GOOS,
		GOARCH:  runtime.GOARCH,
		Method:  lockgraph.MethodSource,
		SHA256:  testSHA,
		Edges:   edges,
	})
	if err != nil {
		t.Fatalf("provenance.New(%s): %v", name, err)
	}
	if err := provenance.Write(dir, rec); err != nil {
		t.Fatalf("provenance.Write(%s): %v", name, err)
	}
}

// lockCtx builds a cmdContext over a HOME-rooted gale dir whose
// resolver answers every name from versions, so a lock resolves its
// pins without a registry.
func lockCtx(
	t *testing.T, tmp, configBody string, versions map[string]string,
) *cmdContext {
	t.Helper()
	return lockCtxResolver(t, tmp, configBody,
		func(_ context.Context, name string) (*recipe.Recipe, error) {
			v, ok := versions[name]
			if !ok {
				t.Fatalf("resolver asked for an unexpected package %q", name)
			}
			return minimalRecipe(name, v), nil
		})
}

// lockCtxResolver is lockCtx with the recipe resolver supplied, for
// the fixtures that need a recipe a lock can actually install from.
func lockCtxResolver(
	t *testing.T, tmp, configBody string, resolver installer.RecipeResolver,
) *cmdContext {
	t.Helper()
	galeDir := filepath.Join(tmp, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	galePath := filepath.Join(galeDir, "gale.toml")
	if err := os.WriteFile(galePath, []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}
	return buildFakeCtx(t, galePath, galeDir, storeRoot, resolver)
}

// readFileOrFail reads a file the test needs to compare byte for byte.
func readFileOrFail(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// discardOutput is the sink for commands under test: `gale lock`
// prints progress, and none of these tests assert on it.
func discardOutput() *output.Output {
	return output.New(io.Discard, false)
}

// TestLockWritesTheDefaultTargetWithoutTouchingTheManifest is the
// §11 table's `gale lock` row: manifest section written, none; lock
// target regenerated, [targets.default] only.
//
// The manifest comparison is the half that is easy to lose. `lock`
// resolves and installs the same way `install` does, and every other
// writer follows that with a gale.toml write; a lock that repinned
// the manifest would silently convert a read of what is declared
// into an edit of it.
func TestLockWritesTheDefaultTargetWithoutTouchingTheManifest(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("GALE_HOST", "testbox")

	ctx := lockCtx(t, tmp, "[packages]\n  testpkg = \"1.0.0\"\n",
		map[string]string{"testpkg": "1.0.0"})
	seedSourceProvenance(t, ctx.StoreRoot, "testpkg", "1.0.0-1")

	before := readFileOrFail(t, ctx.GalePath)
	if err := runLock(ctx, "", discardOutput()); err != nil {
		t.Fatalf("runLock: %v", err)
	}
	if after := readFileOrFail(t, ctx.GalePath); !bytes.Equal(before, after) {
		t.Errorf("gale.toml was rewritten:\n%s", after)
	}

	lf := readLock(t, ctx)
	if lf.Targets.Default == nil {
		t.Fatalf("no [targets.default]; targets = %+v", lf.Targets)
	}
	if len(lf.Targets.Default.Roots) != 1 ||
		lf.Targets.Default.Roots[0] != "testpkg@1.0.0-1" {
		t.Errorf("default roots = %v, want [testpkg@1.0.0-1]",
			lf.Targets.Default.Roots)
	}
	if len(lf.Targets.Host) != 0 {
		t.Errorf("host targets = %v, want none", lf.Targets.Host)
	}
	if _, ok := lf.Packages["testpkg@1.0.0-1"]; !ok {
		t.Errorf("lock packages = %v, want a testpkg@1.0.0-1 node", lf.Packages)
	}
}

// writeRecipe writes a letter-bucketed recipe under recipesRoot so a
// --recipes run resolves the pin locally instead of over the network.
func writeRecipe(t *testing.T, recipesRoot, name, version string) {
	t.Helper()
	dir := filepath.Join(recipesRoot, "recipes", name[:1])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Join([]string{
		`[package]`,
		`name = "` + name + `"`,
		`version = "` + version + `"`,
		`revision = 1`,
		``,
		`[source]`,
		`url = "https://example.invalid/` + name + `.tar.gz"`,
		`sha256 = "` + strings.Repeat("0", 64) + `"`,
		``,
		`[build]`,
		`steps = ["true"]`,
	}, "\n")
	if err := os.WriteFile(
		filepath.Join(dir, name+".toml"), []byte(body), 0o644,
	); err != nil {
		t.Fatal(err)
	}
}

// runLockCmd runs the cobra command in projDir with the given flags,
// resetting the package-level flag vars afterwards.
func runLockCmd(t *testing.T, projDir, recipesRoot, host string) error {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck // best-effort restore
	lockRecipes = recipesRoot
	lockHost = host
	t.Cleanup(func() { lockRecipes, lockHost = "", "" })
	return lockCmd.RunE(lockCmd, nil)
}

// TestLockHostCurrentWritesOnlyThatHostTarget is the §11 table's
// `gale lock --host K` row, with K arriving as the `current` alias.
//
// Three things have to hold at once. The target key is the concrete
// hostname resolveHostFlag expands to, because a target keyed
// "current" matches no machine and every reader would plan without
// it. The roots come from that host's overlay alone. And the default
// target written by the earlier plain run survives untouched: a
// concrete-host operation that rewrote the shared graph would drop
// every root the shared section declares but this run never looked at.
func TestLockHostCurrentWritesOnlyThatHostTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_HOST", "testbox")
	t.Setenv("GALE_OFFLINE", "1")

	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")
	if err := os.WriteFile(configPath, []byte(
		"[packages]\n  shared = \"1.0.0\"\n\n"+
			"[hosts.testbox.packages]\n  hostpkg = \"2.0.0\"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	recipesRoot := filepath.Join(projDir, "gale-recipes")
	writeRecipe(t, recipesRoot, "shared", "1.0.0")
	writeRecipe(t, recipesRoot, "hostpkg", "2.0.0")
	seedSourceProvenance(t, defaultStoreRoot(), "shared", "1.0.0-1")
	seedSourceProvenance(t, defaultStoreRoot(), "hostpkg", "2.0.0-1")

	if err := runLockCmd(t, projDir, recipesRoot, ""); !errors.Is(err, errSwitchHosts) {
		t.Fatalf("gale lock with host overlays: %v, want errSwitchHosts", err)
	}
}

// TestLockRefusesATargetTheManifestDeclaresNothingFor pins §11's
// last sentence: a project declaring no default packages makes plain
// `gale lock` error and list the declared selectors.
//
// Writing an empty [targets.default] instead would be worse than
// useless. It reads as "the shared section declares nothing to lock",
// which is indistinguishable from the state a `remove` legitimately
// produces, while the packages the user does declare stay unlocked
// and unmentioned. The remedy is a --host selector, so the error has
// to name the ones that exist.
func TestLockRefusesATargetTheManifestDeclaresNothingFor(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("GALE_HOST", "testbox")

	// The resolver fails the test if it is called at all: a target
	// with nothing declared must be refused before any recipe work.
	ctx := lockCtx(t, tmp,
		"[hosts.\"ci-*\".packages]\n  citool = \"3.0.0\"\n\n"+
			"[hosts.testbox.packages]\n  hostpkg = \"2.0.0\"\n", nil)

	err := runLock(ctx, "", discardOutput())
	if !errors.Is(err, errNoDeclarations) {
		t.Fatalf("runLock error = %v, want errNoDeclarations", err)
	}
	for _, want := range []string{"ci-*", "testbox"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name the declared selector %q", err, want)
		}
	}

	lp, pathErr := lockfilePath(ctx.GalePath)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if _, statErr := os.Lstat(lp); !os.IsNotExist(statErr) {
		data, _ := os.ReadFile(lp)
		t.Errorf("a lock was written for a target with no declarations:\n%s", data)
	}
}

// TestLockRefusesAnOccupiedUnprovenancedStoreDir is acceptance test
// 36's negative half: `gale lock` may populate an absent canonical
// directory and may not adopt an occupied one that carries no
// provenance.
//
// Adopting it would assert provenance for bytes nothing verified,
// which is the unverified marker §13 rejected under another name.
// Replacement is possible, but only through the explicit
// `fetch-adopt`/`migrate` route, so the refusal has to say which. The
// directory is left exactly as it was: this command decided, it did
// not mutate.
func TestLockRefusesAnOccupiedUnprovenancedStoreDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	ctx := lockCtx(t, tmp, "[packages]\n  legacypkg = \"1.0.0\"\n",
		map[string]string{"legacypkg": "1.0.0"})
	dir := seedStore(t, ctx.StoreRoot, "legacypkg", "1.0.0-1")

	err := runLock(ctx, "", discardOutput())
	if !errors.Is(err, errUnprovenancedStoreDir) {
		t.Fatalf("runLock error = %v, want errUnprovenancedStoreDir", err)
	}
	if strings.Contains(err.Error(), "--refresh") {
		t.Errorf("error %q names deleted --refresh", err)
	}
	if !strings.Contains(err.Error(), "fetch-adopt") {
		t.Errorf("error %q does not name fetch-adopt", err)
	}

	if _, statErr := os.Lstat(
		filepath.Join(dir, provenance.File),
	); !os.IsNotExist(statErr) {
		t.Errorf("lock stamped provenance onto a directory it never verified: %v",
			statErr)
	}
	if _, statErr := os.Lstat(
		filepath.Join(dir, "bin", "legacypkg"),
	); statErr != nil {
		t.Errorf("the store dir was disturbed: %v", statErr)
	}

	lp, pathErr := lockfilePath(ctx.GalePath)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if _, statErr := os.Lstat(lp); !os.IsNotExist(statErr) {
		data, _ := os.ReadFile(lp)
		t.Errorf("a lock was written over an unprovenanced store dir:\n%s", data)
	}
}

// TestLockChecksInstalledBinaryProvenanceAgainstTheRecipe pins §11's
// reuse rule: installed binary provenance is reused only when it
// matches the recipe's declared SHA256 and manifest digest.
//
// The store dir is occupied and validly provenanced in every case, so
// a disagreement cannot be resolved by refetching here — that is the
// replacement §11 forbids. Reusing the record anyway would lock this
// machine's bytes under an identity the recipe says holds different
// ones, which is the substitution the lock exists to detect.
//
// The manifest digest is compared only where the recipe carries one
// (design §3, step 6): an index published before the field existed
// declares none, and treating that as a disagreement would make every
// such package unlockable.
func TestLockDryRunInstallsNothingAndWritesNoLock(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	ctx := lockCtx(t, tmp, "[packages]\n  drypkg = \"1.0.0\"\n",
		map[string]string{"drypkg": "1.0.0"})

	dryRun = true
	t.Cleanup(func() { dryRun = false })

	if err := runLock(ctx, "", discardOutput()); err != nil {
		t.Fatalf("runLock: %v", err)
	}
	if _, err := os.Lstat(
		filepath.Join(ctx.StoreRoot, "drypkg"),
	); !os.IsNotExist(err) {
		t.Errorf("a dry run installed drypkg: %v", err)
	}
	lp, pathErr := lockfilePath(ctx.GalePath)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if _, err := os.Lstat(lp); !os.IsNotExist(err) {
		data, _ := os.ReadFile(lp)
		t.Errorf("a dry run wrote %s:\n%s", lp, data)
	}
}

// provenanceFailureCase is one store state a provenance read can
// fail in, and how `gale lock` must report it.
type provenanceFailureCase struct {
	name string
	// seed puts the store dir into the state under test. The dir
	// itself always exists, so every case is "occupied".
	seed func(t *testing.T, dir string)
	// wantIs is what the returned error must match.
	wantIs error
	// replaceable says whether this state may be offered the
	// destructive remedies.
	replaceable bool
	// wantExit is the class a pipeline branches on. The two
	// provenance states are integrity conflicts; a failure to read the
	// file is an ordinary failure and must not be dressed up as one.
	wantExit   int
	skipAsRoot bool
}

func provenanceFailureCases() []provenanceFailureCase {
	return []provenanceFailureCase{
		{
			name:        "no provenance file",
			seed:        func(*testing.T, string) {},
			wantIs:      errUnprovenancedStoreDir,
			replaceable: true,
			wantExit:    exitLockIntegrity,
		},
		{
			name: "record does not validate",
			seed: func(t *testing.T, dir string) {
				if err := os.WriteFile(
					filepath.Join(dir, provenance.File),
					[]byte("name = \"legacypkg\"\n"), 0o644,
				); err != nil {
					t.Fatal(err)
				}
			},
			wantIs:   provenance.ErrInvalid,
			wantExit: exitLockIntegrity,
		},
		{
			name: "record cannot be read",
			seed: func(t *testing.T, dir string) {
				path := filepath.Join(dir, provenance.File)
				if err := os.WriteFile(path, []byte("x = 1\n"), 0o000); err != nil {
					t.Fatal(err)
				}
			},
			wantIs:     fs.ErrPermission,
			wantExit:   exitFailure,
			skipAsRoot: true,
		},
	}
}

// checkProvenanceFailure asserts the classification and, for a state
// that may not be replaced, that the message withholds the
// destructive remedies.
func checkProvenanceFailure(t *testing.T, tc provenanceFailureCase, err error) {
	t.Helper()
	if !errors.Is(err, tc.wantIs) {
		t.Fatalf("runLock error = %v, want one matching %v", err, tc.wantIs)
	}
	if got := exitCodeFor(err); got != tc.wantExit {
		t.Errorf("exitCodeFor(%v) = %d, want %d", err, got, tc.wantExit)
	}
	if tc.replaceable {
		return
	}
	if errors.Is(err, errUnprovenancedStoreDir) {
		t.Errorf("error %q reads as an unprovenanced directory", err)
	}
	for _, remedy := range []string{"--refresh", "fetch-adopt", "migrate"} {
		if strings.Contains(err.Error(), remedy) {
			t.Errorf("error %q offers %s for a state it cannot repair",
				err, remedy)
		}
	}
}

// TestLockClassifiesProvenanceReadFailures separates the one store
// state that may be replaced from every other reason a record fails
// to read.
//
// Only an absent file is the occupied-but-unprovenanced case §13 lets
// `--refresh` or `migrate` replace. A record that exists and does not
// validate is an integrity failure, and an I/O or permission failure
// is neither: offering to replace a store directory because a file
// could not be opened recommends a destructive action in response to
// a transient one.
func TestLockClassifiesProvenanceReadFailures(t *testing.T) {
	for _, tc := range provenanceFailureCases() {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skipAsRoot && os.Getuid() == 0 {
				t.Skip("running as root: permission checks do not apply")
			}
			tmp := t.TempDir()
			t.Setenv("HOME", tmp)

			ctx := lockCtx(t, tmp, "[packages]\n  legacypkg = \"1.0.0\"\n",
				map[string]string{"legacypkg": "1.0.0"})
			tc.seed(t, seedStore(t, ctx.StoreRoot, "legacypkg", "1.0.0-1"))

			checkProvenanceFailure(t, tc, runLock(ctx, "", discardOutput()))

			lp, pathErr := lockfilePath(ctx.GalePath)
			if pathErr != nil {
				t.Fatal(pathErr)
			}
			if _, statErr := os.Lstat(lp); !os.IsNotExist(statErr) {
				t.Error("a lock was written despite an unreadable record")
			}
		})
	}
}

// TestLockNamesARunnableRemedyForEachDeclaredSection: the remedy has
// to follow the section it is offered for.
//
// A project whose packages all live in shared [packages] is fixed by
// plain `gale lock`, so telling that user to pass --host repeats the
// failure they just hit. A host overlay needs the selector verbatim
// and quoted, since a wildcard would otherwise be expanded by the
// shell before gale ever sees it.
func TestLockNamesARunnableRemedyForEachDeclaredSection(t *testing.T) {
	cases := []struct {
		name      string
		config    string
		target    string
		wantIn    []string
		wantNotIn []string
	}{
		{
			name:      "only the shared section declares packages",
			config:    "[packages]\n  tool = \"1.0.0\"\n",
			target:    "missing",
			wantIn:    []string{"gale lock"},
			wantNotIn: []string{"--host"},
		},
		{
			name: "only host overlays declare packages",
			config: "[hosts.\"ci-*\".packages]\n  citool = \"3.0.0\"\n\n" +
				"[hosts.testbox.packages]\n  hostpkg = \"2.0.0\"\n",
			target: "",
			wantIn: []string{
				"gale lock --host 'ci-*'", "gale lock --host 'testbox'",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("HOME", tmp)
			ctx := lockCtx(t, tmp, tc.config, nil)

			err := runLock(ctx, tc.target, discardOutput())
			if !errors.Is(err, errNoDeclarations) {
				t.Fatalf("runLock error = %v, want errNoDeclarations", err)
			}
			for _, want := range tc.wantIn {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not offer %q", err, want)
				}
			}
			for _, unwanted := range tc.wantNotIn {
				if strings.Contains(err.Error(), unwanted) {
					t.Errorf("error %q offers %q for a section that has none",
						err, unwanted)
				}
			}
		})
	}
}
