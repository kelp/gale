package main

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/recipe"
)

// TestOutdatedSortedOutput pins audit RO-J:output-format/0004:
// checkOutdated must return items in name-sorted order even when
// the input map has packages in non-alphabetical iteration order.
// This is a behavioral test: it calls checkOutdated directly with
// a multi-entry input and asserts the returned slice is sorted.
func TestOutdatedSortedOutput(t *testing.T) {
	// Packages in non-alphabetical order (map iteration is random).
	pkgs := map[string]string{
		"zz-tool": "1.0.0",
		"aa-tool": "1.0.0",
		"mm-tool": "1.0.0",
	}

	// Resolver returns a newer version for every package so all
	// appear in result.Items.
	resolver := func(_ context.Context, name string) (*recipe.Recipe, error) {
		return &recipe.Recipe{
			Package: recipe.Package{
				Name:    name,
				Version: "2.0.0",
			},
		}, nil
	}

	// Use a nil output to discard any warnings.
	out := newCmdOutput(outdatedCmd)
	result := checkOutdated(pkgs, resolver, out)

	if len(result.Items) != len(pkgs) {
		t.Fatalf("expected %d items, got %d",
			len(pkgs), len(result.Items))
	}

	for i := 1; i < len(result.Items); i++ {
		if result.Items[i].Name < result.Items[i-1].Name {
			t.Errorf("items not sorted: %q before %q",
				result.Items[i-1].Name,
				result.Items[i].Name)
		}
	}
}

// TestFormatOutdatedPreservesOrder verifies the helper keeps
// the caller's input order intact. The caller is responsible
// for sorting; formatOutdated itself must not shuffle.
func TestFormatOutdatedPreservesOrder(t *testing.T) {
	items := []outdatedItem{
		{"zlib", "1.2", "1.3"},
		{"jq", "1.7", "1.8"},
		{"go", "1.25", "1.26"},
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Name < items[j].Name
	})
	lines := formatOutdated(items)
	want := []string{"go", "jq", "zlib"}
	for i, w := range want {
		if !strings.Contains(lines[i], w) {
			t.Errorf("line %d = %q, want it to start with %s",
				i, lines[i], w)
		}
	}
}

func TestFormatOutdated(t *testing.T) {
	tests := []struct {
		name      string
		items     []outdatedItem
		wantLines int
		wantEmpty bool
	}{
		{
			"no outdated packages",
			nil,
			0,
			true,
		},
		{
			"one outdated package",
			[]outdatedItem{
				{"jq", "1.7.1", "1.8.1"},
			},
			1,
			false,
		},
		{
			"multiple outdated packages",
			[]outdatedItem{
				{"jq", "1.7.1", "1.8.1"},
				{"go", "1.25.0", "1.26.1"},
			},
			2,
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := formatOutdated(tt.items)
			if tt.wantEmpty && len(lines) != 0 {
				t.Errorf("expected empty, got %d lines",
					len(lines))
			}
			if !tt.wantEmpty && len(lines) != tt.wantLines {
				t.Errorf("got %d lines, want %d",
					len(lines), tt.wantLines)
			}
			// Each line should contain name, current, and
			// latest version.
			for i, line := range lines {
				item := tt.items[i]
				if !strings.Contains(line, item.Name) ||
					!strings.Contains(line, item.Current) ||
					!strings.Contains(line, item.Latest) {
					t.Errorf("line %q missing info for %s",
						line, item.Name)
				}
			}
		})
	}
}
