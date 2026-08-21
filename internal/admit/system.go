package admit

import (
	"path"
	"strings"
	"unicode"
)

// SystemOnly reports whether lib is a permitted system dependency
// for goos. Darwin allows /usr/lib/ and /System/Library/ after
// path.Clean (not /System/Volumes/). Linux allows a bare
// vDSO/loader/libc soname, or an absolute loader/libc path that
// cleans to a system lib directory.
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
	if !strings.HasPrefix(lib, "/") {
		return false
	}
	lib = path.Clean(lib)
	return lib == "/usr/lib" ||
		strings.HasPrefix(lib, "/usr/lib/") ||
		lib == "/System/Library" ||
		strings.HasPrefix(lib, "/System/Library/")
}

func linuxSystem(lib string) bool {
	if strings.Contains(lib, "/") {
		if !strings.HasPrefix(lib, "/") {
			return false
		}
		lib = path.Clean(lib)
		base := path.Base(lib)
		if linuxVDSO(base) {
			return false
		}
		if !linuxSystemSoname(base) {
			return false
		}
		return linuxSystemDir(lib)
	}
	if linuxVDSO(lib) {
		return true
	}
	return linuxSystemSoname(lib)
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

// linuxSearchPath reports whether a DT_RPATH / DT_RUNPATH
// entry is a system library directory after path.Clean.
func linuxSearchPath(p string) bool {
	p = strings.TrimSpace(p)
	if p == "" || !strings.HasPrefix(p, "/") {
		return false
	}
	p = path.Clean(p)
	return p == "/lib" || p == "/lib64" ||
		p == "/usr/lib" || p == "/usr/lib64" ||
		linuxSystemDir(p)
}
