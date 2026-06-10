package registry

import "testing"

// TestPickLatestDeterministicForNonSemverKeys is the repro for
// gh#58: socat's real index keys are non-semver ("1.8.1.1" has
// four components), so version.IsNewer returns true for every
// comparison and pickLatest degenerates to last-map-key-wins.
// Go randomizes map iteration, so before the fix this resolved
// to the OLDER revision ~10-13% of the time. 300 iterations make
// a pre-fix pass vanishingly unlikely (~0.88^300).
func TestPickLatestDeterministicForNonSemverKeys(t *testing.T) {
	idx := map[string]string{
		"1.8.1.1":   "aaaaaaa",
		"1.8.1.1-2": "bbbbbbb",
	}
	for i := 0; i < 300; i++ {
		got, ok := pickLatest(idx)
		if !ok {
			t.Fatalf("iteration %d: pickLatest ok = false", i)
		}
		if got != "1.8.1.1-2" {
			t.Fatalf("iteration %d: pickLatest = %q, want %q "+
				"(revision bump silently ignored, gh#58)",
				i, got, "1.8.1.1-2")
		}
	}
}

// TestPickLatestNaturalOrderForNonSemverVersions pins the
// deterministic fallback ordering for non-semver version strings
// (autossh's "1.4g", arm-none-eabi-gcc's "15.2.rel1"): digit
// runs compare numerically, everything else byte-wise.
func TestPickLatestNaturalOrderForNonSemverVersions(t *testing.T) {
	cases := []struct {
		name string
		idx  map[string]string
		want string
	}{
		{
			name: "letter suffix",
			idx:  map[string]string{"1.4g": "a", "1.4h": "b"},
			want: "1.4h",
		},
		{
			name: "rel components with numeric runs",
			idx: map[string]string{
				"15.2.rel1": "a",
				"15.2.rel2": "b",
				"9.9.rel9":  "c",
			},
			want: "15.2.rel2",
		},
		{
			name: "numeric runs beat lexicographic",
			idx:  map[string]string{"1.8.1.2": "a", "1.8.1.10": "b"},
			want: "1.8.1.10",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for i := 0; i < 100; i++ {
				got, ok := pickLatest(tc.idx)
				if !ok {
					t.Fatalf("pickLatest ok = false")
				}
				if got != tc.want {
					t.Fatalf("iteration %d: pickLatest = %q, want %q",
						i, got, tc.want)
				}
			}
		})
	}
}
