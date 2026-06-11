package main

import (
	"os"
	"testing"
)

// TestNewCmdContextWiresParallelism verifies that the resolved
// download/sync parallelism (here driven via GALE_JOBS, which
// config.ResolveParallelism honours first) flows into both the
// cmdContext and the Installer's Downloads limiter, so one
// configured number bounds total in-flight downloads.
func TestNewCmdContextWiresParallelism(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate ~/.gale (project registry)
	tmp := t.TempDir()
	if err := os.WriteFile(
		tmp+"/gale.toml", []byte("[packages]\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(origDir) })

	// GALE_JOBS is the highest-precedence input to
	// config.ResolveParallelism, so it exercises the wiring
	// without writing a config.toml.
	t.Setenv("GALE_JOBS", "3")

	ctx, err := newCmdContext("", false, false)
	if err != nil {
		t.Fatalf("newCmdContext: %v", err)
	}

	if ctx.Parallelism != 3 {
		t.Errorf("ctx.Parallelism = %d, want 3", ctx.Parallelism)
	}
	if got := ctx.Installer.Downloads.Cap(); got != 3 {
		t.Errorf("Installer.Downloads cap = %d, want 3", got)
	}
}
