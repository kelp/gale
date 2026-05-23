package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// TestDoctorDoesNotHitRegistryByDefault pins
// audit/readonly/network-perf/0004 and
// read-only-invariant/0002: the default `gale doctor` run must
// not make any HTTP requests to the registry. Side-effect cache
// writes from doctor are how RO-B/0002 surfaced; the fix is to
// keep network probes opt-in via --check-registry.
func TestDoctorDoesNotHitRegistryByDefault(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&hits, 1)
			http.NotFound(w, r)
		}))
	defer srv.Close()

	// Point gale at our server via config.toml.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(home) // avoid picking up a project gale.toml from cwd
	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(galeDir, "config.toml"),
		[]byte("[registry]\nurl = \""+srv.URL+"\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(galeDir, "gale.toml"),
		[]byte("[packages]\njq = \"1.7.1\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	// Reset the --check-registry flag so a stale prior test
	// doesn't leak.
	doctorCheckRegistry = false
	// Some checks fail on a sparse fixture but we only assert
	// the network side effect here.
	_ = doctorCmd.RunE(doctorCmd, nil)

	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("default doctor hit the registry %d time(s); "+
			"network probes must be opt-in via --check-registry",
			got)
	}
}

// TestDoctorCheckRegistryFlagEnablesNetwork verifies that the
// opt-in flag wires back through to the resolver-using checks.
// Failing this test would mean --check-registry is a dead flag.
func TestDoctorCheckRegistryFlagEnablesNetwork(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&hits, 1)
			http.NotFound(w, r)
		}))
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(home) // avoid picking up a project gale.toml from cwd
	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(galeDir, "config.toml"),
		[]byte("[registry]\nurl = \""+srv.URL+"\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(galeDir, "gale.toml"),
		[]byte("[packages]\njq = \"1.7.1\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	doctorCheckRegistry = true
	t.Cleanup(func() { doctorCheckRegistry = false })
	_ = doctorCmd.RunE(doctorCmd, nil)

	if atomic.LoadInt32(&hits) == 0 {
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
