package registry

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// SearchResult represents a package found by search.
type SearchResult struct {
	Name        string
	Description string
	Score       int
}

// Search fetches the package index from the registry and
// returns packages matching the query, sorted by relevance.
func (r *Registry) Search(query string) ([]SearchResult, error) {
	url := r.BaseURL + "/index.tsv"

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch index: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch index: HTTP %d",
			resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read index: %w", err)
	}

	entries := parseIndex(string(body))
	q := strings.ToLower(query)

	var results []SearchResult
	for _, e := range entries {
		// Score against name and description separately,
		// take the best.
		nameScore := fuzzyScore(q, strings.ToLower(e.Name))
		descScore := fuzzyScore(q, strings.ToLower(e.Description))

		// Name matches get a bonus.
		if nameScore > 0 {
			nameScore += 100
		}

		best := nameScore
		if descScore > best {
			best = descScore
		}

		if best > 0 {
			results = append(results, SearchResult{
				Name:        e.Name,
				Description: e.Description,
				Score:       best,
			})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	return results, nil
}

// indexEntry is a raw name+description from the index.
type indexEntry struct {
	Name        string
	Description string
}

// parseIndex parses the TSV index file into entries.
func parseIndex(data string) []indexEntry {
	var entries []indexEntry
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		entry := indexEntry{Name: parts[0]}
		if len(parts) == 2 {
			entry.Description = parts[1]
		}
		entries = append(entries, entry)
	}
	return entries
}

// fuzzyScore returns a score for how well query matches
// target. Returns 0 for no match. Higher is better.
//
// Matching strategy:
//   - Exact match: highest score
//   - Prefix match: high score
//   - Substring match: medium score
//   - All query chars present in order: low score
//   - No match: 0
func fuzzyScore(query, target string) int {
	if query == "" || target == "" {
		return 0
	}

	// Exact match.
	if query == target {
		return 1000
	}

	// Prefix match.
	if strings.HasPrefix(target, query) {
		return 500
	}

	// Substring match (word boundary).
	if strings.Contains(target, query) {
		return 200
	}

	// Subsequence match: all query chars appear in order.
	qi := 0
	for ti := 0; ti < len(target) && qi < len(query); ti++ {
		if target[ti] == query[qi] {
			qi++
		}
	}
	if qi == len(query) {
		return 50
	}

	return 0
}
