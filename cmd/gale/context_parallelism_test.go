package main

import (
	"os"
	"testing"

	"github.com/kelp/gale/internal/config"
)

// TestNewCmdContextWiresParallelism verifies the compiled
// limiter size reaches both cmdContext and the Installer's
// Downloads cap. GALE_JOBS is inert.
func TestNewCmdContextWiresParallelism(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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

	t.Setenv("GALE_JOBS", "3")

	ctx, err := newCmdContext("", false, false)
	if err != nil {
		t.Fatalf("newCmdContext: %v", err)
	}

	want := config.DefaultParallelism
	if ctx.Parallelism != want {
		t.Errorf("ctx.Parallelism = %d, want %d", ctx.Parallelism, want)
	}
	if got := ctx.Installer.Downloads.Cap(); got != want {
		t.Errorf("Installer.Downloads cap = %d, want %d", got, want)
	}
}
