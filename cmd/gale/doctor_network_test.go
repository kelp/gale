package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/kelp/gale/internal/registry"
)

func doctorNetworkFixture(t *testing.T) *int32 {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&hits, 1)
			http.NotFound(w, r)
		},
	))
	t.Cleanup(srv.Close)

	reg, err := registry.NewWithURL(srv.URL)
	if err != nil {
		t.Fatalf("NewWithURL: %v", err)
	}
	injectedRegistry = reg
	t.Cleanup(func() { injectedRegistry = nil })

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(home)
	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(galeDir, "gale.toml"),
		[]byte("[packages]\njq = \"1.7.1\"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	return &hits
}

// TestDoctorDoesNotHitRegistryByDefault: doctor is four local
// checks. Nothing talks to the registry.
func TestDoctorDoesNotHitRegistryByDefault(t *testing.T) {
	hits := doctorNetworkFixture(t)
	_ = doctorCmd.RunE(doctorCmd, nil)
	if got := atomic.LoadInt32(hits); got != 0 {
		t.Errorf("doctor hit the registry %d time(s)", got)
	}
}

// TestDoctorHasNoRepairFlag pins the Milestone 2 deletion: doctor
// never mutates.
func TestDoctorHasNoRepairFlag(t *testing.T) {
	if f := doctorCmd.Flags().Lookup("repair"); f != nil {
		t.Fatalf("doctor --repair must be gone, found usage %q", f.Usage)
	}
}

// TestDoctorHasNoForceFlag pins the same deletion. gc --force is gone;
// doctor's --force was only the repair escape hatch.
func TestDoctorHasNoForceFlag(t *testing.T) {
	if f := doctorCmd.Flags().Lookup("force"); f != nil {
		t.Fatalf("doctor --force must be gone, found usage %q", f.Usage)
	}
}
