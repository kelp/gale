//go:build darwin

package build

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
)

// Mach-O constants from <mach-o/loader.h> and <mach-o/fat.h>,
// limited to what rewriteUUID needs.
const (
	machMagic64     = 0xfeedfacf
	machMagic32     = 0xfeedface
	fatMagic        = 0xcafebabe
	lcUUID          = 0x1b
	machHeaderLen64 = 32
	machHeaderLen32 = 28
	uuidCmdLen      = 24 // cmd + cmdsize + 16 uuid bytes
)

// rewriteUUID replaces every LC_UUID payload in the Mach-O at
// path with a UUID derived from a SHA-256 of the file's own
// content (with the UUID bytes zeroed), so identical content
// always carries an identical UUID.
//
// ld computes LC_UUID from a hash of the linked output, and that
// output embeds the randomized build workspace path in
// LC_ID_DYLIB / LC_LOAD_DYLIB. FixupBinaries rewrites those
// paths to @rpath afterwards, but the UUID keeps the workspace
// randomness — leaving two otherwise-identical builds of the
// same recipe differing in exactly the 16 UUID bytes plus the
// signature over them (gale-recipes#79). Recomputing the UUID
// from the normalized content restores byte reproducibility.
//
// The caller must strip the code signature first: signature
// bytes are derived from the pre-fixup content, so hashing them
// would smuggle the randomness back in. A file with no LC_UUID
// is left untouched.
func rewriteUUID(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read mach-o: %w", err)
	}
	rewrote := false
	// Hash per architecture slice, not per file: ld assigns each
	// slice of a universal binary its own UUID, and symbolication
	// tooling keys on per-slice UUIDs.
	for _, s := range machoSlices(data) {
		if len(s.uuidOffs) == 0 {
			continue
		}
		for _, off := range s.uuidOffs {
			clear(data[off : off+16])
		}
		sum := sha256.Sum256(data[s.start : s.start+s.size])
		uuid := sum[:16]
		// RFC 4122 version 3 (name/hash-based) and variant bits,
		// matching the shape of ld's own content-based UUIDs.
		uuid[6] = uuid[6]&0x0f | 0x30
		uuid[8] = uuid[8]&0x3f | 0x80
		for _, off := range s.uuidOffs {
			copy(data[off:], uuid)
		}
		rewrote = true
	}
	if !rewrote {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat mach-o: %w", err)
	}
	//nolint:gosec // G703 — path walked from gale's own build prefix
	if err := os.WriteFile(path, data, info.Mode().Perm()); err != nil {
		return fmt.Errorf("write mach-o: %w", err)
	}
	return nil
}

// machoSlice describes one architecture slice of a Mach-O file:
// its extent within the file and the absolute offsets of the
// 16-byte LC_UUID payloads found inside it. A thin file is a
// single slice spanning the whole file.
type machoSlice struct {
	start, size int
	uuidOffs    []int
}

// machoSlices splits a Mach-O file into its architecture slices
// and locates each slice's LC_UUID payloads. Unrecognized or
// truncated content yields no slices (or slices with no UUIDs),
// which callers treat as "leave the file untouched".
func machoSlices(data []byte) []machoSlice {
	if len(data) < 8 {
		return nil
	}
	if binary.BigEndian.Uint32(data) != fatMagic {
		return []machoSlice{{
			start:    0,
			size:     len(data),
			uuidOffs: thinUUIDOffsets(data, 0),
		}}
	}
	// Universal binary: fat headers are always big-endian.
	// fat_arch is 5 uint32s; offset and size are the 3rd and 4th.
	var parts []machoSlice
	narch := int(binary.BigEndian.Uint32(data[4:]))
	for i := range narch {
		rec := 8 + i*20
		if rec+20 > len(data) {
			break
		}
		start := int(binary.BigEndian.Uint32(data[rec+8:]))
		size := int(binary.BigEndian.Uint32(data[rec+12:]))
		if start+size > len(data) {
			continue
		}
		parts = append(parts, machoSlice{
			start:    start,
			size:     size,
			uuidOffs: thinUUIDOffsets(data[start:start+size], start),
		})
	}
	return parts
}

// thinUUIDOffsets scans one little-endian Mach-O slice for
// LC_UUID commands and returns the absolute offsets (slice
// offset base + local offset) of their 16-byte payloads.
func thinUUIDOffsets(d []byte, base int) []int {
	if len(d) < machHeaderLen32 {
		return nil
	}
	var hdrLen int
	switch binary.LittleEndian.Uint32(d) {
	case machMagic64:
		hdrLen = machHeaderLen64
	case machMagic32:
		hdrLen = machHeaderLen32
	default:
		// Big-endian slices (PPC-era) are not produced by any
		// toolchain gale drives; leave them untouched.
		return nil
	}
	var offs []int
	ncmds := int(binary.LittleEndian.Uint32(d[16:]))
	p := hdrLen
	for range ncmds {
		if p+8 > len(d) {
			break
		}
		cmd := binary.LittleEndian.Uint32(d[p:])
		cmdsize := int(binary.LittleEndian.Uint32(d[p+4:]))
		if cmdsize < 8 || p+cmdsize > len(d) {
			break
		}
		if cmd == lcUUID && cmdsize >= uuidCmdLen {
			offs = append(offs, base+p+8)
		}
		p += cmdsize
	}
	return offs
}

// normalizeAndResign makes a Mach-O that gale modified
// reproducible, then re-signs it: capture entitlements, strip
// the stale signature, rewrite LC_UUID from a content hash, and
// sign the result. Identical inputs yield identical bytes —
// codesign's ad-hoc signature is a pure function of the file
// content and its basename.
func normalizeAndResign(file string) error {
	ent := extractEntitlements(file)
	_ = run("codesign", "--remove-signature", file)
	if err := rewriteUUID(file); err != nil {
		return fmt.Errorf("rewrite uuid: %w", err)
	}
	return resignWithEntitlements(file, ent)
}
