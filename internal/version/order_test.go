package version

import "testing"

// KeyNewer is a deterministic total order over version keys,
// shared by the registry (.versions index) and the recipe
// ([[history]] ledger) so both agree on which version is latest.
// It must reproduce the gh#58 fix: non-semver keys order by
// natural (digit-run-aware) comparison, never by IsNewer's
// optimistic "always true".
func TestKeyNewer(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		// Semver bases.
		{"1.8.1", "1.7.1", true},
		{"1.7.1", "1.8.1", false},
		// Revision tiebreak on equal bases.
		{"1.8.1-2", "1.8.1", true},
		{"1.8.1", "1.8.1-2", false},
		{"1.8.1-5", "1.8.1-2", true},
		// Non-semver natural ordering (gh#58).
		{"1.8.1.10", "1.8.1.2", true},
		{"1.4h", "1.4g", true},
		{"15.2.rel2", "15.2.rel1", true},
		// Equal keys are not strictly newer.
		{"1.8.1", "1.8.1", false},
	}
	for _, tt := range tests {
		if got := KeyNewer(tt.a, tt.b); got != tt.want {
			t.Errorf("KeyNewer(%q, %q) = %v, want %v",
				tt.a, tt.b, got, tt.want)
		}
	}
}

func TestLatest(t *testing.T) {
	tests := []struct {
		name string
		keys []string
		want string
	}{
		{"semver", []string{"1.7.1", "1.8.1", "1.7.0"}, "1.8.1"},
		{"revision bump", []string{"1.8.1.1", "1.8.1.1-2"}, "1.8.1.1-2"},
		{"natural", []string{"1.4g", "1.4h"}, "1.4h"},
		{"single", []string{"2.0.0"}, "2.0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Run repeatedly: input order must not matter.
			for i := 0; i < 50; i++ {
				got, ok := Latest(tt.keys)
				if !ok {
					t.Fatalf("Latest ok = false")
				}
				if got != tt.want {
					t.Fatalf("Latest = %q, want %q", got, tt.want)
				}
			}
		})
	}
}

func TestLatestEmpty(t *testing.T) {
	if got, ok := Latest(nil); ok || got != "" {
		t.Errorf("Latest(nil) = (%q, %v), want (\"\", false)", got, ok)
	}
}

func TestPick(t *testing.T) {
	keys := []string{"8.19.0-1", "8.19.0-2", "8.20.0-1"}
	tests := []struct {
		requested string
		want      string
		wantOK    bool
	}{
		// Exact match.
		{"8.19.0-2", "8.19.0-2", true},
		// Bare base resolves to highest revision.
		{"8.19.0", "8.19.0-2", true},
		// -1 implicit fallback to a bare entry.
		{"8.20.0-1", "8.20.0-1", true},
		// Unknown version.
		{"9.0.0", "", false},
		// Explicit non-1 revision with no exact match: no fallback.
		{"8.19.0-9", "", false},
	}
	for _, tt := range tests {
		got, ok := Pick(keys, tt.requested)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("Pick(%q) = (%q, %v), want (%q, %v)",
				tt.requested, got, ok, tt.want, tt.wantOK)
		}
	}
}

// Pick honors the legacy bare-version entry: a "-1" request finds
// a bare entry recorded before revisions existed.
func TestPickBareEntryViaDashOne(t *testing.T) {
	keys := []string{"1.2.3"}
	got, ok := Pick(keys, "1.2.3-1")
	if !ok || got != "1.2.3" {
		t.Errorf("Pick = (%q, %v), want (\"1.2.3\", true)", got, ok)
	}
}
