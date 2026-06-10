package build

import "testing"

// shouldResign gates the FixupBinaries re-sign on whether gale
// actually modified the Mach-O. This is the pure decision logic
// behind issue #27: untouched binaries (e.g. qemu's self-signed
// mains) must be left alone so their entitlements survive.
func TestShouldResign(t *testing.T) {
	cases := []struct {
		name    string
		changed bool
		inLib   bool
		want    bool
	}{
		{"untouched binary not re-signed", false, false, false},
		{"dep rewritten re-signed", true, false, true},
		{"lib dir re-signed", false, true, true},
		{"changed and in lib re-signed", true, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldResign(c.changed, c.inLib); got != c.want {
				t.Errorf("shouldResign(%v, %v) = %v, want %v",
					c.changed, c.inLib, got, c.want)
			}
		})
	}
}
