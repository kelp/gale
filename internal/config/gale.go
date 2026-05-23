package config

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/kelp/gale/internal/atomicfile"
	"github.com/kelp/gale/internal/filelock"
)

// ErrGaleConfigNotFound is returned when gale.toml cannot be
// found in the directory tree.
var ErrGaleConfigNotFound = errors.New(
	"gale.toml not found",
)

// ErrPackageNotFound is returned when a package to remove does
// not exist in the config.
var ErrPackageNotFound = errors.New("package not found")

// HostConfig represents a per-host packages/pinned overlay
// stored under [hosts.<name>] in gale.toml.
type HostConfig struct {
	Packages map[string]string `toml:"packages,omitempty"`
	Pinned   map[string]bool   `toml:"pinned,omitempty"`
}

// GaleConfig represents a gale.toml file (global or project).
type GaleConfig struct {
	Packages map[string]string     `toml:"packages"`
	Vars     map[string]string     `toml:"vars,omitempty"`
	Pinned   map[string]bool       `toml:"pinned,omitempty"`
	Hosts    map[string]HostConfig `toml:"hosts,omitempty"`
}

// ParseGaleConfig parses a gale.toml string.
func ParseGaleConfig(data string) (*GaleConfig, error) {
	var cfg GaleConfig
	if _, err := toml.Decode(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing gale config: %w", err)
	}
	return &cfg, nil
}

// EffectivePackages returns the shared [packages] merged with
// every [hosts.<key>.packages] section whose key matches host.
// Host section keys may list multiple comma-separated patterns
// and use glob wildcards (*, ?). Wildcard-bearing sections are
// applied first; exact-name sections last, so exact entries
// override globs. Does not mutate the receiver.
func (c *GaleConfig) EffectivePackages(host string) map[string]string {
	out := make(map[string]string, len(c.Packages))
	maps.Copy(out, c.Packages)
	if host == "" {
		return out
	}
	for _, k := range matchingHostKeys(c.Hosts, host) {
		maps.Copy(out, c.Hosts[k].Packages)
	}
	return out
}

// ApplyHost replaces Packages and Pinned with the effective
// merged maps for the given host. Mutates the receiver.
// Callers that need the raw on-disk view (e.g. mutators) must
// not call this.
func (c *GaleConfig) ApplyHost(host string) {
	c.Packages = c.EffectivePackages(host)
	c.Pinned = c.EffectivePinned(host)
}

// EffectivePinned merges shared [pinned] with every matching
// [hosts.<key>.pinned] overlay, using the same multi-pattern
// matching and override order as EffectivePackages. Does not
// mutate the receiver.
func (c *GaleConfig) EffectivePinned(host string) map[string]bool {
	out := make(map[string]bool, len(c.Pinned))
	maps.Copy(out, c.Pinned)
	if host == "" {
		return out
	}
	for _, k := range matchingHostKeys(c.Hosts, host) {
		maps.Copy(out, c.Hosts[k].Pinned)
	}
	return out
}

// HostKeyMatches reports whether sectionKey applies to the
// given host. The key is a comma-separated list of glob
// patterns; any matching pattern returns true.
func HostKeyMatches(sectionKey, host string) bool {
	for pat := range strings.SplitSeq(sectionKey, ",") {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			continue
		}
		if pat == host {
			return true
		}
		if ok, err := filepath.Match(pat, host); err == nil && ok {
			return true
		}
	}
	return false
}

// hostKeySpecificity ranks a section key from least to most
// specific so callers can apply broader sections first and
// let narrower ones override. Order: glob (0) < comma-list
// of literals (1) < single literal name (2).
func hostKeySpecificity(sectionKey string) int {
	if strings.ContainsAny(sectionKey, "*?[") {
		return 0
	}
	if strings.Contains(sectionKey, ",") {
		return 1
	}
	return 2
}

// matchingHostKeys returns the host section keys that apply
// to host, sorted from least to most specific so the caller
// can apply each section in order — later sections override
// earlier ones, so exact-name entries win over comma-lists,
// which in turn win over globs. Within each tier, keys are
// sorted alphabetically for deterministic merge order.
func matchingHostKeys(hosts map[string]HostConfig, host string) []string {
	keys := make([]string, 0, len(hosts))
	for k := range hosts {
		if HostKeyMatches(k, host) {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		si := hostKeySpecificity(keys[i])
		sj := hostKeySpecificity(keys[j])
		if si != sj {
			return si < sj
		}
		return keys[i] < keys[j]
	})
	return keys
}

// CurrentHost returns the active host identifier. Reads
// $GALE_HOST first; falls back to os.Hostname(). Returns "" if
// both fail.
func CurrentHost() string {
	if h := os.Getenv("GALE_HOST"); h != "" {
		return h
	}
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}

// FindGaleConfig walks up from dir to find gale.toml.
// Returns the path to the found file.
func FindGaleConfig(dir string) (string, error) {
	path := findFileUp(dir, "gale.toml")
	if path == "" {
		return "", ErrGaleConfigNotFound
	}
	return path, nil
}

// findFileUp walks up from dir looking for a file with the
// given name. Returns the full path if found, empty if not.
func findFileUp(dir, name string) string {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for {
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// WriteGaleConfig writes a GaleConfig to the given path atomically.
func WriteGaleConfig(path string, cfg *GaleConfig) error {
	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	if err := enc.Encode(cfg); err != nil {
		return fmt.Errorf("encoding gale config: %w", err)
	}
	return atomicfile.Write(path, buf.Bytes())
}

// withFileLock acquires an exclusive file lock on a .lock
// sibling of path, runs fn, and releases the lock. This
// serializes concurrent read-modify-write operations.
func withFileLock(path string, fn func() error) error {
	return filelock.With(path+".lock", fn)
}

// readOrEmpty reads the file at path. If the file does not exist,
// returns empty bytes and nil error. Otherwise returns the file
// contents or an error.
func readOrEmpty(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []byte{}, nil
		}
		return nil, err
	}
	return data, nil
}

// splitLines splits content by '\n'.
func splitLines(content []byte) []string {
	return strings.Split(string(content), "\n")
}

// sectionLineIndex scans lines for a line whose trimmed content
// equals "[section]". Returns the line index or -1 if not found.
func sectionLineIndex(lines []string, section string) int {
	target := "[" + section + "]"
	for i, line := range lines {
		if strings.TrimSpace(line) == target {
			return i
		}
	}
	return -1
}

// nextSectionIndex finds the next line (starting from fromLine,
// exclusive) that begins a new TOML section (trimmed content
// starts with '['). Returns len(lines) if no next section.
func nextSectionIndex(lines []string, fromLine int) int {
	for i := fromLine; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "[") {
			return i
		}
	}
	return len(lines)
}

// keyLineIndex searches within lines[sectionStart:sectionEnd] for
// a line that sets key. A line sets key if, after trimming leading
// whitespace, it starts with key followed by ' =' or '="'.
// Returns the absolute line index within lines, or -1 if not found.
func keyLineIndex(lines []string, sectionStart, sectionEnd int, key string) int {
	prefix1 := key + " ="
	prefix2 := key + "="
	for i := sectionStart; i < sectionEnd; i++ {
		trimmed := strings.TrimLeft(lines[i], " \t")
		if strings.HasPrefix(trimmed, prefix1) || strings.HasPrefix(trimmed, prefix2) {
			return i
		}
	}
	return -1
}

// setTOMLStringKey sets key = "value" in the specified TOML section,
// preserving all other content verbatim. If the section does not exist
// it is appended. If the key already exists in the section it is
// updated in place; otherwise the key is inserted just before the end
// of the section.
func setTOMLStringKey(content []byte, section, key, value string) []byte {
	lines := splitLines(content)
	secIdx := sectionLineIndex(lines, section)

	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	newLine := "  " + key + " = \"" + escaped + "\""

	if secIdx < 0 {
		// Section not found — append it.
		result := content
		if len(result) > 0 && result[len(result)-1] != '\n' {
			result = append(result, '\n')
		}
		result = append(result, []byte("\n["+section+"]\n"+newLine+"\n")...)
		return result
	}

	// Section found: determine body range.
	bodyStart := secIdx + 1
	bodyEnd := nextSectionIndex(lines, bodyStart)

	// Look for existing key.
	keyIdx := keyLineIndex(lines, bodyStart, bodyEnd, key)
	if keyIdx >= 0 {
		// Replace the existing key line.
		lines[keyIdx] = newLine
	} else {
		// Insert before end of section.
		// Find insertion point: last non-empty line in body, then insert after it.
		insertAt := bodyEnd
		// Walk backwards to skip trailing blank lines at end of body.
		for insertAt > bodyStart && strings.TrimSpace(lines[insertAt-1]) == "" {
			insertAt--
		}
		// Insert the new line.
		newLines := make([]string, 0, len(lines)+1)
		newLines = append(newLines, lines[:insertAt]...)
		newLines = append(newLines, newLine)
		newLines = append(newLines, lines[insertAt:]...)
		lines = newLines
	}

	return []byte(strings.Join(lines, "\n"))
}

// deleteTOMLKey removes key from section in content. Returns
// (modified content, true) if found and removed, (original, false)
// otherwise.
func deleteTOMLKey(content []byte, section, key string) ([]byte, bool) {
	lines := splitLines(content)
	secIdx := sectionLineIndex(lines, section)
	if secIdx < 0 {
		return content, false
	}

	bodyStart := secIdx + 1
	bodyEnd := nextSectionIndex(lines, bodyStart)
	keyIdx := keyLineIndex(lines, bodyStart, bodyEnd, key)
	if keyIdx < 0 {
		return content, false
	}

	newLines := make([]string, 0, len(lines)-1)
	newLines = append(newLines, lines[:keyIdx]...)
	newLines = append(newLines, lines[keyIdx+1:]...)
	return []byte(strings.Join(newLines, "\n")), true
}

// hostPinned returns the pinned map for cfg.Hosts[host],
// creating the host entry if needed.
func hostPinned(cfg *GaleConfig, host string) map[string]bool {
	if cfg.Hosts == nil {
		cfg.Hosts = make(map[string]HostConfig)
	}
	h := cfg.Hosts[host]
	if h.Pinned == nil {
		h.Pinned = make(map[string]bool)
	}
	cfg.Hosts[host] = h
	return cfg.Hosts[host].Pinned
}

// UpsertPackage updates a package in gale.toml, preserving its
// existing location. If the package is present in the current
// host's section, it is updated there; otherwise it is written
// to the shared [packages] section. Used by install/update flows
// that should not move a host-scoped package to the shared
// section. host may be empty (no preservation; equivalent to
// AddPackage(path, "", ...)).
func UpsertPackage(path, host, name, version string) error {
	return withFileLock(path, func() error {
		content, err := readOrEmpty(path)
		if err != nil {
			return err
		}
		section := "packages"
		if host != "" {
			// Check if the package is already in the host section.
			hostSection := "hosts." + host + ".packages"
			lines := splitLines(content)
			secIdx := sectionLineIndex(lines, hostSection)
			if secIdx >= 0 {
				endIdx := nextSectionIndex(lines, secIdx+1)
				if keyLineIndex(lines, secIdx+1, endIdx, name) >= 0 {
					section = hostSection
				}
			}
		}
		content = setTOMLStringKey(content, section, name, version)
		return atomicfile.Write(path, content)
	})
}

// AddPackage adds or updates a package in the gale.toml at path.
// When host is empty, the package is written to the shared
// [packages] section. When non-empty, it is written under
// [hosts.<host>.packages]. If the file does not exist, it is
// bootstrapped.
func AddPackage(path, host, name, version string) error {
	return withFileLock(path, func() error {
		section := "packages"
		if host != "" {
			section = "hosts." + host + ".packages"
		}
		content, err := readOrEmpty(path)
		if err != nil {
			return err
		}
		content = setTOMLStringKey(content, section, name, version)
		return atomicfile.Write(path, content)
	})
}

// RemovePackage removes a package from the gale.toml at path.
// When host is empty, removes from shared [packages]; otherwise
// from [hosts.<host>.packages]. Returns ErrPackageNotFound if
// the package is not present in the targeted section.
func RemovePackage(path, host, name string) error {
	return withFileLock(path, func() error {
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading gale config: %w", err)
		}
		section := "packages"
		if host != "" {
			section = "hosts." + host + ".packages"
		}
		modified, found := deleteTOMLKey(content, section, name)
		if !found {
			return ErrPackageNotFound
		}
		return atomicfile.Write(path, modified)
	})
}

// PinPackage marks a package as pinned in the gale.toml at path.
// When host is empty, the pin is recorded in shared [pinned] and
// the package must exist in shared [packages]. Otherwise the pin
// is recorded under [hosts.<host>.pinned] and the package must
// exist in that host's package list. Returns ErrPackageNotFound
// when the package is not in the targeted section.
//
// TODO(0012-0014): PinPackage uses struct round-trip (WriteGaleConfig)
// which strips comments and drops unknown sections. Convert to text-based
// edit when setTOMLBoolKey is added.
func PinPackage(path, host, name string) error {
	return withFileLock(path, func() error {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading gale config: %w", err)
		}
		cfg, err := ParseGaleConfig(string(data))
		if err != nil {
			return err
		}

		if host == "" {
			if _, ok := cfg.Packages[name]; !ok {
				return ErrPackageNotFound
			}
			if cfg.Pinned == nil {
				cfg.Pinned = make(map[string]bool)
			}
			cfg.Pinned[name] = true
		} else {
			h, ok := cfg.Hosts[host]
			if !ok {
				return ErrPackageNotFound
			}
			if _, ok := h.Packages[name]; !ok {
				return ErrPackageNotFound
			}
			hostPinned(cfg, host)[name] = true
		}

		return WriteGaleConfig(path, cfg)
	})
}

// UnpinPackage removes a pin from the gale.toml at path.
// When host is empty, removes from shared [pinned]; otherwise
// from [hosts.<host>.pinned]. Missing pins are a no-op.
//
// TODO(0012-0014): UnpinPackage uses struct round-trip (WriteGaleConfig)
// which strips comments and drops unknown sections. Convert to text-based
// edit when setTOMLBoolKey is added.
func UnpinPackage(path, host, name string) error {
	return withFileLock(path, func() error {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading gale config: %w", err)
		}
		cfg, err := ParseGaleConfig(string(data))
		if err != nil {
			return err
		}

		if host == "" {
			delete(cfg.Pinned, name)
		} else if h, ok := cfg.Hosts[host]; ok {
			delete(h.Pinned, name)
			cfg.Hosts[host] = h
		}

		return WriteGaleConfig(path, cfg)
	})
}
