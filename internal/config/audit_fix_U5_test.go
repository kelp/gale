package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTempGaleToml writes content to gale.toml in a temp dir and
// returns the path.
func writeTempGaleToml(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gale.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// readFileStr reads path and returns the contents as a string.
func readFileStr(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Issue #60: a trailing comment on the section header must not
// cause AddPackage to append a duplicate [packages] table.
func TestAddPackageSectionHeaderWithComment(t *testing.T) {
	path := writeTempGaleToml(t,
		"[packages] # my CLI tools\njq = \"1.7.1\"\n")

	if err := AddPackage(path, "", "ripgrep", "15.1.0"); err != nil {
		t.Fatalf("AddPackage: %v", err)
	}

	out := readFileStr(t, path)
	if strings.Count(out, "[packages]") != 1 {
		t.Fatalf("duplicate [packages] section written:\n%s", out)
	}
	cfg, err := ParseGaleConfig(out)
	if err != nil {
		t.Fatalf("result does not parse: %v\n%s", err, out)
	}
	if cfg.Packages["jq"] != "1.7.1" {
		t.Errorf("jq lost: %v", cfg.Packages)
	}
	if cfg.Packages["ripgrep"] != "15.1.0" {
		t.Errorf("ripgrep missing: %v", cfg.Packages)
	}
}

// Issue #60: whitespace inside the section brackets must still
// match the section.
func TestAddPackageSectionHeaderWithWhitespace(t *testing.T) {
	path := writeTempGaleToml(t,
		"[ packages ]\njq = \"1.7.1\"\n")

	if err := AddPackage(path, "", "ripgrep", "15.1.0"); err != nil {
		t.Fatalf("AddPackage: %v", err)
	}

	out := readFileStr(t, path)
	cfg, err := ParseGaleConfig(out)
	if err != nil {
		t.Fatalf("result does not parse: %v\n%s", err, out)
	}
	if cfg.Packages["ripgrep"] != "15.1.0" {
		t.Errorf("ripgrep missing: %v", cfg.Packages)
	}
	if cfg.Packages["jq"] != "1.7.1" {
		t.Errorf("jq lost: %v", cfg.Packages)
	}
}

// Issue #60: RemovePackage must find a package whose section
// header carries a trailing comment.
func TestRemovePackageSectionHeaderWithComment(t *testing.T) {
	path := writeTempGaleToml(t,
		"[packages] # my CLI tools\njq = \"1.7.1\"\nripgrep = \"15.1.0\"\n")

	if err := RemovePackage(path, "", "jq"); err != nil {
		t.Fatalf("RemovePackage: %v", err)
	}

	cfg, err := ParseGaleConfig(readFileStr(t, path))
	if err != nil {
		t.Fatalf("result does not parse: %v", err)
	}
	if _, ok := cfg.Packages["jq"]; ok {
		t.Error("jq still present after remove")
	}
	if cfg.Packages["ripgrep"] != "15.1.0" {
		t.Errorf("ripgrep lost: %v", cfg.Packages)
	}
}

// Issue #59: a dotted hostname must round-trip through AddPackage
// and ParseGaleConfig — the host key must be quoted in the section
// header, not split into nested tables.
func TestAddPackageDottedHostRoundTrip(t *testing.T) {
	const host = "travis-macbook.local"
	path := writeTempGaleToml(t, "[packages]\njq = \"1.7.1\"\n")

	if err := AddPackage(path, host, "ripgrep", "14.1.0"); err != nil {
		t.Fatalf("AddPackage: %v", err)
	}

	out := readFileStr(t, path)
	cfg, err := ParseGaleConfig(out)
	if err != nil {
		t.Fatalf("result does not parse: %v\n%s", err, out)
	}
	h, ok := cfg.Hosts[host]
	if !ok {
		t.Fatalf("Hosts[%q] missing after AddPackage; hosts=%v\n%s",
			host, cfg.Hosts, out)
	}
	if h.Packages["ripgrep"] != "14.1.0" {
		t.Errorf("ripgrep missing from host section: %v", h.Packages)
	}
	eff := cfg.EffectivePackages(host)
	if eff["ripgrep"] != "14.1.0" {
		t.Errorf("EffectivePackages(%q) missing ripgrep: %v", host, eff)
	}
}

// Issue #59: adding twice for the same dotted host must update the
// existing section in place, not create a second one.
func TestAddPackageDottedHostIdempotentSection(t *testing.T) {
	const host = "travis-macbook.local"
	path := writeTempGaleToml(t, "")

	if err := AddPackage(path, host, "ripgrep", "14.1.0"); err != nil {
		t.Fatalf("AddPackage: %v", err)
	}
	if err := AddPackage(path, host, "jq", "1.7.1"); err != nil {
		t.Fatalf("AddPackage: %v", err)
	}
	if err := AddPackage(path, host, "ripgrep", "15.0.0"); err != nil {
		t.Fatalf("AddPackage: %v", err)
	}

	out := readFileStr(t, path)
	if n := strings.Count(out, "[hosts."); n != 1 {
		t.Fatalf("expected 1 host section, found %d:\n%s", n, out)
	}
	cfg, err := ParseGaleConfig(out)
	if err != nil {
		t.Fatalf("result does not parse: %v\n%s", err, out)
	}
	h := cfg.Hosts[host]
	if h.Packages["ripgrep"] != "15.0.0" {
		t.Errorf("ripgrep not updated: %v", h.Packages)
	}
	if h.Packages["jq"] != "1.7.1" {
		t.Errorf("jq missing: %v", h.Packages)
	}
}

// Issue #59: UpsertPackage with a dotted current host must update
// the package inside the (quoted) host section, not move it to the
// shared [packages] table.
func TestUpsertPackageDottedHostPreservesLocation(t *testing.T) {
	const host = "travis-macbook.local"
	path := writeTempGaleToml(t,
		"[packages]\njq = \"1.7.1\"\n\n"+
			"[hosts.\"travis-macbook.local\".packages]\nripgrep = \"14.1.0\"\n")

	if err := UpsertPackage(path, host, "ripgrep", "15.0.0"); err != nil {
		t.Fatalf("UpsertPackage: %v", err)
	}

	out := readFileStr(t, path)
	cfg, err := ParseGaleConfig(out)
	if err != nil {
		t.Fatalf("result does not parse: %v\n%s", err, out)
	}
	if cfg.Hosts[host].Packages["ripgrep"] != "15.0.0" {
		t.Errorf("host section not updated: %v\n%s",
			cfg.Hosts[host].Packages, out)
	}
	if _, ok := cfg.Packages["ripgrep"]; ok {
		t.Errorf("ripgrep leaked into shared [packages]:\n%s", out)
	}
}

// Issue #59: RemovePackage with a dotted host must find the quoted
// host section.
func TestRemovePackageDottedHost(t *testing.T) {
	const host = "travis-macbook.local"
	path := writeTempGaleToml(t,
		"[hosts.\"travis-macbook.local\".packages]\nripgrep = \"14.1.0\"\n")

	if err := RemovePackage(path, host, "ripgrep"); err != nil {
		t.Fatalf("RemovePackage: %v", err)
	}

	cfg, err := ParseGaleConfig(readFileStr(t, path))
	if err != nil {
		t.Fatalf("result does not parse: %v", err)
	}
	if _, ok := cfg.Hosts[host].Packages["ripgrep"]; ok {
		t.Error("ripgrep still present after remove")
	}
}

// Configs written before host keys were quoted contain legacy
// unquoted dotted headers like [hosts.travis-macbook.local.packages]
// (four bare segments). AddPackage must find that block, normalize
// the header to the quoted form, and not append a duplicate section.
func TestAddPackageLegacyUnquotedHostHeader(t *testing.T) {
	const host = "travis-macbook.local"
	path := writeTempGaleToml(t,
		"[hosts.travis-macbook.local.packages]\nripgrep = \"14.1.0\"\n")

	if err := AddPackage(path, host, "jq", "1.7.1"); err != nil {
		t.Fatalf("AddPackage: %v", err)
	}

	out := readFileStr(t, path)
	if n := strings.Count(out, "[hosts."); n != 1 {
		t.Fatalf("expected 1 host section, found %d:\n%s", n, out)
	}
	cfg, err := ParseGaleConfig(out)
	if err != nil {
		t.Fatalf("result does not parse: %v\n%s", err, out)
	}
	h := cfg.Hosts[host]
	if h.Packages["jq"] != "1.7.1" {
		t.Errorf("jq missing from host section: %v\n%s", h.Packages, out)
	}
	if h.Packages["ripgrep"] != "14.1.0" {
		t.Errorf("ripgrep lost: %v\n%s", h.Packages, out)
	}
}

// UpsertPackage must update a package inside a legacy unquoted
// dotted host section instead of moving it to shared [packages].
func TestUpsertPackageLegacyUnquotedHostHeader(t *testing.T) {
	const host = "travis-macbook.local"
	path := writeTempGaleToml(t,
		"[packages]\njq = \"1.7.1\"\n\n"+
			"[hosts.travis-macbook.local.packages]\nripgrep = \"14.1.0\"\n")

	if err := UpsertPackage(path, host, "ripgrep", "15.0.0"); err != nil {
		t.Fatalf("UpsertPackage: %v", err)
	}

	out := readFileStr(t, path)
	if n := strings.Count(out, "[hosts."); n != 1 {
		t.Fatalf("expected 1 host section, found %d:\n%s", n, out)
	}
	cfg, err := ParseGaleConfig(out)
	if err != nil {
		t.Fatalf("result does not parse: %v\n%s", err, out)
	}
	if cfg.Hosts[host].Packages["ripgrep"] != "15.0.0" {
		t.Errorf("host section not updated: %v\n%s",
			cfg.Hosts[host].Packages, out)
	}
	if _, ok := cfg.Packages["ripgrep"]; ok {
		t.Errorf("ripgrep leaked into shared [packages]:\n%s", out)
	}
}

// RemovePackage must find a package inside a legacy unquoted dotted
// host section instead of reporting package-not-found.
func TestRemovePackageLegacyUnquotedHostHeader(t *testing.T) {
	const host = "travis-macbook.local"
	path := writeTempGaleToml(t,
		"[hosts.travis-macbook.local.packages]\n"+
			"ripgrep = \"14.1.0\"\njq = \"1.7.1\"\n")

	if err := RemovePackage(path, host, "ripgrep"); err != nil {
		t.Fatalf("RemovePackage: %v", err)
	}

	out := readFileStr(t, path)
	cfg, err := ParseGaleConfig(out)
	if err != nil {
		t.Fatalf("result does not parse: %v\n%s", err, out)
	}
	if _, ok := cfg.Hosts[host].Packages["ripgrep"]; ok {
		t.Errorf("ripgrep still present after remove:\n%s", out)
	}
	if cfg.Hosts[host].Packages["jq"] != "1.7.1" {
		t.Errorf("jq lost: %v\n%s", cfg.Hosts[host].Packages, out)
	}
}

// Glob/comma host keys contain characters that are not valid TOML
// bare keys; they must be quoted and still round-trip.
func TestAddPackageGlobHostKeyRoundTrip(t *testing.T) {
	const key = "*.local"
	path := writeTempGaleToml(t, "")

	if err := AddPackage(path, key, "jq", "1.7.1"); err != nil {
		t.Fatalf("AddPackage: %v", err)
	}

	out := readFileStr(t, path)
	cfg, err := ParseGaleConfig(out)
	if err != nil {
		t.Fatalf("result does not parse: %v\n%s", err, out)
	}
	if cfg.Hosts[key].Packages["jq"] != "1.7.1" {
		t.Errorf("Hosts[%q] missing jq: %v\n%s", key, cfg.Hosts, out)
	}
	eff := cfg.EffectivePackages("travis-macbook.local")
	if eff["jq"] != "1.7.1" {
		t.Errorf("glob section not effective: %v", eff)
	}
}
