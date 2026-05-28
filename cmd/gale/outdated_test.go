package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"testing"
)

// TestOutdatedSortsByName pins audit RO-J:output-format/0004:
// outdated's display order must be sorted by package name. Go
// map iteration is randomised, so iterating `cfg.Packages`
// directly produces non-deterministic output that peer
// read-only commands (list, sbom, env, inspect) already avoid.
//
// Static check: outdated.go must call sort.* before iterating
// cfg.Packages, so the resulting items slice has a stable
// order.
func TestOutdatedSortsByName(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(
		fset, "outdated.go", nil, 0,
	)
	if err != nil {
		t.Fatalf("parsing outdated.go: %v", err)
	}

	callsSort := false
	rangesOverPackagesDirectly := false

	ast.Inspect(f, func(n ast.Node) bool {
		// Detect any call to sort.* (sort.Strings, sort.Slice,
		// etc.).
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if ident, ok := sel.X.(*ast.Ident); ok {
					if ident.Name == "sort" {
						callsSort = true
					}
				}
			}
		}
		// Detect `for ... := range cfg.Packages` (map iter).
		if rs, ok := n.(*ast.RangeStmt); ok {
			if sel, ok := rs.X.(*ast.SelectorExpr); ok {
				if sel.Sel.Name == "Packages" {
					rangesOverPackagesDirectly = true
				}
			}
		}
		return true
	})

	if rangesOverPackagesDirectly && !callsSort {
		t.Errorf(
			"outdated.go iterates cfg.Packages (a map) without " +
				"calling sort.* — output order will vary " +
				"between runs. Build a sorted keys slice first.",
		)
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
		if !contains(lines[i], w) {
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
				if !contains(line, item.Name) ||
					!contains(line, item.Current) ||
					!contains(line, item.Latest) {
					t.Errorf("line %q missing info for %s",
						line, item.Name)
				}
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) &&
		findSubstring(s, substr)
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
