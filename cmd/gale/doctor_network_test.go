package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

// TestDoctorDoesNotHitRegistryByDefault pins
// audit/readonly/network-perf/0004 and
// read-only-invariant/0002: the default `gale doctor` run must
// not make any HTTP requests to the registry. The injected
// server is the one doctor would reach if the gate leaked.
func TestDoctorDoesNotHitRegistryByDefault(t *testing.T) {
	hits := doctorNetworkFixture(t)

	doctorCheckRegistry = false
	_ = doctorCmd.RunE(doctorCmd, nil)

	if got := atomic.LoadInt32(hits); got != 0 {
		t.Errorf("default doctor hit the registry %d time(s); "+
			"network probes must be opt-in via --check-registry",
			got)
	}
}

// TestDoctorCheckRegistryFlagEnablesNetwork verifies that the
// opt-in flag wires back through to the resolver-using checks.
func TestDoctorCheckRegistryFlagEnablesNetwork(t *testing.T) {
	hits := doctorNetworkFixture(t)

	doctorCheckRegistry = true
	t.Cleanup(func() { doctorCheckRegistry = false })
	_ = doctorCmd.RunE(doctorCmd, nil)

	if atomic.LoadInt32(hits) == 0 {
		t.Error("expected --check-registry to enable network probes, " +
			"got 0 hits")
	}
}

// TestDoctorHasCheckRegistryFlag pins the flag's existence so
// it doesn't regress. The default must be off (so airplane-mode
// is the contract, not the exception).
func TestDoctorHasCheckRegistryFlag(t *testing.T) {
	f := doctorCmd.Flags().Lookup("check-registry")
	if f == nil {
		t.Fatal("--check-registry flag missing from doctor")
	}
	if f.DefValue != "false" {
		t.Errorf("--check-registry default = %q, want false",
			f.DefValue)
	}
	if !strings.Contains(strings.ToLower(f.Usage), "registry") &&
		!strings.Contains(strings.ToLower(f.Usage), "network") {
		t.Errorf("--check-registry usage doesn't mention network/registry: %q",
			f.Usage)
	}
}

// TestDoctorHasNoRepairFlag pins the Milestone 2 deletion: doctor
// never mutates. --check-registry stays.
func TestDoctorHasNoRepairFlag(t *testing.T) {
	if f := doctorCmd.Flags().Lookup("repair"); f != nil {
		t.Fatalf("doctor --repair must be gone, found usage %q", f.Usage)
	}
}

// TestDoctorHasNoForceFlag pins the same deletion. gc --force stays;
// doctor's --force was only the repair escape hatch.
func TestDoctorHasNoForceFlag(t *testing.T) {
	if f := doctorCmd.Flags().Lookup("force"); f != nil {
		t.Fatalf("doctor --force must be gone, found usage %q", f.Usage)
	}
}
