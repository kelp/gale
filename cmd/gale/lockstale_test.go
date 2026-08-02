package main

// Tests for lockIsStale, the schema-tolerant replacement for
// lockfile.IsStale that syncIfNeeded calls on every direnv `cd`.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/lockfile"
)

// v1StaleFixture is a v1 lock whose closure is larger than its root
// set: jq is declared, oniguruma is reached through it. The legacy
// count comparison reports this stale forever, because gale.toml
// names one package and [packages] holds two.
const v1StaleFixture = `version = 1

[packages."!gale-lock-v1"]
version = 1

[targets.default]
roots = ["jq@1.8.1-2"]

[targets.host."work-mbp"]
roots = ["jq@1.8.1-2", "ripgrep@14.1.1-1"]

[packages."jq@1.8.1-2".artifacts."darwin/arm64"]
sha256 = "aaaa"
method = "binary"
runtime_deps = ["oniguruma@6.9.10-1"]
graph_digest = "sha256:cccc"

[packages."oniguruma@6.9.10-1".artifacts."darwin/arm64"]
sha256 = "bbbb"
method = "source"
graph_digest = "sha256:dddd"

[packages."ripgrep@14.1.1-1".artifacts."darwin/arm64"]
sha256 = "eeee"
method = "binary"
graph_digest = "sha256:ffff"
`

// TestLockIsStaleV1RootsVsToml is the phase's central case: a v1
// lock is compared root-set against manifest, never by counting
// package entries. Transitive nodes are the normal state of an
// enforced lock, so a count comparison would report stale on every
// `cd` and sync forever.
func TestLockIsStaleV1RootsVsToml(t *testing.T) {
	tests := []struct {
		name      string
		host      string
		declared  map[string]string
		wantStale bool
	}{
		{
			name:      "roots match, closure larger",
			declared:  map[string]string{"jq": "1.8.1"},
			wantStale: false,
		},
		{
			name: "edited manifest pin",
			// gale.toml pins an exact version, so a changed pin means
			// the lock no longer describes what was asked for. The bare
			// version against the lock's canonical 1.8.1-2 is the one
			// divergence that is not staleness, covered above.
			declared:  map[string]string{"jq": "1.8"},
			wantStale: true,
		},
		{
			name:      "declared package with no locked root",
			declared:  map[string]string{"jq": "1.8.1", "fd": "10.2.0"},
			wantStale: true,
		},
		{
			name:      "locked root no longer declared",
			declared:  map[string]string{},
			wantStale: true,
		},
		{
			name:      "host overlay satisfies the host's manifest",
			host:      "work-mbp",
			declared:  map[string]string{"jq": "1.8.1", "ripgrep": "14.1.1"},
			wantStale: false,
		},
		{
			name: "host overlay ignored for another host",
			host: "laptop",
			// The overlay's ripgrep root does not apply here, so the
			// default target alone must satisfy the manifest.
			declared:  map[string]string{"jq": "1.8.1"},
			wantStale: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "gale.lock")
			if err := os.WriteFile(path, []byte(v1StaleFixture), 0o600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}

			stale, err := lockIsStale(path, tt.declared, tt.host)
			if err != nil {
				t.Fatalf("lockIsStale: %v", err)
			}
			if stale != tt.wantStale {
				t.Errorf("stale = %v, want %v", stale, tt.wantStale)
			}
		})
	}
}

// TestLockIsStaleV1MalformedRootIsAnError separates the two failure
// modes a v1 lock has. A root set that disagrees with the manifest is
// staleness, which sync resolves. A root that is not an identity is a
// lock that cannot be modeled, and reporting it as merely stale would
// send sync to fix it by writing, hiding a lock-unusable condition
// behind a routine one.
func TestLockIsStaleV1MalformedRootIsAnError(t *testing.T) {
	const malformed = `version = 1

[packages."!gale-lock-v1"]
version = 1

[targets.default]
roots = ["jq"]
`
	path := filepath.Join(t.TempDir(), "gale.lock")
	if err := os.WriteFile(path, []byte(malformed), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	stale, err := lockIsStale(path, map[string]string{"jq": "1.8.1"}, "")
	if !errors.Is(err, lockfile.ErrMalformedRoot) {
		t.Fatalf("error = %v, want ErrMalformedRoot", err)
	}
	if stale {
		t.Error("stale = true alongside an error, want false")
	}
}

// TestLockIsStaleLegacyAndAbsent pins that the paths every installed
// gale is on today are untouched: the flat schema keeps its existing
// comparison, and no lock at all still means stale, which is what
// makes the first sync in a fresh project run.
func TestLockIsStaleLegacyAndAbsent(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "gale.lock")
	if err := os.WriteFile(legacy, []byte(`[packages.jq]
version = "1.8.1-2"
sha256 = "aaaa"
`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	stale, err := lockIsStale(legacy, map[string]string{"jq": "1.8.1"}, "")
	if err != nil {
		t.Fatalf("lockIsStale legacy: %v", err)
	}
	if stale {
		t.Error("legacy lock matching gale.toml reported stale")
	}

	stale, err = lockIsStale(legacy, map[string]string{"jq": "1.9.0"}, "")
	if err != nil {
		t.Fatalf("lockIsStale legacy changed: %v", err)
	}
	if !stale {
		t.Error("legacy lock with a changed version reported fresh")
	}

	stale, err = lockIsStale(filepath.Join(dir, "absent.lock"), nil, "")
	if err != nil {
		t.Fatalf("lockIsStale absent: %v", err)
	}
	if !stale {
		t.Error("absent lock reported fresh")
	}
}
