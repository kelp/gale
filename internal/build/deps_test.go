package build

import (
	"slices"
	"strings"
	"testing"
)

// --- gale-recipes#79: builds must be byte-reproducible ---
//
// The installer records dep dirs in goroutine-completion order,
// which shuffles -L/-Wl,-rpath flags and PATH between runs of
// the same build — the shipped Mach-O then differs in LC_RPATH
// order and UUID. Canonicalize must give BuildDeps a stable,
// input-order-independent shape.

func TestBuildDepsCanonicalizeSortsAndDedupes(t *testing.T) {
	a := &BuildDeps{
		BinDirs:   []string{"/s/zlib/bin", "/s/cmake/bin", "/s/zlib/bin"},
		StoreDirs: []string{"/s/zlib", "/s/cmake", "/s/meson"},
	}
	b := &BuildDeps{
		BinDirs:   []string{"/s/cmake/bin", "/s/zlib/bin"},
		StoreDirs: []string{"/s/meson", "/s/zlib", "/s/cmake"},
	}
	a.Canonicalize()
	b.Canonicalize()

	if !slices.Equal(a.StoreDirs, b.StoreDirs) {
		t.Errorf("StoreDirs order depends on input order:"+
			" %v vs %v", a.StoreDirs, b.StoreDirs)
	}
	if !slices.Equal(a.BinDirs, b.BinDirs) {
		t.Errorf("BinDirs order depends on input order:"+
			" %v vs %v", a.BinDirs, b.BinDirs)
	}
	if !slices.IsSorted(a.StoreDirs) {
		t.Errorf("StoreDirs not sorted: %v", a.StoreDirs)
	}
	if len(a.BinDirs) != 2 {
		t.Errorf("BinDirs not deduped: %v", a.BinDirs)
	}
}

// --- gale#174: explicit build deps take PATH priority ---
//
// A recipe with system = "zig" gets an implicit "zig" toolchain
// merged into its build deps. When it also pins an explicit
// variant (zig15), a single lexicographically sorted BinDirs list
// puts pkg/zig/0.16.0-1/bin ('/' < '1') ahead of
// pkg/zig15/0.15.2-1/bin, so the implicit toolchain shadows the
// pin. Splitting explicit dirs (BinDirs) from implicit system-dep
// dirs (SystemBinDirs) and joining explicit-first makes the pin
// win while keeping each group deterministic.

func TestBuildEnvExplicitBinDirsBeatSystemBinDirs(t *testing.T) {
	isolateHome(t)

	t.Setenv("PATH", "/usr/bin:/bin")

	explicit := "/s/pkg/zig15/0.15.2-1/bin"
	system := "/s/pkg/zig/0.16.0-1/bin"

	env, cleanup, err := buildEnv(&BuildContext{
		PrefixDir: "/tmp/prefix",
		Jobs:      "4",
		Version:   "1.0.0",
		Deps: &BuildDeps{
			BinDirs:       []string{explicit},
			SystemBinDirs: []string{system},
		},
	})
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("buildEnv: %v", err)
	}

	path := envToMap(env)["PATH"]
	ei := strings.Index(path, explicit)
	si := strings.Index(path, system)
	if ei < 0 || si < 0 {
		t.Fatalf("PATH missing a dep dir: explicit@%d system@%d in %q",
			ei, si, path)
	}
	if ei > si {
		t.Errorf("explicit dir must precede system dir in PATH:"+
			" explicit@%d system@%d in %q", ei, si, path)
	}
}

// TestBuildDepsCanonicalizeSortsSystemBinDirs asserts the
// implicit group is sorted and deduped independently of BinDirs,
// so PATH stays byte-reproducible across dep-install orderings.
func TestBuildDepsCanonicalizeSortsSystemBinDirs(t *testing.T) {
	a := &BuildDeps{
		SystemBinDirs: []string{"/s/zig/bin", "/s/go/bin", "/s/zig/bin"},
	}
	b := &BuildDeps{
		SystemBinDirs: []string{"/s/go/bin", "/s/zig/bin"},
	}
	a.Canonicalize()
	b.Canonicalize()

	if !slices.Equal(a.SystemBinDirs, b.SystemBinDirs) {
		t.Errorf("SystemBinDirs order depends on input order:"+
			" %v vs %v", a.SystemBinDirs, b.SystemBinDirs)
	}
	if !slices.IsSorted(a.SystemBinDirs) {
		t.Errorf("SystemBinDirs not sorted: %v", a.SystemBinDirs)
	}
	if len(a.SystemBinDirs) != 2 {
		t.Errorf("SystemBinDirs not deduped: %v", a.SystemBinDirs)
	}
}
