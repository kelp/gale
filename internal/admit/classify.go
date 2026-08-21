// Package admit inspects a placed artifact tree and emits an
// index fragment. It is the only intended producer of tree_digest
// for index entries.
package admit

import (
	"debug/elf"
	"debug/macho"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// Kind is the object format of an inspectable binary.
type Kind int

const (
	// KindNone is a regular file that is not Mach-O or ELF.
	KindNone Kind = iota
	// KindELF is a Linux ELF object.
	KindELF
	// KindMachO is a Darwin Mach-O object.
	KindMachO
)

// ErrNotBinary reports a file with no Mach-O or ELF header.
var ErrNotBinary = errors.New("not a binary")

// Classify returns the object kind and Go arch of path.
func Classify(path string) (Kind, string, error) {
	if kind, arch, err := classifyELF(path); err == nil {
		return kind, arch, nil
	}
	if kind, arch, err := classifyMachO(path); err == nil {
		return kind, arch, nil
	}
	return KindNone, "", ErrNotBinary
}

func classifyELF(path string) (Kind, string, error) {
	f, err := elf.Open(path)
	if err != nil {
		return KindNone, "", err
	}
	defer f.Close()
	arch, err := elfArch(f.Machine)
	if err != nil {
		return KindNone, "", err
	}
	return KindELF, arch, nil
}

func classifyMachO(path string) (Kind, string, error) {
	f, err := macho.Open(path)
	if err == nil {
		defer f.Close()
		arch, err := machoArch(f.Cpu)
		if err != nil {
			return KindNone, "", err
		}
		return KindMachO, arch, nil
	}
	fat, fatErr := macho.OpenFat(path)
	if fatErr != nil {
		return KindNone, "", err
	}
	defer fat.Close()
	return classifyFat(fat)
}

func classifyFat(fat *macho.FatFile) (Kind, string, error) {
	var arches []string
	for _, a := range fat.Arches {
		arch, err := machoArch(a.Cpu)
		if err != nil {
			return KindNone, "", err
		}
		arches = append(arches, arch)
	}
	if len(arches) == 0 {
		return KindNone, "", fmt.Errorf("fat Mach-O has no arches")
	}
	return KindMachO, strings.Join(arches, ","), nil
}

func elfArch(m elf.Machine) (string, error) {
	switch m {
	case elf.EM_X86_64:
		return "amd64", nil
	case elf.EM_AARCH64:
		return "arm64", nil
	default:
		return "", fmt.Errorf("unsupported ELF machine %s", m)
	}
}

func machoArch(cpu macho.Cpu) (string, error) {
	switch cpu {
	case macho.CpuAmd64:
		return "amd64", nil
	case macho.CpuArm64:
		return "arm64", nil
	default:
		return "", fmt.Errorf("unsupported Mach-O cpu %s", cpu)
	}
}

// MachOARM64Stub is a minimal MH_EXECUTE arm64 header that
// debug/macho can parse. Tests use it on Linux.
func MachOARM64Stub() []byte {
	var b [32]byte
	binary.LittleEndian.PutUint32(b[0:], macho.Magic64)
	binary.LittleEndian.PutUint32(b[4:], uint32(macho.CpuArm64))
	binary.LittleEndian.PutUint32(b[12:], uint32(macho.TypeExec))
	return b[:]
}

// MachOFatARM64Stub is a one-arch universal binary wrapping
// MachOARM64Stub. macho.Open rejects it; OpenFat must accept it.
func MachOFatARM64Stub() []byte {
	thin := MachOARM64Stub()
	const offset, thinSize = 32, 32
	if len(thin) != thinSize {
		panic("MachOARM64Stub size")
	}
	b := make([]byte, offset+thinSize)
	binary.BigEndian.PutUint32(b[0:], macho.MagicFat)
	binary.BigEndian.PutUint32(b[4:], 1)
	binary.BigEndian.PutUint32(b[8:], uint32(macho.CpuArm64))
	binary.BigEndian.PutUint32(b[16:], offset)
	binary.BigEndian.PutUint32(b[20:], thinSize)
	copy(b[offset:], thin)
	return b
}

// ELFStub is a minimal ELF64 header debug/elf can parse.
func ELFStub(goarch string) []byte {
	var machine uint16
	switch goarch {
	case "arm64":
		machine = uint16(elf.EM_AARCH64)
	default:
		machine = uint16(elf.EM_X86_64)
	}
	b := make([]byte, 64)
	b[0], b[1], b[2], b[3] = 0x7f, 'E', 'L', 'F'
	b[4] = 2                                 // ELFCLASS64
	b[5] = 1                                 // ELFDATA2LSB
	b[6] = 1                                 // EV_CURRENT
	binary.LittleEndian.PutUint16(b[16:], 2) // ET_EXEC
	binary.LittleEndian.PutUint16(b[18:], machine)
	binary.LittleEndian.PutUint32(b[20:], 1)
	binary.LittleEndian.PutUint16(b[52:], 64)
	return b
}

// ELFInterpStub is a minimal ELF64 with PT_INTERP. Tests use it
// to prove DynamicLibs reads the loader path without ldd.
func ELFInterpStub(goarch, interp string) []byte {
	hdr := ELFStub(goarch)
	const (
		ehsize    = 64
		phentsize = 56
		phoff     = 64
	)
	interp = strings.TrimRight(interp, "\x00") + "\x00"
	off := phoff + phentsize
	b := make([]byte, off+len(interp))
	copy(b, hdr)
	binary.LittleEndian.PutUint64(b[32:], phoff)
	binary.LittleEndian.PutUint16(b[52:], ehsize)
	binary.LittleEndian.PutUint16(b[54:], phentsize)
	binary.LittleEndian.PutUint16(b[56:], 1)
	binary.LittleEndian.PutUint32(b[phoff:], 3) // PT_INTERP
	binary.LittleEndian.PutUint32(b[phoff+4:], 4)
	binary.LittleEndian.PutUint64(b[phoff+8:], uint64(off))
	binary.LittleEndian.PutUint64(b[phoff+32:], uint64(len(interp)))
	binary.LittleEndian.PutUint64(b[phoff+40:], uint64(len(interp)))
	binary.LittleEndian.PutUint64(b[phoff+48:], 1)
	copy(b[off:], interp)
	return b
}
