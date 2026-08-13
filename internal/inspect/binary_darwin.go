//go:build darwin

package inspect

import "debug/macho"

// binaryMagics is every 4-byte word a Mach-O file can start with:
// thin headers in both byte orders and both widths, plus the
// universal-binary wrapper in both. The two 64-bit fat magics
// (0xcafebabf and its swap) are listed although macho.OpenFat rejects
// them — the filter must never answer "not a binary" where the parser
// would have looked, and a file the parser then declines still comes
// back (nil, nil) exactly as before.
var binaryMagics = [][4]byte{
	{0xfe, 0xed, 0xfa, 0xce}, // MH_MAGIC (32-bit)
	{0xce, 0xfa, 0xed, 0xfe}, // MH_CIGAM (32-bit, swapped)
	{0xfe, 0xed, 0xfa, 0xcf}, // MH_MAGIC_64
	{0xcf, 0xfa, 0xed, 0xfe}, // MH_CIGAM_64
	{0xca, 0xfe, 0xba, 0xbe}, // FAT_MAGIC
	{0xbe, 0xba, 0xfe, 0xca}, // FAT_CIGAM
	{0xca, 0xfe, 0xba, 0xbf}, // FAT_MAGIC_64
	{0xbf, 0xba, 0xfe, 0xca}, // FAT_CIGAM_64
}

// readBinary parses a Mach-O file and returns its LC_RPATH
// and LC_LOAD_DYLIB entries. Returns (nil, nil) for files
// that aren't Mach-O, so callers can skip them silently.
func readBinary(path string) (*binaryRefs, error) {
	// One open to reject the documentation, headers and share/ data
	// that dominate an install, before two parser opens can charge
	// for them.
	if !hasBinaryMagic(path) {
		return nil, nil //nolint:nilnil // not a Mach-O; skipped silently
	}
	f, err := macho.Open(path)
	if err != nil {
		// Try fat binaries — macho.Open only handles thin.
		fat, fatErr := macho.OpenFat(path)
		if fatErr != nil {
			return nil, nil //nolint:nilerr // not a Mach-O
		}
		defer fat.Close()
		if len(fat.Arches) == 0 {
			return &binaryRefs{}, nil
		}
		// Use the first arch — rpaths/deps are the same
		// across arches in a universal binary.
		return extractMachO(fat.Arches[0].File), nil
	}
	defer f.Close()
	return extractMachO(f), nil
}

func extractMachO(f *macho.File) *binaryRefs {
	refs := &binaryRefs{}
	for _, load := range f.Loads {
		switch cmd := load.(type) {
		case *macho.Rpath:
			refs.rpaths = append(refs.rpaths, cmd.Path)
		case *macho.Dylib:
			refs.deps = append(refs.deps, cmd.Name)
		}
	}
	return refs
}
