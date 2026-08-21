package admit

import (
	"fmt"
	"strings"
)

// ParseDynamicLibs extracts library paths from otool -L or ldd
// output. The first otool line (the binary path followed by ":")
// is skipped. "not a dynamic executable" yields no libraries.
func ParseDynamicLibs(out string) ([]string, error) {
	if strings.Contains(out, "not a dynamic executable") {
		return nil, nil
	}
	var libs []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasSuffix(line, ":") && !strings.Contains(line, " ") {
			continue
		}
		lib, ok := libFromLine(line)
		if !ok {
			continue
		}
		libs = append(libs, lib)
	}
	return libs, nil
}

func libFromLine(line string) (string, bool) {
	if i := strings.Index(line, " => "); i >= 0 {
		right := strings.TrimSpace(line[i+4:])
		return firstToken(right), true
	}
	tok := firstToken(line)
	if tok == "" || tok == "statically" {
		return "", false
	}
	return tok, true
}

func firstToken(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "not" {
		return ""
	}
	if i := strings.IndexAny(s, " \t("); i >= 0 {
		s = s[:i]
	}
	return s
}

// FileMap is one --file SRC:DEST:MODE entry.
type FileMap struct {
	Src  string
	Dest string
	Mode int
}

// ParseFileFlag decodes SRC:DEST:MODE. MODE is 644 or 755,
// with optional 0 or 0o prefix.
func ParseFileFlag(raw string) (FileMap, error) {
	modeAt := strings.LastIndex(raw, ":")
	if modeAt <= 0 {
		return FileMap{}, fmt.Errorf("file must be src:dest:mode")
	}
	mode, err := parseFileMode(raw[modeAt+1:])
	if err != nil {
		return FileMap{}, err
	}
	rest := raw[:modeAt]
	destAt := strings.LastIndex(rest, ":")
	if destAt <= 0 || destAt == len(rest)-1 {
		return FileMap{}, fmt.Errorf("file must be src:dest:mode")
	}
	return FileMap{Src: rest[:destAt], Dest: rest[destAt+1:], Mode: mode}, nil
}

func parseFileMode(s string) (int, error) {
	s = strings.TrimPrefix(s, "0o")
	s = strings.TrimPrefix(s, "0")
	switch s {
	case "644":
		return 0o644, nil
	case "755":
		return 0o755, nil
	default:
		return 0, fmt.Errorf("mode must be 644 or 755")
	}
}

// InferFormat maps an archive path suffix onto an admitted format.
func InferFormat(p string) (string, error) {
	switch {
	case strings.HasSuffix(p, ".tar.gz"), strings.HasSuffix(p, ".tgz"):
		return "tar.gz", nil
	case strings.HasSuffix(p, ".tar.xz"):
		return "tar.xz", nil
	case strings.HasSuffix(p, ".zip"):
		return "zip", nil
	default:
		return "", fmt.Errorf("unknown archive suffix")
	}
}
