package admit

import (
	"path"
	"strings"
)

// SystemOnly reports whether lib is a permitted system dependency
// for goos. Darwin allows /usr/lib and /System prefixes. Linux
// allows the dynamic loader, libc, and the vDSO by name, not by
// a /lib* prefix.
func SystemOnly(goos, lib string) bool {
	lib = strings.TrimSpace(lib)
	if lib == "" {
		return false
	}
	switch goos {
	case "darwin":
		return darwinSystem(lib)
	case "linux":
		return linuxSystem(lib)
	default:
		return false
	}
}

func darwinSystem(lib string) bool {
	return strings.HasPrefix(lib, "/usr/lib") ||
		strings.HasPrefix(lib, "/System")
}

func linuxSystem(lib string) bool {
	base := path.Base(lib)
	if base == "linux-vdso.so.1" || strings.HasPrefix(base, "linux-vdso.so.") {
		return true
	}
	if strings.HasPrefix(base, "libc.so") {
		return true
	}
	if strings.HasPrefix(base, "ld-linux-") || strings.HasPrefix(base, "ld-musl-") {
		return true
	}
	return false
}
