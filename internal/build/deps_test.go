package build

import (
	"slices"
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
