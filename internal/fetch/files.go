package fetch

import (
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/kelp/gale/internal/index"
)

func fileMode(mode int) (os.FileMode, error) {
	switch mode {
	case 0o644:
		return 0o644, nil
	case 0o755:
		return 0o755, nil
	default:
		return 0, fmt.Errorf("mode must be 0o644 or 0o755")
	}
}

func validateFiles(files []index.FileEntry) error {
	if len(files) == 0 {
		return fmt.Errorf("files is required")
	}
	for i, fe := range files {
		if err := checkRelPath(fe.Src); err != nil {
			return fmt.Errorf("files[%d].src: %w", i, err)
		}
		if err := checkRelPath(fe.Dest); err != nil {
			return fmt.Errorf("files[%d].dest: %w", i, err)
		}
		if isGaleSidecar(path.Base(fe.Dest)) {
			return fmt.Errorf("files[%d].dest: dest basename is reserved", i)
		}
		if _, err := fileMode(fe.Mode); err != nil {
			return fmt.Errorf("files[%d].mode: %w", i, err)
		}
	}
	return checkDestCollisions(files)
}

func checkRelPath(raw string) error {
	if raw == "" {
		return fmt.Errorf("path is required")
	}
	if !cleanRelPath(raw) {
		return fmt.Errorf("path is not a clean relative slash path")
	}
	return nil
}

// cleanRelPath matches index.lint_files cleanRelPath. Copied on
// purpose: index does not export the predicate, and ToStore must
// not inherit safety from a prior Lint.
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

func checkDestCollisions(files []index.FileEntry) error {
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
			if a == b {
				return fmt.Errorf("files[%d].dest: dest is not unique", j)
			}
			if strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/") {
				return fmt.Errorf("files[%d].dest: dest overlaps another dest", j)
			}
		}
	}
	return nil
}

// isGaleSidecar matches download.isGaleSidecar. fetch stays a
// leaf of download, so the two-line predicate is duplicated.
func isGaleSidecar(base string) bool {
	return strings.HasPrefix(base, ".gale-") && strings.HasSuffix(base, ".toml")
}
