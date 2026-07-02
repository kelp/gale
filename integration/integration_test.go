//go:build integration

package integration_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/kelp/gale/integration/support"
	"github.com/rogpeppe/go-internal/testscript"
)

var (
	galeBin      string
	fixturesRoot string
	payloads     *support.Payloads
)

func TestMain(m *testing.M) {
	os.Exit(runMain(m))
}

func runMain(m *testing.M) int {
	tmpDir, err := os.MkdirTemp("", "gale-integration-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mktemp:", err)
		return 2
	}
	defer os.RemoveAll(tmpDir)

	name := "gale"
	if runtime.GOOS == "windows" {
		name = "gale.exe"
	}
	galeBin = filepath.Join(tmpDir, name)

	build := exec.Command("go", "build", "-o", galeBin, "../cmd/gale")
	build.Stderr = os.Stderr
	build.Stdout = os.Stdout
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build gale:", err)
		return 2
	}

	fixturesRoot, err = filepath.Abs("fixtures")
	if err != nil {
		fmt.Fprintln(os.Stderr, "abs fixtures:", err)
		return 2
	}

	payloads, err = support.BuildPayloads(fixturesRoot, filepath.Join(tmpDir, "payloads"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "build payloads:", err)
		return 2
	}

	return m.Run()
}

func TestIntegration(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir:                 "scripts",
		RequireExplicitExec: true,
		Setup: func(env *testscript.Env) error {
			return setupScript(t, env)
		},
		Cmds: map[string]func(ts *testscript.TestScript, neg bool, args []string){
			"gale-fixture":         support.CmdFixture,
			"gale-attest-referrer": support.CmdAttestReferrer,
		},
	})
}

func setupScript(t *testing.T, env *testscript.Env) error {
	t.Helper()

	home := filepath.Join(env.WorkDir, "home")
	if err := os.MkdirAll(filepath.Join(home, ".gale"), 0o755); err != nil {
		return fmt.Errorf("mkdir home: %w", err)
	}

	env.Setenv("HOME", home)
	env.Setenv("FIXTURES", fixturesRoot)

	ghcr := support.StartFakeGHCR(t, payloads)
	env.Values["ghcr"] = ghcr
	env.Setenv("GHCR_URL", ghcr.URL)
	// Point gale's OCI-referrer attestation fetch (gale verify) at the
	// fake registry so it stays hermetic. Only ghcr.BaseURL() reads
	// this; recipe URLs use the templated __GHCR_URL__ directly.
	env.Setenv("GALE_GHCR_URL", ghcr.URL)
	env.Defer(ghcr.Close)

	for name, p := range payloads.Map {
		env.Setenv(support.EnvNameForSHA(name), p.SHA256)
	}

	sep := string(os.PathListSeparator)
	env.Setenv("PATH",
		filepath.Dir(galeBin)+sep+env.Getenv("PATH"))
	return nil
}
