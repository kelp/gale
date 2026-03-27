//go:build linux

package build

// FixupBinaries rewrites ELF rpath entries so binaries
// find shared libraries relative to themselves.
func FixupBinaries(prefixDir string) error {
	// TODO: implement with patchelf
	return nil
}
