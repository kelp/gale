package config

import (
	"strings"
)

// nameMap translates .tool-versions names to gale recipe
// names where they differ.
var nameMap = map[string]string{
	"golang": "go",
	"nodejs": "node",
}

// ParseToolVersions parses a .tool-versions file into a
// package map. Each line is "<name> <version> [version...]".
// Comments start with #. When multiple versions are listed,
// only the first is used. Names are mapped to gale recipe
// names where known (e.g., "golang" → "go").
func ParseToolVersions(data string) (map[string]string, error) {
	pkgs := make(map[string]string)
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := fields[0]
		version := fields[1]

		// Map tool-versions names to gale names.
		if mapped, ok := nameMap[name]; ok {
			name = mapped
		}

		pkgs[name] = version
	}
	return pkgs, nil
}

// FindToolVersions walks up from dir looking for a
// .tool-versions file. Returns the path if found, empty
// string if not.
func FindToolVersions(dir string) string {
	return findFileUp(dir, ".tool-versions")
}
