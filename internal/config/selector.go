package config

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// ErrUnsupportedSelector reports a host selector outside the grammar this
// package can decide overlap for.
//
// docs/configuration.md documents exactly three forms: a single hostname,
// a comma-separated list, and a `*` glob. filepath.Match happens to
// accept `?` and character classes as well, but that is a property of the
// implementation rather than a promise, and deciding intersection over
// them completely needs a rune-level model of the whole matcher language.
// Reporting the limit is what lets a caller fail closed instead of
// believing an answer that was never sound.
var ErrUnsupportedSelector = errors.New("unsupported host selector")

// ErrSelectorSearchTooLarge reports a selector set whose search space
// exceeded the bound below.
//
// It is an error rather than a truncated result on purpose. A caller uses
// these witnesses to prove that no conflict exists, so a partial answer
// silently omits the very cases it was asked to find. Unreachable for the
// supported grammar at any realistic size; it exists so a pathological
// gale.toml fails loudly rather than quietly.
var ErrSelectorSearchTooLarge = errors.New("host selector search too large")

// maxSelectorStates bounds the product search.
const maxSelectorStates = 200_000

// HostWitness returns a concrete hostname matched by every one of the
// given host selectors, reporting false when no such hostname exists.
//
// It answers "can these selectors apply to the same machine", which a
// lock writer must know: selectors that can co-apply have to agree about
// versions, while selectors that cannot are legitimately independent, so
// two exact hostnames may pin whatever they like.
func HostWitness(selectors ...string) (string, bool, error) {
	if len(selectors) == 0 {
		return "", false, nil
	}
	found := ""
	ok := false
	err := searchHosts(selectors, func(host string, matched []bool) bool {
		if !slices.Contains(matched, false) {
			found, ok = host, true
			return false
		}
		return true
	})
	if err != nil {
		return "", false, err
	}
	return found, ok, nil
}

// HostSelectorSets returns one witness hostname per distinct set of
// selectors that can apply together, excluding the empty set.
//
// A caller deciding whether selectors may disagree needs this rather than
// a witness per pair. A third selector matching the same hostname can
// mask a disagreement between two others through replacement, while a
// different hostname matching only those two still exposes it. Given
// `work-*`, `*-mbp`, and `work-mbp`, the hostname `work-mbp` applies all
// three and hides the conflict; `work-x-mbp` applies only the globs and
// does not. Both sets are returned.
//
// Every witness is verified with HostKeyMatches, so a returned hostname
// genuinely produces the set it stands for.
func HostSelectorSets(selectors []string) ([]string, error) {
	witnesses := make(map[string]string)
	err := searchHosts(selectors, func(host string, matched []bool) bool {
		if !slices.Contains(matched, true) {
			return true
		}
		key := signature(matched)
		if _, seen := witnesses[key]; !seen {
			witnesses[key] = host
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	out := slices.Collect(maps.Values(witnesses))
	slices.Sort(out)
	return out, nil
}

// checkSupported rejects a selector this package cannot decide.
//
// The grammar is the documented one: ASCII, literal characters, `*`, and
// commas separating alternatives. Excluding `?` and character classes is
// what makes the byte-level search below complete rather than
// approximate: with only literals and `*`, byte-wise and rune-wise
// matching agree, because `*` consumes any bytes and a literal ASCII byte
// is its own rune.
func checkSupported(sel string) error {
	for i := 0; i < len(sel); i++ {
		switch c := sel[i]; {
		case c >= 0x80:
			return fmt.Errorf(
				"%w: %q contains a non-ASCII character", ErrUnsupportedSelector, sel,
			)
		case c == '?' || c == '[' || c == ']':
			return fmt.Errorf(
				"%w: %q uses %q; supported forms are a hostname, "+
					"a comma-separated list, and a `*` glob",
				ErrUnsupportedSelector, sel, string(c),
			)
		}
	}
	return nil
}

// signature encodes which selectors a hostname matches.
func signature(matched []bool) string {
	var b strings.Builder
	for _, m := range matched {
		if m {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
	}
	return b.String()
}

// searchHosts enumerates candidate hostnames breadth-first over the
// product of every selector's match states, calling visit with each
// distinct product state's shortest hostname and which selectors it
// matches. visit returns false to stop the search.
//
// Breadth-first over a product of match-offset sets is what makes `*`
// tractable: it stands for both "consume another character" and "match
// nothing further", and the product is finite, so the search terminates
// once every reachable state is visited.
//
// The matched flags are recomputed with the public HostKeyMatches rather
// than read off the automaton, so an error in the search can only lose a
// candidate, never report one that does not hold.
func searchHosts(selectors []string, visit func(host string, matched []bool) bool) error {
	for _, sel := range selectors {
		if err := checkSupported(sel); err != nil {
			return err
		}
	}
	pats := flatten(selectors)
	if len(pats) == 0 {
		return nil
	}
	alphabet := alphabetOf(pats)
	start := make([]posSet, len(pats))
	for i, p := range pats {
		start[i] = closure(p, posSet{0: true})
	}
	type node struct {
		sets []posSet
		word string
	}
	queue := []node{{sets: start}}
	seen := map[string]bool{stateKey(start): true}
	for len(queue) > 0 {
		if len(seen) >= maxSelectorStates {
			return fmt.Errorf("%w: %v", ErrSelectorSearchTooLarge, selectors)
		}
		cur := queue[0]
		queue = queue[1:]
		if !visit(cur.word, matchesEach(selectors, cur.word)) {
			return nil
		}
		for _, c := range alphabet {
			next := make([]posSet, len(pats))
			live := false
			for i, p := range pats {
				next[i] = step(p, cur.sets[i], c)
				if len(next[i]) > 0 {
					live = true
				}
			}
			// Every pattern dead means no extension of this word can
			// match anything, so the branch is abandoned.
			if !live {
				continue
			}
			k := stateKey(next)
			if seen[k] {
				continue
			}
			seen[k] = true
			queue = append(queue, node{sets: next, word: cur.word + string(c)})
		}
	}
	return nil
}

// matchesEach reports, per selector, whether host matches it.
func matchesEach(selectors []string, host string) []bool {
	out := make([]bool, len(selectors))
	for i, sel := range selectors {
		out[i] = HostKeyMatches(sel, host)
	}
	return out
}

// flatten splits every selector into its comma-separated patterns. Which
// selector a pattern came from is deliberately not tracked: match results
// are recomputed with HostKeyMatches instead, so there is one authority on
// what a selector matches.
func flatten(selectors []string) []string {
	var pats []string
	for _, sel := range selectors {
		for pat := range strings.SplitSeq(sel, ",") {
			if pat = strings.TrimSpace(pat); pat != "" {
				pats = append(pats, pat)
			}
		}
	}
	return pats
}

// posSet is the set of pattern offsets a match could have reached.
type posSet map[int]bool

// stateKey renders the product state so the search visits it once.
func stateKey(sets []posSet) string {
	var b strings.Builder
	for _, s := range sets {
		for _, i := range slices.Sorted(maps.Keys(s)) {
			b.WriteString(strconv.Itoa(i))
			b.WriteByte(',')
		}
		b.WriteByte('|')
	}
	return b.String()
}

// closure adds the offsets reachable without consuming a character,
// which means skipping over a `*` that matches nothing.
func closure(p string, in posSet) posSet {
	out := make(posSet, len(in))
	var add func(i int)
	add = func(i int) {
		if out[i] {
			return
		}
		out[i] = true
		if i < len(p) && p[i] == '*' {
			add(i + 1)
		}
	}
	for i := range in {
		add(i)
	}
	return out
}

// step consumes one character from every offset that can accept it.
func step(p string, in posSet, c byte) posSet {
	out := make(posSet, len(in))
	for i := range in {
		if i >= len(p) {
			continue
		}
		if p[i] == '*' {
			// Consumes the character and stays, since `*` covers any
			// number of them.
			out[i] = true
			continue
		}
		if p[i] == c {
			out[i+1] = true
		}
	}
	return closure(p, out)
}

// alphabetOf collects the characters worth trying.
//
// Completeness rests on one observation: with only literals and `*`, a
// character no pattern mentions is treated identically by every pattern,
// since every literal rejects it and every `*` accepts it. So the
// mentioned characters plus a single unmentioned representative cover
// every distinguishable case.
//
// The representative is searched across the whole ASCII range rather than
// a fixed shortlist. A shortlist can be exhausted by the patterns
// themselves, and then there is no representative at all, which silently
// removes the "any other character" case from the search.
func alphabetOf(pats []string) []byte {
	seen := map[byte]bool{}
	var out []byte
	add := func(c byte) {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	for _, p := range pats {
		for i := 0; i < len(p); i++ {
			if p[i] != '*' {
				add(p[i])
			}
		}
	}
	// Selectors are ASCII by checkSupported, so some byte below 0x80 is
	// always unmentioned and stands for every character none of them name.
	for c := 0x21; c < 0x80; c++ {
		if !seen[byte(c)] {
			add(byte(c))
			break
		}
	}
	return out
}
