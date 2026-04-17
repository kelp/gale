//go:build darwin

package inspect

import "debug/macho"

// readBinary parses a Mach-O file and returns its LC_RPATH
// and LC_LOAD_DYLIB entries. Returns (nil, nil) for files
// that aren't Mach-O, so callers can skip them silently.
func readBinary(path string) (*binaryRefs, error) {
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
