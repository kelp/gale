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
