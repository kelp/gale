package version

import (
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

// KeyNewer reports whether version key a orders strictly after key
// b under gale's total order. Bases compare by semver when both
// parse as semver, by natural (digit-run-aware) ordering otherwise;
// equal bases break the tie on the numeric "-<N>" revision suffix
// (absent means 1), then on the raw string so the order is total.
//
// Unlike IsNewer, this never returns optimistically true for
// non-semver strings — a max-selection loop over map keys needs a
// deterministic order or it degenerates to last-key-wins (gh#58).
func KeyNewer(a, b string) bool {
	aBase, aRev := splitRevision(a)
	bBase, bRev := splitRevision(b)
	if aBase != bBase {
		av, bv := "v"+aBase, "v"+bBase
		if semver.IsValid(av) && semver.IsValid(bv) {
			if c := semver.Compare(av, bv); c != 0 {
				return c > 0
			}
		} else if c := naturalCompare(aBase, bBase); c != 0 {
			return c > 0
		}
	}
	if aRev != bRev {
		return aRev > bRev
	}
	return a > b
}

// Latest returns the newest key in keys under KeyNewer, and false
// when keys is empty.
func Latest(keys []string) (string, bool) {
	best := ""
	for _, k := range keys {
		if best == "" || KeyNewer(k, best) {
			best = k
		}
	}
	return best, best != ""
}

// Pick resolves a user-supplied version string against the set of
// available version keys. Exact match wins; a bare base (no
// "-<digits>" suffix) resolves to its highest available revision;
// a "-1" request falls back to a bare legacy entry. Returns
// ("", false) when nothing matches.
func Pick(keys []string, requested string) (string, bool) {
	set := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		set[k] = struct{}{}
	}
	// 1. Exact match wins immediately.
	if _, ok := set[requested]; ok {
		return requested, true
	}
	// 2. A "-1" request finds a bare entry recorded before
	//    revisions existed (revision 1 is the implicit default).
	if bare, ok := strings.CutSuffix(requested, "-1"); ok {
		if _, ok := set[bare]; ok {
			return bare, true
		}
	}
	// 3. Other -<digits> suffixes get no fallback — only bare
	//    bases bump to the latest revision.
	if hasRevisionSuffix(requested) {
		return "", false
	}
	// 4. Scan for "<requested>-<N>" entries; pick the highest N.
	prefix := requested + "-"
	bestRev := -1
	bestKey := ""
	for _, k := range keys {
		suf, ok := strings.CutPrefix(k, prefix)
		if !ok {
			continue
		}
		n, err := strconv.Atoi(suf)
		if err != nil || n < 0 {
			continue
		}
		if n > bestRev {
			bestRev = n
			bestKey = k
		}
	}
	if bestKey != "" {
		return bestKey, true
	}
	return "", false
}

// hasRevisionSuffix reports whether v ends with "-<digits>".
func hasRevisionSuffix(v string) bool {
	i := strings.LastIndex(v, "-")
	if i < 0 || i == len(v)-1 {
		return false
	}
	for _, c := range v[i+1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// naturalCompare compares two strings chunk-wise: maximal runs of
// ASCII digits compare numerically, everything else compares
// byte-wise. Gives "1.8.1.10" > "1.8.1.2" and "15.2.rel2" >
// "15.2.rel1" without pretending such strings are semver.
func naturalCompare(a, b string) int {
	for a != "" && b != "" {
		if isASCIIDigit(a[0]) && isASCIIDigit(b[0]) {
			aNum, aRest := splitLeadingDigits(a)
			bNum, bRest := splitLeadingDigits(b)
			at := strings.TrimLeft(aNum, "0")
			bt := strings.TrimLeft(bNum, "0")
			// Longer trimmed digit run is the bigger number;
			// equal lengths compare lexically (same as
			// numerically for equal-length digit strings).
			if len(at) != len(bt) {
				if len(at) < len(bt) {
					return -1
				}
				return 1
			}
			if at != bt {
				if at < bt {
					return -1
				}
				return 1
			}
			a, b = aRest, bRest
			continue
		}
		if a[0] != b[0] {
			if a[0] < b[0] {
				return -1
			}
			return 1
		}
		a, b = a[1:], b[1:]
	}
	switch {
	case a == "" && b == "":
		return 0
	case a == "":
		return -1
	default:
		return 1
	}
}

func isASCIIDigit(c byte) bool { return c >= '0' && c <= '9' }

// splitLeadingDigits splits s into its maximal leading digit run
// and the remainder.
func splitLeadingDigits(s string) (digits, rest string) {
	i := 0
	for i < len(s) && isASCIIDigit(s[i]) {
		i++
	}
	return s[:i], s[i:]
}
