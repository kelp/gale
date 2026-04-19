package version

import "testing"

func TestIsNewer(t *testing.T) {
	tests := []struct {
		current, candidate string
		want               bool
	}{
		// Classic semver upgrades and downgrades.
		{"0.2.0", "0.8.1", true},
		{"1.0.0", "1.0.1", true},
		{"1.0.0", "2.0.0", true},
		{"0.8.1", "0.2.0", false},
		{"2.0.0", "1.0.0", false},
		{"0.8.1", "0.8.1", false},

		// Semver pre-release tags (letters after `-`): delegate
		// to golang.org/x/mod/semver so dev → stable is an
		// upgrade and stable → dev is a downgrade.
		{"0.8.1-dev.2+47a65de", "0.8.1", true},
		{"0.8.1-dev.2", "0.8.1", true},
		{"0.8.1", "0.8.1-dev.2", false},
		{"0.8.1", "0.9.0-dev.1", true},
		{"0.8.2-dev.1", "0.8.1", false},

		// Non-semver (git hashes, ad-hoc strings): optimistic.
		{"abc1234", "0.8.1", true},
		{"0.8.1", "abc1234", true},
		{"abc1234", "def5678", true},

		// Gale revision suffixes: numeric `-<N>` is newer than
		// bare, not older. Unlike semver pre-release ordering.
		{"1.2.3", "1.2.3-2", true},
		{"1.2.3-2", "1.2.3", false},
		{"1.2.3-2", "1.2.3-3", true},
		{"1.2.3-3", "1.2.3-2", false},
		{"1.2.3-2", "1.2.3-2", false},
		{"1.2.3-2", "1.2.4", true},
		{"1.2.3", "1.2.4-2", true},

		// Revision on a dev tag: dev stays attached to the
		// semver base; the whole thing is treated as dev, not
		// dev+revision. IsNewer falls through to semver.
		{"0.8.1-dev.2", "0.8.1-dev.3", true},
	}
	for _, tt := range tests {
		t.Run(tt.current+"→"+tt.candidate, func(t *testing.T) {
			got := IsNewer(tt.candidate, tt.current)
			if got != tt.want {
				t.Errorf(
					"IsNewer(%q, %q) = %v, want %v",
					tt.candidate, tt.current, got, tt.want)
			}
		})
	}
}

func TestSplitRevision(t *testing.T) {
	tests := []struct {
		v        string
		wantBase string
		wantRev  int
	}{
		{"1.2.3", "1.2.3", 1},
		{"1.2.3-2", "1.2.3", 2},
		{"1.2.3-10", "1.2.3", 10},
		{"1.2.3-", "1.2.3-", 1}, // dangling dash: not a revision
		{"1.2.3-dev", "1.2.3-dev", 1},
		{"1.2.3-dev.2", "1.2.3-dev.2", 1},
		{"1.2.3-0", "1.2.3-0", 1}, // revision must be >= 1
		{"", "", 1},
	}
	for _, tt := range tests {
		t.Run(tt.v, func(t *testing.T) {
			base, rev := splitRevision(tt.v)
			if base != tt.wantBase || rev != tt.wantRev {
				t.Errorf(
					"splitRevision(%q) = (%q, %d), want (%q, %d)",
					tt.v, base, rev, tt.wantBase, tt.wantRev)
			}
		})
	}
}
