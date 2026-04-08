package installer

import (
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
		Resolver: func(name string) (*recipe.Recipe, error) {
			if name == "libfoo" {
				return makeRecipe("libfoo", "1.0", nil, nil), nil
			}
			return nil, fmt.Errorf("unknown: %s", name)
		},
	}

	r := makeRecipe("mypkg", "2.0", nil, []string{"libfoo"})
	deps, err := inst.InstallBuildDeps(r)
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
		Resolver: func(name string) (*recipe.Recipe, error) {
			if r, ok := recipes[name]; ok {
				return r, nil
			}
			return nil, fmt.Errorf("unknown: %s", name)
		},
	}

	a := makeRecipe("a", "1.0", []string{"b"}, nil)
	deps, err := inst.InstallBuildDeps(a)
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
		Resolver: func(name string) (*recipe.Recipe, error) {
			if r, ok := recipes[name]; ok {
				return r, nil
			}
			return nil, fmt.Errorf("unknown: %s", name)
		},
	}

	// Should not infinite loop.
	r := makeRecipe("root", "1.0", []string{"a"}, nil)
	deps, err := inst.InstallBuildDeps(r)
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
		Resolver: func(name string) (*recipe.Recipe, error) {
			if r, ok := recipes[name]; ok {
				return r, nil
			}
			return nil, fmt.Errorf("unknown: %s", name)
		},
	}

	a := makeRecipe("a", "1.0", []string{"b", "c"}, nil)
	deps, err := inst.InstallBuildDeps(a)
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
		Resolver: func(name string) (*recipe.Recipe, error) {
			return nil, fmt.Errorf("should not be called")
		},
	}

	r := makeRecipe("mypkg", "1.0", nil, nil)
	deps, err := inst.InstallBuildDeps(r)
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
		Resolver: func(name string) (*recipe.Recipe, error) {
			if r, ok := recipes[name]; ok {
				return r, nil
			}
			return nil, fmt.Errorf("unknown: %s", name)
		},
	}

	a := makeRecipe("a", "1.0", []string{"b"}, nil)
	deps, err := inst.InstallBuildDeps(a)
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
		Resolver: func(name string) (*recipe.Recipe, error) {
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

	deps, err := inst.InstallBuildDeps(r)
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

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
