package admit

import (
	"debug/elf"
	"debug/macho"
	"fmt"
	"os/exec"
	"strings"
)

// Native verifies Darwin signatures with codesign. Dynamic
// libraries are read from the object headers; ldd is not used
// because it can execute an untrusted ELF.
type Native struct{}

// CodeSign verifies a Darwin signature. Missing codesign is fatal.
func (Native) CodeSign(path string) error {
	cmd := exec.Command("codesign", "--verify", "--verbose=2", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("codesign --verify: %w: %s", err, bytesTrim(out))
	}
	return nil
}

// DynamicLibs lists linked libraries from ELF DT_NEEDED or
// Mach-O LC_LOAD_DYLIB. It does not run the binary.
func (Native) DynamicLibs(path string) ([]string, error) {
	kind, _, err := Classify(path)
	if err != nil {
		return nil, err
	}
	switch kind {
	case KindELF:
		return elfNeeded(path)
	case KindMachO:
		return machoDylibs(path)
	default:
		return nil, ErrNotBinary
	}
}

func elfNeeded(path string) ([]string, error) {
	f, err := elf.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read ELF deps: %w", err)
	}
	defer f.Close()
	if f.SectionByType(elf.SHT_DYNAMIC) == nil {
		return nil, nil
	}
	libs, err := f.ImportedLibraries()
	if err != nil {
		return nil, fmt.Errorf("read ELF deps: %w", err)
	}
	return libs, nil
}

func machoDylibs(path string) ([]string, error) {
	f, err := macho.Open(path)
	if err == nil {
		defer f.Close()
		return machoFileDylibs(f), nil
	}
	fat, fatErr := macho.OpenFat(path)
	if fatErr != nil {
		return nil, fmt.Errorf("read Mach-O deps: %w", err)
	}
	defer fat.Close()
	var libs []string
	for _, a := range fat.Arches {
		libs = append(libs, machoFileDylibs(a.File)...)
	}
	return libs, nil
}

func machoFileDylibs(f *macho.File) []string {
	var libs []string
	for _, load := range f.Loads {
		d, ok := load.(*macho.Dylib)
		if !ok {
			continue
		}
		libs = append(libs, d.Name)
	}
	return libs
}

func bytesTrim(b []byte) string {
	return strings.TrimSpace(string(b))
}
