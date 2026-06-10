package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Issue #108: `--host <name>` accepts any string, so a typo'd
// hostname silently creates a brand-new [hosts.<typo>.packages]
// section. HostSectionExists is the shared check commands use to
// detect whether a --host value already has a matching section,
// so they can print a visible notice before creating a new one.

func writeU16Config(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gale.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestHostSectionExistsExactKey: a host that names an existing
// exact [hosts.<key>] section exists.
func TestHostSectionExistsExactKey(t *testing.T) {
	path := writeU16Config(t, "[packages]\n"+
		"  jq = \"1.8.1\"\n\n"+
		"[hosts.\"travis-mb.local\".packages]\n"+
		"  rg = \"14.1.0\"\n")

	if !HostSectionExists(path, "travis-mb.local") {
		t.Error("exact key 'travis-mb.local' should exist")
	}
}

// TestHostSectionExistsTypo: a typo'd host matches nothing and
// reports false — this is the case that must trigger the notice.
func TestHostSectionExistsTypo(t *testing.T) {
	path := writeU16Config(t, "[packages]\n"+
		"  jq = \"1.8.1\"\n\n"+
		"[hosts.\"travis-mb.local\".packages]\n"+
		"  rg = \"14.1.0\"\n")

	if HostSectionExists(path, "travis-macbok.local") {
		t.Error("typo'd host 'travis-macbok.local' should not exist")
	}
}

// TestHostSectionExistsGlobKey: a literal host covered by an
// existing glob section key exists (HostKeyMatches direction:
// key pattern → host).
func TestHostSectionExistsGlobKey(t *testing.T) {
	path := writeU16Config(t,
		"[hosts.\"travis-mb*\".packages]\n"+
			"  rg = \"14.1.0\"\n")

	if !HostSectionExists(path, "travis-mb.local") {
		t.Error("host covered by glob key 'travis-mb*' should exist")
	}
}

// TestHostSectionExistsGlobFlag: a glob --host value that exactly
// names an existing glob key exists. `--host mac-*` targeting an
// existing [hosts."mac-*"] section must not look "new".
func TestHostSectionExistsGlobFlag(t *testing.T) {
	path := writeU16Config(t,
		"[hosts.\"mac-*\".packages]\n"+
			"  rg = \"14.1.0\"\n")

	if !HostSectionExists(path, "mac-*") {
		t.Error("glob host 'mac-*' targeting identical glob key should exist")
	}
}

// TestHostSectionExistsGlobFlagCoveringExactKey: a glob --host
// value covering an existing exact key exists (HostKeyMatches
// direction: host pattern → key). Suppression is cheap; a false
// "new section" notice on an intentional glob is noise.
func TestHostSectionExistsGlobFlagCoveringExactKey(t *testing.T) {
	path := writeU16Config(t,
		"[hosts.\"mac-mini\".packages]\n"+
			"  rg = \"14.1.0\"\n")

	if !HostSectionExists(path, "mac-*") {
		t.Error("glob host 'mac-*' covering exact key 'mac-mini' should exist")
	}
}

// TestHostSectionExistsCommaListKey: a comma-list --host value
// that exactly names an existing comma-list key exists.
// HostKeyMatches splits keys on commas before comparing, so it
// never does whole-string equality on comma-list keys; without a
// literal comparison, `--host "mac-1,mac-2"` targeting an
// identical [hosts."mac-1,mac-2"] section warns "new section" on
// every run even though AddPackage reuses the literal key.
func TestHostSectionExistsCommaListKey(t *testing.T) {
	path := writeU16Config(t,
		"[hosts.\"mac-1,mac-2\".packages]\n"+
			"  rg = \"14.1.0\"\n")

	if !HostSectionExists(path, "mac-1,mac-2") {
		t.Error("comma-list host 'mac-1,mac-2' targeting identical comma-list key should exist")
	}
}

// TestHostSectionExistsLegacyDottedHeader: a host present only as
// a pre-#59 unquoted dotted header parses as nested tables and
// never reaches the typed Hosts map, but the section DOES exist
// on disk (the mutators normalize and reuse it), so no "new
// section" notice should fire.
func TestHostSectionExistsLegacyDottedHeader(t *testing.T) {
	path := writeU16Config(t,
		"[hosts.travis-mb.local.packages]\n"+
			"  rg = \"14.1.0\"\n")

	if !HostSectionExists(path, "travis-mb.local") {
		t.Error("legacy unquoted dotted header should count as existing")
	}
}

// TestHostSectionExistsPinnedOnlySection: a host declared only via
// [hosts.<key>.pinned] still exists.
func TestHostSectionExistsPinnedOnlySection(t *testing.T) {
	path := writeU16Config(t,
		"[hosts.\"travis-mb.local\".pinned]\n"+
			"  rg = true\n")

	if !HostSectionExists(path, "travis-mb.local") {
		t.Error("pinned-only host section should exist")
	}
}

// TestHostSectionExistsMissingFile: no config file means no
// sections — a foreign host on a fresh config is genuinely new.
func TestHostSectionExistsMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gale.toml")
	if HostSectionExists(path, "travis-mb.local") {
		t.Error("missing file should report no host sections")
	}
}
