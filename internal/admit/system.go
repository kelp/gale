package admit

import (
	"path"
	"strings"
	"unicode"
)

// SystemOnly reports whether lib is a permitted system dependency
// for goos. Darwin allows /usr/lib/ and /System/. Linux allows
// the vDSO by soname, and the loader or libc only when the path
// is under a system lib directory (or the entry is a bare soname).
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
	return lib == "/usr/lib" ||
		strings.HasPrefix(lib, "/usr/lib/") ||
		lib == "/System" ||
		strings.HasPrefix(lib, "/System/")
}

func linuxSystem(lib string) bool {
	base := path.Base(lib)
	if linuxVDSO(base) {
		return !strings.HasPrefix(lib, "/")
	}
	if !linuxSystemSoname(base) {
		return false
	}
	if !strings.HasPrefix(lib, "/") {
		return true
	}
	return linuxSystemDir(lib)
}

func linuxVDSO(base string) bool {
	return base == "linux-vdso.so.1" || strings.HasPrefix(base, "linux-vdso.so.")
}

func linuxSystemSoname(base string) bool {
	if linuxLibcSoname(base) {
		return true
	}
	if strings.HasPrefix(base, "ld-linux-") || strings.HasPrefix(base, "ld-musl-") {
		return true
	}
	return false
}

func linuxLibcSoname(base string) bool {
	if base == "libc.so" {
		return true
	}
	rest, ok := strings.CutPrefix(base, "libc.so.")
	if !ok || rest == "" {
		return false
	}
	for _, r := range rest {
		if r != '.' && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func linuxSystemDir(lib string) bool {
	return strings.HasPrefix(lib, "/lib/") ||
		strings.HasPrefix(lib, "/lib64/") ||
		strings.HasPrefix(lib, "/usr/lib/") ||
		strings.HasPrefix(lib, "/usr/lib64/")
}
