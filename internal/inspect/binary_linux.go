//go:build linux

package inspect

import (
	"debug/elf"
	"strings"
)

// binaryMagics is the one word an ELF file can start with:
// \x7fELF, byte order and class encoded later in the header.
var binaryMagics = [][4]byte{
	{0x7f, 'E', 'L', 'F'},
}

// readBinary parses an ELF file and returns its RUNPATH (or
// RPATH fallback) and NEEDED entries. Returns (nil, nil)
// for files that aren't ELF.
func readBinary(path string) (*binaryRefs, error) {
	// One open to reject the non-binaries that dominate an install
	// before elf.Open reads and validates a header for them.
	if !hasBinaryMagic(path) {
		return nil, nil //nolint:nilnil // not an ELF; skipped silently
	}
	f, err := elf.Open(path)
	if err != nil {
		return nil, nil //nolint:nilerr // not ELF
	}
	defer f.Close()

	refs := &binaryRefs{}

	needed, err := f.DynString(elf.DT_NEEDED)
	if err == nil {
		refs.deps = append(refs.deps, needed...)
	}

	// Prefer DT_RUNPATH, fall back to DT_RPATH. Both are
	// colon-separated lists.
	if rp, err := f.DynString(elf.DT_RUNPATH); err == nil && len(rp) > 0 {
		refs.rpaths = splitColon(rp[0])
	} else if rp, err := f.DynString(elf.DT_RPATH); err == nil && len(rp) > 0 {
		refs.rpaths = splitColon(rp[0])
	}

	return refs, nil
}

func splitColon(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ":") {
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}
