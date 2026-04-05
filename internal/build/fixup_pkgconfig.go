package build

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// FixupPkgConfig rewrites prefix= lines in .pc files to
// use ${pcfiledir}/../.. so they resolve correctly from
// any install location. Build-time prefixes (temp dirs,
// CI paths) become stale after install — relative paths
// always work.
func FixupPkgConfig(prefixDir string) error {
	pcDir := filepath.Join(prefixDir, "lib", "pkgconfig")
	entries, err := os.ReadDir(pcDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".pc") {
			continue
		}
		path := filepath.Join(pcDir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		lines := strings.Split(string(data), "\n")
		changed := false
		for i, line := range lines {
			switch {
			case strings.HasPrefix(line, "prefix="):
				lines[i] = "prefix=${pcfiledir}/../.."
				changed = true
			case strings.HasPrefix(line, "exec_prefix="):
				lines[i] = "exec_prefix=${prefix}"
				changed = true
			case strings.HasPrefix(line, "libdir="):
				lines[i] = "libdir=${exec_prefix}/lib"
				changed = true
			case strings.HasPrefix(line, "includedir="):
				lines[i] = "includedir=${prefix}/include"
				changed = true
			}
		}

		if changed {
			//nolint:gosec // .pc files should be world-readable
			err = os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
			if err != nil {
				return err
			}
		}
	}
	return nil
}
