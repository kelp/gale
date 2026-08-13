//go:build darwin

package build

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// --- gale-recipes#79: builds must be byte-reproducible ---
//
// ld computes LC_UUID from a hash of the linked output, and that
// content includes the randomized build workspace path baked into
// LC_ID_DYLIB. FixupBinaries rewrites the path to @rpath but must
// also normalize the UUID, or two builds of the same recipe differ
// in exactly the 16 UUID bytes (plus the signature over them).

// compileTinyObject compiles a one-function object file and
// returns its path, skipping the test if cc is unavailable.
func compileTinyObject(t *testing.T, dir string) string {
	t.Helper()
	src := filepath.Join(dir, "mylib.c")
	obj := filepath.Join(dir, "mylib.o")
	if err := os.WriteFile(src,
		[]byte("int mylib_func(void) { return 42; }\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cc", "-c", "-o", obj, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cc -c failed: %v\n%s", err, out)
	}
	return obj
}

// linkAndFixup links obj into <prefix>/lib/libmylib.dylib with the
// prefix path baked into the install name (as autotools/meson
// builds do), runs FixupBinaries over the prefix, and returns the
// fixed-up dylib bytes.
func linkAndFixup(t *testing.T, prefix, obj string) []byte {
	t.Helper()
	libDir := filepath.Join(prefix, "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	libPath := filepath.Join(libDir, "libmylib.dylib")
	cmd := exec.Command("cc", "-shared",
		"-install_name", libPath,
		"-Wl,-headerpad_max_install_names",
		"-o", libPath, obj)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cc -shared failed: %v\n%s", err, out)
	}
	if err := FixupBinaries(prefix); err != nil {
		t.Fatalf("FixupBinaries(%s): %v", prefix, err)
	}
	data, err := os.ReadFile(libPath)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestFixupBinariesReproducibleAcrossBuildDirs(t *testing.T) {
	// Two builds of the same object from workspace paths that
	// differ only in their random suffix (equal length, like two
	// CI runs) must produce byte-identical dylibs after fixup.
	dir := t.TempDir()
	obj := compileTinyObject(t, dir)

	one := linkAndFixup(t,
		filepath.Join(dir, "gale-build-1111111111"), obj)
	two := linkAndFixup(t,
		filepath.Join(dir, "gale-build-2222222222"), obj)

	if !bytes.Equal(one, two) {
		t.Errorf("fixed-up dylibs differ across build dirs:"+
			" %d vs %d bytes; first difference at byte %d",
			len(one), len(two), firstDiff(one, two))
	}
}

// firstDiff returns the offset of the first differing byte, or -1
// if one slice is a prefix of the other.
func firstDiff(a, b []byte) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	return -1
}

func TestRewriteUUIDNoopWithoutLCUUID(t *testing.T) {
	// A Mach-O linked without LC_UUID must pass through
	// untouched — no error, no byte churn.
	dir := t.TempDir()
	obj := compileTinyObject(t, dir)
	lib := filepath.Join(dir, "libmylib.dylib")
	cmd := exec.Command("cc", "-shared", "-Wl,-no_uuid",
		"-o", lib, obj)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cc -shared failed: %v\n%s", err, out)
	}
	before, err := os.ReadFile(lib)
	if err != nil {
		t.Fatal(err)
	}
	if err := rewriteUUID(lib); err != nil {
		t.Fatalf("rewriteUUID: %v", err)
	}
	after, err := os.ReadFile(lib)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("rewriteUUID modified a Mach-O without LC_UUID")
	}
}

func TestRewriteUUIDRewritesEverySliceOfFatBinary(t *testing.T) {
	// Universal binaries carry one LC_UUID per architecture
	// slice; rewriteUUID must reach both through the big-endian
	// fat header.
	dir := t.TempDir()
	src := filepath.Join(dir, "mylib.c")
	if err := os.WriteFile(src,
		[]byte("int mylib_func(void) { return 42; }\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	arm := filepath.Join(dir, "arm.dylib")
	amd := filepath.Join(dir, "amd.dylib")
	fat := filepath.Join(dir, "libmylib.dylib")
	for target, arch := range map[string]string{
		arm: "arm64", amd: "x86_64",
	} {
		cmd := exec.Command("cc", "-shared", "-arch", arch,
			"-o", target, src)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("cc -arch %s failed: %v\n%s", arch, err, out)
		}
	}
	cmd := exec.Command("lipo", "-create", arm, amd, "-output", fat)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("lipo failed: %v\n%s", err, out)
	}

	before, err := os.ReadFile(fat)
	if err != nil {
		t.Fatal(err)
	}
	var offs []int
	for _, s := range machoSlices(before) {
		offs = append(offs, s.uuidOffs...)
	}
	if len(offs) != 2 {
		t.Fatalf("machoSlices found %d LC_UUIDs in fat binary,"+
			" want 2", len(offs))
	}
	if err := rewriteUUID(fat); err != nil {
		t.Fatalf("rewriteUUID: %v", err)
	}
	after, err := os.ReadFile(fat)
	if err != nil {
		t.Fatal(err)
	}
	for _, off := range offs {
		if bytes.Equal(before[off:off+16], after[off:off+16]) {
			t.Errorf("slice UUID at offset %d not rewritten", off)
		}
	}
	// Each slice must get its own content-derived UUID, like ld
	// assigns: symbolication tooling keys on per-slice UUIDs.
	if bytes.Equal(after[offs[0]:offs[0]+16],
		after[offs[1]:offs[1]+16]) {
		t.Error("both fat slices share one UUID; want per-slice" +
			" content hashes")
	}
	// The rewritten file must still be a valid universal binary.
	if out, err := exec.Command("lipo", "-info", fat).
		CombinedOutput(); err != nil {
		t.Errorf("lipo -info after rewrite: %v\n%s", err, out)
	}
}

func TestRewriteUUIDDeterministicForIdenticalContent(t *testing.T) {
	// Identical file content must yield an identical UUID, and
	// the rewrite must change the linker-assigned one.
	dir := t.TempDir()
	obj := compileTinyObject(t, dir)
	lib := filepath.Join(dir, "libmylib.dylib")
	cmd := exec.Command("cc", "-shared", "-o", lib, obj)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cc -shared failed: %v\n%s", err, out)
	}
	copyPath := filepath.Join(dir, "copy.dylib")
	orig, err := os.ReadFile(lib)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyPath, orig, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := rewriteUUID(lib); err != nil {
		t.Fatalf("rewriteUUID: %v", err)
	}
	if err := rewriteUUID(copyPath); err != nil {
		t.Fatalf("rewriteUUID: %v", err)
	}
	one, err := os.ReadFile(lib)
	if err != nil {
		t.Fatal(err)
	}
	two, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(one, two) {
		t.Error("identical content produced differing UUIDs")
	}
	if bytes.Equal(one, orig) {
		t.Error("rewriteUUID left the linker-assigned UUID in place")
	}
}

// TestNormalizeAndResignRefusesWhenEntitlementsCannotBeRead pins
// the third gh#254 caller. normalizeAndResign captures entitlements,
// strips the signature, rewrites LC_UUID and signs the result; a
// capture that answered "" for a failed read handed
// resignWithEntitlements a signature with the entitlement removed,
// after the original had already been stripped and could no longer
// be recovered.
func TestNormalizeAndResignRefusesWhenEntitlementsCannotBeRead(t *testing.T) {
	bin := entitledBinary(t, "qemu-like")
	before, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	stubFailingCodesign(t)

	if err := normalizeAndResign(bin); err == nil {
		t.Fatal("normalizeAndResign() succeeded while the binary's " +
			"entitlements could not be read; it strips the " +
			"signature next, so a guess here is unrecoverable " +
			"(gh#254)")
	}
	after, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("normalizeAndResign() modified the binary before " +
			"failing; the entitlement capture is its first step")
	}
}

// TestHasCodeSignatureDistinguishesSignedFromUnsigned pins the probe
// the signed/unsigned split rests on. It reads the Mach-O's load
// commands rather than asking codesign, because the case that must
// be told apart is exactly the one where codesign will not answer.
func TestHasCodeSignatureDistinguishesSignedFromUnsigned(t *testing.T) {
	bin := compileTinyBinary(t, t.TempDir(), "probe")
	if out, err := exec.Command("codesign", "--force", "--sign", "-", bin).
		CombinedOutput(); err != nil {
		t.Skipf("codesign --sign - failed: %v\n%s", err, out)
	}
	signed, err := hasCodeSignature(bin)
	if err != nil {
		t.Fatalf("hasCodeSignature(signed) error = %v", err)
	}
	if !signed {
		t.Error("hasCodeSignature() = false for a signed Mach-O")
	}

	if out, err := exec.Command("codesign", "--remove-signature", bin).
		CombinedOutput(); err != nil {
		t.Fatalf("remove-signature: %v\n%s", err, out)
	}
	signed, err = hasCodeSignature(bin)
	if err != nil {
		t.Fatalf("hasCodeSignature(unsigned) error = %v", err)
	}
	if signed {
		t.Error("hasCodeSignature() = true after --remove-signature")
	}
}
