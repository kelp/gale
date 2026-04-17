//go:build linux

package inspect

import (
	"debug/elf"
	"strings"
)

// readBinary parses an ELF file and returns its RUNPATH (or
// RPATH fallback) and NEEDED entries. Returns (nil, nil)
// for files that aren't ELF.
func readBinary(path string) (*binaryRefs, error) {
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
