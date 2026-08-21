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
	if err != nil {
		return KindNone, "", err
	}
	defer f.Close()
	arch, err := machoArch(f.Cpu)
	if err != nil {
		return KindNone, "", err
	}
	return KindMachO, arch, nil
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
