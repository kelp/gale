package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// BUG-6: audit and verify resolve lockfile from wrong scope.
// Both must derive the lockfile path from the context's
// GalePath rather than calling resolveConfigPath independently.

func TestAuditDoesNotCallResolveConfigPathSeparately(t *testing.T) {
	// Parse audit.go and verify it does not call
	// resolveConfigPath before newCmdContext.
	assertNoStandaloneResolveConfigPath(t, "audit.go")
}

func TestVerifyDoesNotCallResolveConfigPathSeparately(t *testing.T) {
	assertNoStandaloneResolveConfigPath(t, "verify.go")
}

// TestAuditDoesNotUseFprintfStderrDirectly pins audit
// RO-J:stream-discipline/0004: audit must route detail lines
// through internal/output helpers, not raw `fmt.Fprintf(
// os.Stderr, ...)`. The raw writes bypass quiet/color modes
// — a `-q` user still sees the SHA pair.
func TestAuditDoesNotUseFprintfStderrDirectly(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "audit.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing audit.go: %v", err)
	}

	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if pkg.Name != "fmt" {
			return true
		}
		// Look for fmt.Fprintf / fmt.Fprintln / fmt.Fprint
		// targeting os.Stderr.
		if !strings.HasPrefix(sel.Sel.Name, "Fprint") {
			return true
		}
		if len(call.Args) == 0 {
			return true
		}
		target, ok := call.Args[0].(*ast.SelectorExpr)
		if !ok {
			return true
		}
		targetPkg, ok := target.X.(*ast.Ident)
		if !ok {
			return true
		}
		if targetPkg.Name == "os" && target.Sel.Name == "Stderr" {
			t.Errorf(
				"audit.go calls fmt.%s(os.Stderr, ...) — "+
					"route through internal/output helpers "+
					"so quiet/color modes are respected",
				sel.Sel.Name)
		}
		return true
	})
}

func assertNoStandaloneResolveConfigPath(t *testing.T, file string) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", file, err)
	}

	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		if strings.Contains(ident.Name, "resolveConfigPath") {
			t.Errorf(
				"%s should not call resolveConfigPath "+
					"— derive lockfile from ctx.GalePath",
				file)
		}
		return true
	})
}
