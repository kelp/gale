package admit

import (
	"fmt"
	"os/exec"
	"strings"
)

// Native shells out to codesign, otool, and ldd.
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

// DynamicLibs lists linked libraries via otool or ldd.
func (n Native) DynamicLibs(path string) ([]string, error) {
	kind, _, err := Classify(path)
	if err != nil {
		return nil, err
	}
	var cmd *exec.Cmd
	switch kind {
	case KindMachO:
		cmd = exec.Command("otool", "-L", path)
	case KindELF:
		cmd = exec.Command("ldd", path)
	default:
		return nil, ErrNotBinary
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if kind == KindELF && strings.Contains(string(out), "not a dynamic executable") {
			return nil, nil
		}
		return nil, fmt.Errorf("%s: %w: %s", cmd.Args[0], err, bytesTrim(out))
	}
	return ParseDynamicLibs(string(out))
}

func bytesTrim(b []byte) string {
	return strings.TrimSpace(string(b))
}
