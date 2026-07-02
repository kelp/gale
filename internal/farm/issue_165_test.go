package farm

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestIssue165DottedStemVersioned covers dotted-stem sonames
// like libpython3.14.so.1.0 (stem "python3.14"), which the
// original linuxVersioned pattern never matched because its
// stem class excluded '.'. See gh#165.
func TestIssue165DottedStemVersioned(t *testing.T) {
	type tc struct {
		os   string
		name string
		want bool
	}
	cases := []tc{
		// linux: dotted stems must match (the bug).
		{"linux", "libpython3.14.so.1.0", true},
		{"linux", "libpython3.13.so.1.0", true},
		// linux: regression guards — non-dotted still match.
		{"linux", "libssl.so.3", true},
		// linux: a dotted stem with no version stays false.
		{"linux", "libpython3.14.so", false},
		{"linux", "libfoo.so", false},
		// linux: the version suffix anchors on the last
		// ".so." occurrence, so a pathological name still
		// parses (harmless — such files don't exist).
		{"linux", "libfoo.so.1.so.2", true},
		// darwin: dotted-stem dylibs already match because
		// Mach-O versioning precedes the extension.
		{"darwin", "libpython3.14.dylib", true},
		{"darwin", "libpython3.14.1.0.dylib", true},
		{"darwin", "libfoo.dylib", false},
	}
	for _, c := range cases {
		if runtime.GOOS != c.os {
			continue
		}
		if got := IsVersionedDylib(c.name); got != c.want {
			t.Errorf("IsVersionedDylib(%q) = %v, want %v",
				c.name, got, c.want)
		}
	}
}

// TestIssue165PopulateFarmsDottedStem confirms the fix end to
// end: a dotted-stem versioned dylib lands in the farm under
// its own basename, not just that the regex matches.
func TestIssue165PopulateFarmsDottedStem(t *testing.T) {
	root := t.TempDir()
	farmDir := filepath.Join(root, "lib")
	dylib := versionedName("libpython3.14", "1.0")
	storeDir := storeLayout(t, root, "python", "3.14.0",
		[]string{dylib, aliasName("libpython3.14")})

	if err := Populate(storeDir, farmDir); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(farmDir, dylib)
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("expected farm symlink at %s: %v", link, err)
	}
	want := filepath.Join(storeDir, "lib", dylib)
	if target != want {
		t.Errorf("target = %q, want %q", target, want)
	}
}
