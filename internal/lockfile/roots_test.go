package lockfile

import (
	"errors"
	"strings"
	"testing"
)

// rootsOf builds a lock carrying only targets. Every test here is
// about the root set, and none of them reads a package node.
func rootsOf(def []string, host map[string][]string) *V1 {
	lf := &V1{Version: SchemaVersion, Packages: map[string]Package{}}
	if def != nil {
		lf.Targets.Default = &Target{Roots: def}
	}
	if host != nil {
		lf.Targets.Host = make(map[string]Target, len(host))
		for k, roots := range host {
			lf.Targets.Host[k] = Target{Roots: roots}
		}
	}
	return lf
}

// TestEffectiveRootsPrecedence pins that the lock reuses gale.toml's
// selector precedence rather than defining its own, and that a more
// specific selector replaces a pin instead of adding a second one.
func TestEffectiveRootsPrecedence(t *testing.T) {
	lf := rootsOf(
		[]string{"a@1.0-1", "b@1.0-1"},
		map[string][]string{
			"work-*":  {"a@2.0-1"},
			"work-mb": {"a@3.0-1"},
			"other":   {"b@9.0-1"},
		},
	)

	tests := []struct {
		host string
		want map[string]string
	}{
		{
			// No host: overlays are not consulted at all, which is what
			// lets a lock be read for a platform or machine other than
			// the one in hand.
			host: "",
			want: map[string]string{"a": "a@1.0-1", "b": "b@1.0-1"},
		},
		{
			host: "work-laptop",
			want: map[string]string{"a": "a@2.0-1", "b": "b@1.0-1"},
		},
		{
			// Both selectors match; the exact host is more specific.
			host: "work-mb",
			want: map[string]string{"a": "a@3.0-1", "b": "b@1.0-1"},
		},
		{
			host: "other",
			want: map[string]string{"a": "a@1.0-1", "b": "b@9.0-1"},
		},
	}

	for _, tt := range tests {
		t.Run("host="+tt.host, func(t *testing.T) {
			got, err := lf.EffectiveRoots(tt.host)
			if err != nil {
				t.Fatalf("EffectiveRoots: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("roots = %v, want %v", got, tt.want)
			}
			for name, id := range tt.want {
				if got[name] != id {
					t.Errorf("roots[%q] = %q, want %q", name, got[name], id)
				}
			}
		})
	}
}

// TestEffectiveRootsRejects covers the two malformed shapes. A
// duplicate inside one roots list is separated from replacement across
// selectors: replacement is the overlay's purpose, while list order
// deciding a winner is a lock that cannot be modeled.
func TestEffectiveRootsRejects(t *testing.T) {
	tests := []struct {
		name    string
		lf      *V1
		host    string
		wantErr error
		names   []string
	}{
		{
			name:    "two identities in one roots list",
			lf:      rootsOf([]string{"a@1.0-1", "a@2.0-1"}, nil),
			wantErr: ErrVersionConflict,
			names:   []string{"a@1.0-1", "a@2.0-1"},
		},
		{
			name:    "duplicate inside a host overlay",
			lf:      rootsOf(nil, map[string][]string{"mb": {"a@1.0-1", "a@2.0-1"}}),
			host:    "mb",
			wantErr: ErrVersionConflict,
		},
		{
			name:    "bare name is not an identity",
			lf:      rootsOf([]string{"a"}, nil),
			wantErr: ErrMalformedRoot,
		},
		{
			name:    "no revision suffix",
			lf:      rootsOf([]string{"a@1.0"}, nil),
			wantErr: ErrMalformedRoot,
		},
		{
			// The name is joined onto a --recipes directory downstream,
			// so a name that is itself a path must never get that far.
			name:    "name is a path",
			lf:      rootsOf([]string{"../../outside@1.0-1"}, nil),
			wantErr: ErrMalformedRoot,
		},
		{
			name:    "malformed identity in an overlay",
			lf:      rootsOf(nil, map[string][]string{"*": {"a@1.0"}}),
			host:    "anything",
			wantErr: ErrMalformedRoot,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.lf.EffectiveRoots(tt.host)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			for _, want := range tt.names {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error does not name %s: %v", want, err)
				}
			}
		})
	}
}

// TestEffectiveRootsRepeatedIdentityIsNotAConflict pins that the same
// identity listed twice is harmless. Only two *different* identities
// for one name make list order decide anything.
func TestEffectiveRootsRepeatedIdentityIsNotAConflict(t *testing.T) {
	got, err := rootsOf([]string{"a@1.0-1", "a@1.0-1"}, nil).EffectiveRoots("")
	if err != nil {
		t.Fatalf("EffectiveRoots: %v", err)
	}
	if got["a"] != "a@1.0-1" {
		t.Errorf("roots[a] = %q, want a@1.0-1", got["a"])
	}
}

// TestCheckDeclaredComparesPins is the case the name-only comparison
// missed: gale.toml pins an exact version, so an edited pin means the
// lock no longer describes what was asked for. Legacy IsStale caught
// this through VersionMatches, and losing it would make a pin edit
// invisible to the direnv staleness check.
func TestCheckDeclaredComparesPins(t *testing.T) {
	tests := []struct {
		name      string
		roots     map[string]string
		declared  map[string]string
		wantStale bool
		names     []string
	}{
		{
			name:      "exact agreement",
			roots:     map[string]string{"jq": "jq@1.8.1-1"},
			declared:  map[string]string{"jq": "1.8.1-1"},
			wantStale: false,
		},
		{
			// The normal state: gale.toml carries the bare version so
			// the entry tracks revision bumps, the lock carries the
			// canonical form it resolved to.
			name:      "bare manifest pin against a canonical root",
			roots:     map[string]string{"jq": "jq@1.8.1-2"},
			declared:  map[string]string{"jq": "1.8.1"},
			wantStale: false,
		},
		{
			name:      "edited pin",
			roots:     map[string]string{"jq": "jq@1.8.1-2"},
			declared:  map[string]string{"jq": "1.9.0"},
			wantStale: true,
			names:     []string{"jq", "1.9.0", "1.8.1-2"},
		},
		{
			// A revision suffix of 0 is not canonical, so it is not the
			// bare-vs-canonical case and must not be reconciled away.
			name:      "revision zero is not a match",
			roots:     map[string]string{"jq": "jq@1.8.1-0"},
			declared:  map[string]string{"jq": "1.8.1"},
			wantStale: true,
		},
		{
			name:      "declared with no root",
			roots:     map[string]string{},
			declared:  map[string]string{"jq": "1.8.1"},
			wantStale: true,
			names:     []string{"no locked root"},
		},
		{
			name:      "root no longer declared",
			roots:     map[string]string{"jq": "jq@1.8.1-1"},
			declared:  map[string]string{},
			wantStale: true,
			names:     []string{"no longer declares"},
		},
		{
			name:      "empty on both sides",
			roots:     map[string]string{},
			declared:  map[string]string{},
			wantStale: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckDeclared(tt.roots, tt.declared)
			if tt.wantStale {
				if !errors.Is(err, ErrStaleLock) {
					t.Fatalf("err = %v, want ErrStaleLock", err)
				}
				for _, want := range tt.names {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error does not mention %q: %v", want, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("CheckDeclared: %v", err)
			}
		})
	}
}

// TestCheckDeclaredNamesEveryDisagreement pins that one message
// carries all three directions at once. A message naming one leaves
// the user fixing that and rerunning to discover the next.
func TestCheckDeclaredNamesEveryDisagreement(t *testing.T) {
	err := CheckDeclared(
		map[string]string{"jq": "jq@1.8.1-1", "orphan": "orphan@1.0-1"},
		map[string]string{"jq": "1.9.0", "missing": "2.0"},
	)
	if !errors.Is(err, ErrStaleLock) {
		t.Fatalf("err = %v, want ErrStaleLock", err)
	}
	for _, want := range []string{"missing", "orphan", "jq"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}
