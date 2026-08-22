package index

import (
	"fmt"
	"path"
	"strings"
)

func lintFiles(issues *[]Issue, p string, files []FileEntry) {
	if len(files) == 0 {
		add(issues, p+".files", "files is required")
		return
	}
	for i, fe := range files {
		fp := fmt.Sprintf("%s.files[%d]", p, i)
		lintRelPath(issues, fp+".src", fe.Src)
		lintRelPath(issues, fp+".dest", fe.Dest)
		if fe.Dest != "" && isGaleSidecar(path.Base(fe.Dest)) {
			add(issues, fp+".dest", "dest basename is reserved")
		}
		if fe.Mode != 0o644 && fe.Mode != 0o755 {
			add(issues, fp+".mode", "mode must be 0o644 or 0o755")
		}
	}
	lintDestCollisions(issues, p, files)
}

func lintRelPath(issues *[]Issue, p, raw string) {
	if raw == "" {
		add(issues, p, "path is required")
		return
	}
	if !cleanRelPath(raw) {
		add(issues, p, "path is not a clean relative slash path")
	}
}

func cleanRelPath(raw string) bool {
	if raw == "" || raw == "." {
		return false
	}
	if strings.ContainsAny(raw, "\\\x00\n\r") {
		return false
	}
	if path.IsAbs(raw) || path.Clean(raw) != raw {
		return false
	}
	for _, part := range strings.Split(raw, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func lintDestCollisions(issues *[]Issue, p string, files []FileEntry) {
	for i := range files {
		a := files[i].Dest
		if a == "" || !cleanRelPath(a) {
			continue
		}
		for j := i + 1; j < len(files); j++ {
			b := files[j].Dest
			if b == "" || !cleanRelPath(b) {
				continue
			}
			jp := fmt.Sprintf("%s.files[%d].dest", p, j)
			if a == b {
				add(issues, jp, "dest is not unique")
				continue
			}
			if strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/") {
				add(issues, jp, "dest overlaps another dest")
			}
		}
	}
}

// isGaleSidecar matches download.isGaleSidecar. index stays a
// leaf, so the two-line predicate is duplicated on purpose.
func isGaleSidecar(base string) bool {
	return strings.HasPrefix(base, ".gale-") && strings.HasSuffix(base, ".toml")
}
