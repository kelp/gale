package config

import (
	"errors"
	"testing"
)

// TestHostWitness: a lock writer has to know whether two host selectors
// can apply to one machine, because two selectors that can co-apply must
// agree about versions while two that cannot are legitimately
// independent. Answering that needs a concrete hostname matching both,
// which is also the value a caller wants: it can then resolve the real
// precedence rule against it instead of reimplementing the merge.
// hostWitnessCase is one selector set and what HostWitness owes for it.
type hostWitnessCase struct {
	name     string
	patterns []string
	want     bool
	wantErr  bool
}

// hostWitnessCases is a function rather than an inline literal so the
// test body stays short enough to read as assertions.
func hostWitnessCases() []hostWitnessCase {
	return []hostWitnessCase{
		{name: "one literal", patterns: []string{"work-mbp"}, want: true},
		{name: "one glob", patterns: []string{"work-*"}, want: true},
		{
			// The case that matters: neither selector is a prefix or
			// suffix of the other, yet work-mbp applies both.
			name:     "overlapping globs",
			patterns: []string{"work-*", "*-mbp"},
			want:     true,
		},
		{
			name:     "glob covering a literal",
			patterns: []string{"work-*", "work-mbp"},
			want:     true,
		},
		{
			name:     "distinct literals never co-apply",
			patterns: []string{"work-mbp", "home-imac"},
			want:     false,
		},
		{
			name:     "disjoint globs",
			patterns: []string{"work-*", "home-*"},
			want:     false,
		},
		{
			// A comma list is a union, so overlap needs only one member
			// of each to agree.
			name:     "comma list member overlaps",
			patterns: []string{"work-mbp, home-imac", "*-imac"},
			want:     true,
		},
		{
			// Beyond the documented grammar. Deciding intersection over
			// `?` and character classes completely needs a rune-level
			// model of the whole matcher language, so it is refused
			// rather than answered unsoundly.
			name:     "single-character wildcard is unsupported",
			patterns: []string{"host?", "hostx"},
			wantErr:  true,
		},
		{
			name:     "character class is unsupported",
			patterns: []string{"host[0-9]", "host7"},
			wantErr:  true,
		},
		{
			// The case that proved a byte alphabet built from pattern
			// text is incomplete: hostc satisfies both while appearing in
			// neither. Refused rather than silently missed.
			name:     "negated class is unsupported",
			patterns: []string{"host[b-d]", "host[^bd]"},
			wantErr:  true,
		},
		{
			// The byte automaton and filepath.Match disagree about
			// multi-byte characters, so non-ASCII is refused too.
			name:     "non-ASCII is unsupported",
			patterns: []string{"h\u00f4te-*", "*-mbp"},
			wantErr:  true,
		},
		{
			// Every printable ASCII character appears in the patterns, so
			// a fixed shortlist of filler characters would leave the
			// search with no representative for "any other character".
			name:     "witness needs a character the patterns exhaust",
			patterns: []string{"abcdefghijklmnopqrstuvwxyz*", "*z"},
			want:     true,
		},
	}
}

func TestHostWitness(t *testing.T) {
	tests := hostWitnessCases()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := HostWitness(tc.patterns...)
			if tc.wantErr {
				if !errors.Is(err, ErrUnsupportedSelector) {
					t.Fatalf("err = %v, want ErrUnsupportedSelector", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("HostWitness: %v", err)
			}
			if ok != tc.want {
				t.Fatalf("HostWitness(%v) ok = %v (%q), want %v",
					tc.patterns, ok, got, tc.want)
			}
			if !ok {
				return
			}
			// The witness must actually be matched by every selector.
			// Whatever the search does internally, the contract is this.
			for _, p := range tc.patterns {
				if !HostKeyMatches(p, got) {
					t.Errorf("witness %q is not matched by selector %q", got, p)
				}
			}
		})
	}
}

// TestHostSelectorSets: a single positive witness per selector pair is
// not enough for a caller deciding whether selectors can conflict,
// because a third selector matching that same witness can mask the
// conflict while a different hostname still exposes it.
//
// work-mbp matches all three selectors below, so the exact one's
// replacement hides any disagreement between the two globs. work-x-mbp
// matches only the two globs and does not, so both effective sets have to
// be enumerated.
func TestHostSelectorSets(t *testing.T) {
	selectors := []string{"work-*", "*-mbp", "work-mbp"}
	got, err := HostSelectorSets(selectors)
	if err != nil {
		t.Fatalf("HostSelectorSets: %v", err)
	}

	// Each returned witness must reproduce exactly the set it is labelled
	// with: every selector in the set matches, and every one outside it
	// does not. That is what makes an exclusion testable.
	for _, host := range got {
		for _, sel := range selectors {
			want := false
			for _, in := range MatchingHostKeys(selectors, host) {
				if in == sel {
					want = true
				}
			}
			if HostKeyMatches(sel, host) != want {
				t.Errorf("witness %q: selector %q match = %v, want %v",
					host, sel, !want, want)
			}
		}
	}

	// The two effective sets that decide the masking case must both be
	// represented.
	var sawAllThree, sawGlobsOnly bool
	for _, host := range got {
		switch len(MatchingHostKeys(selectors, host)) {
		case 3:
			sawAllThree = true
		case 2:
			if HostKeyMatches("work-*", host) && HostKeyMatches("*-mbp", host) &&
				!HostKeyMatches("work-mbp", host) {
				sawGlobsOnly = true
			}
		}
	}
	if !sawAllThree {
		t.Error("no witness matching all three selectors")
	}
	if !sawGlobsOnly {
		t.Error("no witness matching both globs but not the exact selector")
	}
}
