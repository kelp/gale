package installer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
)

// preInstall creates a package directory in the store with a
// bin/ subdirectory so IsInstalled returns true and the bin
// path is collected.
func preInstall(t *testing.T, s *store.Store, name, version string) { //nolint:unparam
	t.Helper()
	dir, err := s.Create(name, version)
	if err != nil {
		t.Fatalf("pre-install %s@%s: %v", name, version, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}
}

// makeRecipe builds a minimal recipe with the given deps.
func makeRecipe(name, version string, buildDeps, runtimeDeps []string) *recipe.Recipe {
	return &recipe.Recipe{
		Package: recipe.Package{Name: name, Version: version},
		Dependencies: recipe.Dependencies{
			Build:   buildDeps,
			Runtime: runtimeDeps,
		},
	}
}

func TestInstallBuildDepsRuntimeDep(t *testing.T) {
	storeRoot := t.TempDir()
	s := store.NewStore(storeRoot)

	// Pre-install the runtime dep so Install() returns cached.
	preInstall(t, s, "libfoo", "1.0")

	inst := &Installer{
		Store: s,
		Resolver: func(_ context.Context, name string) (*recipe.Recipe, error) {
			if name == "libfoo" {
				return makeRecipe("libfoo", "1.0", nil, nil), nil
			}
			return nil, fmt.Errorf("unknown: %s", name)
		},
	}

	r := makeRecipe("mypkg", "2.0", nil, []string{"libfoo"})
	deps, err := inst.InstallBuildDeps(context.Background(), r)
	if err != nil {
		t.Fatalf("InstallBuildDeps: %v", err)
	}

	wantStore := filepath.Join(storeRoot, "libfoo", "1.0")
	if !contains(deps.StoreDirs, wantStore) {
		t.Errorf("StoreDirs = %v, want %q", deps.StoreDirs, wantStore)
	}
	wantBin := filepath.Join(storeRoot, "libfoo", "1.0", "bin")
	if !contains(deps.BinDirs, wantBin) {
		t.Errorf("BinDirs = %v, want %q", deps.BinDirs, wantBin)
	}
}

func TestInstallBuildDepsTransitive(t *testing.T) {
	storeRoot := t.TempDir()
	s := store.NewStore(storeRoot)

	// A depends on B, B depends on C.
	preInstall(t, s, "b", "1.0")
	preInstall(t, s, "c", "1.0")

	recipes := map[string]*recipe.Recipe{
		"b": makeRecipe("b", "1.0", []string{"c"}, nil),
		"c": makeRecipe("c", "1.0", nil, nil),
	}

	inst := &Installer{
		Store: s,
		Resolver: func(_ context.Context, name string) (*recipe.Recipe, error) {
			if r, ok := recipes[name]; ok {
				return r, nil
			}
			return nil, fmt.Errorf("unknown: %s", name)
		},
	}

	a := makeRecipe("a", "1.0", []string{"b"}, nil)
	deps, err := inst.InstallBuildDeps(context.Background(), a)
	if err != nil {
		t.Fatalf("InstallBuildDeps: %v", err)
	}

	// Both B and C store dirs should be in the result.
	wantB := filepath.Join(storeRoot, "b", "1.0")
	wantC := filepath.Join(storeRoot, "c", "1.0")
	if !contains(deps.StoreDirs, wantB) {
		t.Errorf("StoreDirs missing B: %v", deps.StoreDirs)
	}
	if !contains(deps.StoreDirs, wantC) {
		t.Errorf("StoreDirs missing C: %v", deps.StoreDirs)
	}
}

func TestInstallBuildDepsCycleDetection(t *testing.T) {
	storeRoot := t.TempDir()
	s := store.NewStore(storeRoot)

	preInstall(t, s, "a", "1.0")
	preInstall(t, s, "b", "1.0")

	// A depends on B, B depends on A — cycle.
	recipes := map[string]*recipe.Recipe{
		"a": makeRecipe("a", "1.0", []string{"b"}, nil),
		"b": makeRecipe("b", "1.0", []string{"a"}, nil),
	}

	inst := &Installer{
		Store: s,
		Resolver: func(_ context.Context, name string) (*recipe.Recipe, error) {
			if r, ok := recipes[name]; ok {
				return r, nil
			}
			return nil, fmt.Errorf("unknown: %s", name)
		},
	}

	// Should not infinite loop.
	r := makeRecipe("root", "1.0", []string{"a"}, nil)
	deps, err := inst.InstallBuildDeps(context.Background(), r)
	if err != nil {
		t.Fatalf("InstallBuildDeps: %v", err)
	}

	wantA := filepath.Join(storeRoot, "a", "1.0")
	wantB := filepath.Join(storeRoot, "b", "1.0")
	if !contains(deps.StoreDirs, wantA) {
		t.Errorf("StoreDirs missing A: %v", deps.StoreDirs)
	}
	if !contains(deps.StoreDirs, wantB) {
		t.Errorf("StoreDirs missing B: %v", deps.StoreDirs)
	}
}

func TestInstallBuildDepsDiamond(t *testing.T) {
	storeRoot := t.TempDir()
	s := store.NewStore(storeRoot)

	// A depends on B and C, both B and C depend on D.
	preInstall(t, s, "b", "1.0")
	preInstall(t, s, "c", "1.0")
	preInstall(t, s, "d", "1.0")

	recipes := map[string]*recipe.Recipe{
		"b": makeRecipe("b", "1.0", []string{"d"}, nil),
		"c": makeRecipe("c", "1.0", []string{"d"}, nil),
		"d": makeRecipe("d", "1.0", nil, nil),
	}

	inst := &Installer{
		Store: s,
		Resolver: func(_ context.Context, name string) (*recipe.Recipe, error) {
			if r, ok := recipes[name]; ok {
				return r, nil
			}
			return nil, fmt.Errorf("unknown: %s", name)
		},
	}

	a := makeRecipe("a", "1.0", []string{"b", "c"}, nil)
	deps, err := inst.InstallBuildDeps(context.Background(), a)
	if err != nil {
		t.Fatalf("InstallBuildDeps: %v", err)
	}

	// D should appear exactly once.
	wantD := filepath.Join(storeRoot, "d", "1.0")
	count := 0
	for _, d := range deps.StoreDirs {
		if d == wantD {
			count++
		}
	}
	if count != 1 {
		t.Errorf("D appears %d times in StoreDirs, want 1: %v",
			count, deps.StoreDirs)
	}

	// All three deps should be present.
	if len(deps.StoreDirs) != 3 {
		t.Errorf("StoreDirs has %d entries, want 3: %v",
			len(deps.StoreDirs), deps.StoreDirs)
	}
}

func TestInstallBuildDepsEmpty(t *testing.T) {
	storeRoot := t.TempDir()
	s := store.NewStore(storeRoot)

	inst := &Installer{
		Store: s,
		Resolver: func(_ context.Context, name string) (*recipe.Recipe, error) {
			return nil, fmt.Errorf("should not be called")
		},
	}

	r := makeRecipe("mypkg", "1.0", nil, nil)
	deps, err := inst.InstallBuildDeps(context.Background(), r)
	if err != nil {
		t.Fatalf("InstallBuildDeps: %v", err)
	}

	if len(deps.StoreDirs) != 0 {
		t.Errorf("StoreDirs = %v, want empty", deps.StoreDirs)
	}
	if len(deps.BinDirs) != 0 {
		t.Errorf("BinDirs = %v, want empty", deps.BinDirs)
	}
}

// TestInstallBuildDepsClassifiesImplicitSystemDep asserts that a
// recipe pinning an explicit toolchain variant (zig15) while
// system = "zig" pulls in the implicit "zig" toolchain routes the
// two into separate groups: the explicit pin into BinDirs, the
// implicit system dep into SystemBinDirs. buildEnv then joins
// BinDirs ahead of SystemBinDirs so the pin wins the build PATH
// (gale#174).
func TestInstallBuildDepsClassifiesImplicitSystemDep(t *testing.T) {
	storeRoot := t.TempDir()
	s := store.NewStore(storeRoot)

	preInstall(t, s, "zig", "0.16.0")
	preInstall(t, s, "zig15", "0.15.2")

	recipes := map[string]*recipe.Recipe{
		"zig":   makeRecipe("zig", "0.16.0", nil, nil),
		"zig15": makeRecipe("zig15", "0.15.2", nil, nil),
	}
	inst := &Installer{
		Store: s,
		Resolver: func(_ context.Context, name string) (*recipe.Recipe, error) {
			if r, ok := recipes[name]; ok {
				return r, nil
			}
			return nil, fmt.Errorf("unknown: %s", name)
		},
	}

	r := makeRecipe("zmx", "0.6.0", []string{"zig15"}, nil)
	r.Build.System = "zig"

	deps, err := inst.InstallBuildDeps(context.Background(), r)
	if err != nil {
		t.Fatalf("InstallBuildDeps: %v", err)
	}

	explicitBin := filepath.Join(storeRoot, "zig15", "0.15.2", "bin")
	systemBin := filepath.Join(storeRoot, "zig", "0.16.0", "bin")

	if !contains(deps.BinDirs, explicitBin) {
		t.Errorf("explicit zig15 bin not in BinDirs: %v", deps.BinDirs)
	}
	if contains(deps.BinDirs, systemBin) {
		t.Errorf("implicit zig bin leaked into BinDirs: %v", deps.BinDirs)
	}
	if !contains(deps.SystemBinDirs, systemBin) {
		t.Errorf("implicit zig bin not in SystemBinDirs: %v",
			deps.SystemBinDirs)
	}
	if contains(deps.SystemBinDirs, explicitBin) {
		t.Errorf("explicit zig15 bin leaked into SystemBinDirs: %v",
			deps.SystemBinDirs)
	}
}

func TestInstallBuildDepsTransitiveNamedDirs(t *testing.T) {
	storeRoot := t.TempDir()
	s := store.NewStore(storeRoot)

	// A depends on B, B depends on C.
	preInstall(t, s, "b", "1.0")
	preInstall(t, s, "c", "1.0")

	recipes := map[string]*recipe.Recipe{
		"b": makeRecipe("b", "1.0", []string{"c"}, nil),
		"c": makeRecipe("c", "1.0", nil, nil),
	}

	inst := &Installer{
		Store: s,
		Resolver: func(_ context.Context, name string) (*recipe.Recipe, error) {
			if r, ok := recipes[name]; ok {
				return r, nil
			}
			return nil, fmt.Errorf("unknown: %s", name)
		},
	}

	a := makeRecipe("a", "1.0", []string{"b"}, nil)
	deps, err := inst.InstallBuildDeps(context.Background(), a)
	if err != nil {
		t.Fatalf("InstallBuildDeps: %v", err)
	}

	// Both B and C should appear in NamedDirs.
	wantB := filepath.Join(storeRoot, "b", "1.0")
	wantC := filepath.Join(storeRoot, "c", "1.0")
	if deps.NamedDirs["b"] != wantB {
		t.Errorf("NamedDirs[b] = %q, want %q",
			deps.NamedDirs["b"], wantB)
	}
	if deps.NamedDirs["c"] != wantC {
		t.Errorf("NamedDirs[c] = %q, want %q",
			deps.NamedDirs["c"], wantC)
	}
}

func TestInstallBuildDepsUsesPlatformOverride(t *testing.T) {
	storeRoot := t.TempDir()
	s := store.NewStore(storeRoot)
	preInstall(t, s, "llvm", "1.0")

	inst := &Installer{
		Store: s,
		Resolver: func(_ context.Context, name string) (*recipe.Recipe, error) {
			if name == "llvm" {
				return makeRecipe("llvm", "1.0", nil, nil), nil
			}
			return nil, fmt.Errorf("unknown: %s", name)
		},
	}

	r := makeRecipe("mypkg", "1.0", []string{"cmake"}, nil)
	if r.Dependencies.Platform == nil {
		r.Dependencies.Platform = make(map[string]recipe.PlatformDependencies)
	}
	key := runtime.GOOS + "-" + runtime.GOARCH
	r.Dependencies.Platform[key] = recipe.PlatformDependencies{
		Build: []string{"llvm"},
	}

	deps, err := inst.InstallBuildDeps(context.Background(), r)
	if err != nil {
		t.Fatalf("InstallBuildDeps: %v", err)
	}

	wantLLVM := filepath.Join(storeRoot, "llvm", "1.0")
	if !contains(deps.StoreDirs, wantLLVM) {
		t.Fatalf("StoreDirs = %v, want %q", deps.StoreDirs, wantLLVM)
	}
	for _, dir := range deps.StoreDirs {
		if strings.Contains(dir, filepath.Join(storeRoot, "cmake")) {
			t.Fatalf("StoreDirs = %v, want platform override to replace default build deps", deps.StoreDirs)
		}
	}
}

// --- C4: dep constraints are enforced at resolve time ---
//
// A recipe that declares `runtime = [{name="openssl", version=">=3.5.0"}]`
// must fail the install loudly when the resolved openssl recipe is
// older than 3.5.0. The bug before C4: the constraint was parsed
// and stored, but only IsStale consulted it — installDepsInner
// resolved whatever was latest and silently proceeded.

// TestInstallBuildDepsConstraintSatisfied verifies that when the
// resolved dep satisfies the recipe's version constraint, the
// install proceeds as normal.
func TestInstallBuildDepsConstraintSatisfied(t *testing.T) {
	storeRoot := t.TempDir()
	s := store.NewStore(storeRoot)

	preInstall(t, s, "openssl", "3.5.4-1")

	inst := &Installer{
		Store: s,
		Resolver: func(_ context.Context, name string) (*recipe.Recipe, error) {
			if name == "openssl" {
				return &recipe.Recipe{
					Package: recipe.Package{
						Name:     "openssl",
						Version:  "3.5.4",
						Revision: 1,
					},
				}, nil
			}
			return nil, fmt.Errorf("unknown: %s", name)
		},
	}

	r := makeRecipe("git", "2.53.0", nil, []string{"openssl"})
	r.Dependencies.Constraints = map[string]string{
		"openssl": ">=3.5.0",
	}

	if _, err := inst.InstallBuildDeps(context.Background(), r); err != nil {
		t.Fatalf("InstallBuildDeps: unexpected error: %v", err)
	}
}

// TestInstallBuildDepsConstraintViolated verifies that when the
// resolved dep does not satisfy the constraint, the install fails
// with an error naming the dep, the constraint, and the resolved
// version.
func TestInstallBuildDepsConstraintViolated(t *testing.T) {
	storeRoot := t.TempDir()
	s := store.NewStore(storeRoot)

	inst := &Installer{
		Store: s,
		Resolver: func(_ context.Context, name string) (*recipe.Recipe, error) {
			if name == "openssl" {
				return &recipe.Recipe{
					Package: recipe.Package{
						Name:     "openssl",
						Version:  "3.4.2",
						Revision: 1,
					},
				}, nil
			}
			return nil, fmt.Errorf("unknown: %s", name)
		},
	}

	r := makeRecipe("git", "2.53.0", nil, []string{"openssl"})
	r.Dependencies.Constraints = map[string]string{
		"openssl": ">=3.5.0",
	}

	_, err := inst.InstallBuildDeps(context.Background(), r)
	if err == nil {
		t.Fatal("expected error for unsatisfied constraint, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"openssl", ">=3.5.0", "3.4.2"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

// TestInstallBuildDepsInvalidConstraintExpression verifies that a
// malformed constraint expression fails loudly rather than silently
// skipping enforcement.
func TestInstallBuildDepsInvalidConstraintExpression(t *testing.T) {
	storeRoot := t.TempDir()
	s := store.NewStore(storeRoot)

	inst := &Installer{
		Store: s,
		Resolver: func(_ context.Context, name string) (*recipe.Recipe, error) {
			if name == "openssl" {
				return &recipe.Recipe{
					Package: recipe.Package{
						Name:     "openssl",
						Version:  "3.5.0",
						Revision: 1,
					},
				}, nil
			}
			return nil, fmt.Errorf("unknown: %s", name)
		},
	}

	r := makeRecipe("git", "2.53.0", nil, []string{"openssl"})
	r.Dependencies.Constraints = map[string]string{
		"openssl": "not-a-version",
	}

	_, err := inst.InstallBuildDeps(context.Background(), r)
	if err == nil {
		t.Fatal("expected error for invalid constraint, got nil")
	}
	if !strings.Contains(err.Error(), "openssl") {
		t.Errorf("error %q should name the dep", err)
	}
}

// TestInstallBuildDepsBareDepSkipsConstraintCheck verifies that a
// bare-string dep (no constraint) resolves to whatever is latest,
// preserving today's behavior for recipes that haven't opted in.
func TestInstallBuildDepsBareDepSkipsConstraintCheck(t *testing.T) {
	storeRoot := t.TempDir()
	s := store.NewStore(storeRoot)

	preInstall(t, s, "openssl", "1.0.0-1")

	inst := &Installer{
		Store: s,
		Resolver: func(_ context.Context, name string) (*recipe.Recipe, error) {
			if name == "openssl" {
				return &recipe.Recipe{
					Package: recipe.Package{
						Name:     "openssl",
						Version:  "1.0.0",
						Revision: 1,
					},
				}, nil
			}
			return nil, fmt.Errorf("unknown: %s", name)
		},
	}

	// No Constraints map set.
	r := makeRecipe("git", "2.53.0", nil, []string{"openssl"})

	if _, err := inst.InstallBuildDeps(context.Background(), r); err != nil {
		t.Fatalf("bare dep should not be constraint-checked: %v", err)
	}
}

// TestInstallBuildDepsPlatformScopedConstraintViolated verifies that a
// version constraint scoped to the current platform (stored in
// Dependencies.Platform[key].Constraints) causes the install to fail
// when the resolved dep does not satisfy it.
//
// Before this fix, copyRecipeForDeps copied r.Dependencies.Constraints
// (the global map) into the recipe handed to installDepsInner, ignoring
// the platform-merged constraints returned by DependenciesForPlatform.
// A platform-scoped constraint was therefore silently skipped.
func TestInstallBuildDepsPlatformScopedConstraintViolated(t *testing.T) {
	storeRoot := t.TempDir()
	s := store.NewStore(storeRoot)

	// Resolver returns expat at 2.7.4, below the platform-scoped >=2.7.5-2.
	inst := &Installer{
		Store: s,
		Resolver: func(_ context.Context, name string) (*recipe.Recipe, error) {
			if name == "expat" {
				return &recipe.Recipe{
					Package: recipe.Package{
						Name:     "expat",
						Version:  "2.7.4",
						Revision: 1,
					},
				}, nil
			}
			return nil, fmt.Errorf("unknown: %s", name)
		},
	}

	platform := runtime.GOOS + "-" + runtime.GOARCH

	r := makeRecipe("git", "2.53.0", nil, []string{"expat"})
	// Constraint scoped to the current platform only — not in the global
	// Constraints map.
	r.Dependencies.Platform = map[string]recipe.PlatformDependencies{
		platform: {
			Constraints: map[string]string{
				"expat": ">=2.7.5-2",
			},
		},
	}

	_, err := inst.InstallBuildDeps(context.Background(), r)
	if err == nil {
		t.Fatal("expected error for platform-scoped constraint violation, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"expat", ">=2.7.5-2", "2.7.4"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

// TestInstallBuildDepsTransitivePlatformScopedConstraintViolated verifies
// that a platform-scoped constraint declared by a TRANSITIVE dep (a
// dep-of-a-dep) is enforced when the resolved leaf violates it.
//
// installDepsInner recurses on a transitive dep's recipe RAW — it is not
// re-wrapped by copyRecipeForDeps the way the top-level recipe is. A
// platform-scoped constraint therefore lives only in the dep's
// Dependencies.Platform map, never in its base Dependencies.Constraints.
// The C4 check must read the platform-merged map (deps.Constraints,
// recomputed via DependenciesForPlatform in each frame), not
// r.Dependencies.Constraints. Reading the base-only map drops the
// transitive platform-scoped constraint and the violating leaf installs
// silently.
//
// mid is pre-installed so inst.Install(context.Background(), mid) short-circuits to the cached
// path without validating mid's own deps — making the outer recursion the
// sole validator of leaf's constraint and isolating the buggy lookup.
func TestInstallBuildDepsTransitivePlatformScopedConstraintViolated(t *testing.T) {
	storeRoot := t.TempDir()
	s := store.NewStore(storeRoot)

	// mid is already in the store, so Install(mid) returns cached and
	// does not re-validate leaf itself.
	preInstall(t, s, "mid", "1.0.0-1")

	platform := runtime.GOOS + "-" + runtime.GOARCH

	inst := &Installer{
		Store: s,
		Resolver: func(_ context.Context, name string) (*recipe.Recipe, error) {
			switch name {
			case "mid":
				// mid declares a platform-scoped constraint on leaf.
				mid := makeRecipe("mid", "1.0.0", nil, []string{"leaf"})
				mid.Package.Revision = 1
				mid.Dependencies.Platform = map[string]recipe.PlatformDependencies{
					platform: {
						Constraints: map[string]string{
							"leaf": ">=2.7.5-2",
						},
					},
				}
				return mid, nil
			case "leaf":
				// leaf resolves below the platform-scoped constraint.
				return &recipe.Recipe{
					Package: recipe.Package{
						Name:     "leaf",
						Version:  "2.7.4",
						Revision: 1,
					},
				}, nil
			default:
				return nil, fmt.Errorf("unknown: %s", name)
			}
		},
	}

	// Top-level recipe depends on mid (no constraint at this level).
	r := makeRecipe("git", "2.53.0", nil, []string{"mid"})

	_, err := inst.InstallBuildDeps(context.Background(), r)
	if err == nil {
		t.Fatal("expected error for transitive platform-scoped constraint violation, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"leaf", ">=2.7.5-2", "2.7.4"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
