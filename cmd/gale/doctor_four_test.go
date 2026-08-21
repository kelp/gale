package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/store"
)

const (
	doctorFourSHA  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	doctorFourSHA2 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestDoctorIsFourChecks(t *testing.T) {
	want := []string{
		"PATH",
		"lock readable",
		"generation matches lock roots",
		"tree digest matches",
	}
	if got := len(doctorChecks); got != 4 {
		t.Fatalf("doctorChecks = %d, want 4", got)
	}
	for i, name := range want {
		if doctorChecks[i].name != name {
			t.Errorf("doctorChecks[%d] = %q, want %q",
				i, doctorChecks[i].name, name)
		}
	}
}

func TestDoctorRejectsCheckRegistryFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".gale"), 0o755); err != nil {
		t.Fatal(err)
	}
	chdirTo(t, home)
	err := executeDoctor(t, "--check-registry")
	if err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("doctor --check-registry must be an unknown flag, got: %v", err)
	}
}

func TestDoctorLockReadable(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		h := newDoctorFourHome(t)
		setGalePATH(t, h.galeDir)
		stdout, stderr := runDoctorHome(t, h)
		if !strings.Contains(stderr, "lock readable") &&
			!strings.Contains(stderr, "lock") {
			t.Fatalf("missing lock must fail lock readable, stderr=%q", stderr)
		}
		if strings.Count(stderr, "fetch-adopt") > 0 {
			t.Errorf("absent lock must not name fetch-adopt, stderr=%q", stderr)
		}
		assertNoSecondLockRed(t, stderr, stdout)
	})
	t.Run("v1", func(t *testing.T) {
		h := newDoctorFourHome(t)
		setGalePATH(t, h.galeDir)
		if err := lockfile.WriteV1(h.lockPath, &lockfile.V1{
			Version: lockfile.SchemaVersion,
			Targets: lockfile.Targets{
				Default: &lockfile.Target{Roots: []string{"just@1.56.0-1"}},
			},
		}); err != nil {
			t.Fatal(err)
		}
		stdout, stderr := runDoctorHome(t, h)
		if !strings.Contains(stderr, "fetch-adopt") {
			t.Fatalf("v1 lock must name fetch-adopt, stderr=%q", stderr)
		}
		if strings.Contains(stderr, "gale verify") {
			t.Errorf("doctor must not print gale verify for a v1 lock: %q", stderr)
		}
		assertNoSecondLockRed(t, stderr, stdout)
	})
	t.Run("v2", func(t *testing.T) {
		h := newDoctorFourHome(t)
		plantDoctorFourFetch(t, h)
		linkDoctorFourGen(t, h, doctorFourSHA)
		setGalePATH(t, h.galeDir)
		_, stderr := runDoctorHome(t, h)
		if line := checkLine(t, stderr, "lock readable"); !strings.HasPrefix(line, "==> ") {
			t.Errorf("v2 lock must pass lock readable, got %q", line)
		}
	})
}

func TestDoctorGenerationMatchesFetchPath(t *testing.T) {
	h := newDoctorFourHome(t)
	plantDoctorFourFetch(t, h)
	linkDoctorFourGen(t, h, doctorFourSHA)
	setGalePATH(t, h.galeDir)
	_, stderr := runDoctorHome(t, h)
	if line := checkLine(t, stderr, "generation matches lock roots"); !strings.HasPrefix(line, "==> ") {
		t.Errorf("matching fetch gen must pass, got %q", line)
	}
}

func TestDoctorGenerationRefusesResolveDir(t *testing.T) {
	h := newDoctorFourHome(t)
	plantDoctorFourFetch(t, h)
	src := filepath.Join(h.storeRoot, "just", "1.56.0", "bin", "just")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("source-just\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	mkActiveGen(t, h.galeDir, 1, src)
	setGalePATH(t, h.galeDir)
	_, stderr := runDoctorHome(t, h)
	if line := checkLine(t, stderr, "generation matches lock roots"); !strings.HasPrefix(line, "xxx ") {
		t.Errorf("ResolveDir link must fail roots, got %q", line)
	}
	if !strings.Contains(stderr, "gale sync") {
		t.Errorf("roots drift must name gale sync, stderr=%q", stderr)
	}
}

func TestDoctorGenerationRefusesWrongSHA(t *testing.T) {
	h := newDoctorFourHome(t)
	plantDoctorFourFetch(t, h)
	plantDoctorFourTree(t, h, doctorFourSHA2)
	linkDoctorFourGen(t, h, doctorFourSHA2)
	setGalePATH(t, h.galeDir)
	_, stderr := runDoctorHome(t, h)
	if line := checkLine(t, stderr, "generation matches lock roots"); !strings.HasPrefix(line, "xxx ") {
		t.Errorf("wrong sha12 must fail roots, got %q", line)
	}
}

func TestDoctorGenerationRefusesDanglingCurrent(t *testing.T) {
	h := newDoctorFourHome(t)
	plantDoctorFourFetch(t, h)
	mkActiveGen(t, h.galeDir, 1)
	if err := os.RemoveAll(filepath.Join(h.galeDir, "gen", "1")); err != nil {
		t.Fatal(err)
	}
	setGalePATH(t, h.galeDir)
	_, stderr := runDoctorHome(t, h)
	if line := checkLine(t, stderr, "generation matches lock roots"); !strings.HasPrefix(line, "xxx ") {
		t.Errorf("dangling current must fail roots, got %q", line)
	}
}

func TestDoctorGenerationReadsProjectScope(t *testing.T) {
	h := newDoctorFourHome(t)
	plantDoctorFourFetch(t, h)
	linkDoctorFourGen(t, h, doctorFourSHA)
	setGalePATH(t, h.galeDir)

	proj := filepath.Join(h.home, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".gale"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "gale.toml"),
		[]byte("[packages]\njust = \"1.56.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.WriteV2(filepath.Join(proj, "gale.lock"), &lockfile.V2{
		Version: lockfile.SchemaV2,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{Roots: []string{"just@1.56.0"}},
		},
		Packages: map[string]lockfile.V2Package{
			"just@1.56.0": {Artifacts: map[string]lockfile.V2Artifact{
				currentPlatform(): {
					URL:        "https://github.com/kelp/just/releases/download/1.56.0/just",
					Format:     "binary",
					SHA256:     doctorFourSHA,
					TreeDigest: "sha256:dead",
					Method:     provenance.MethodFetch,
				},
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	stdout, stderr := runDoctorAt(t, h.galeDir, proj)
	if !strings.Contains(stderr, "project") &&
		!strings.Contains(stdout, "issue") {
		t.Fatalf("project lock without a matching gen must be visible, stderr=%q stdout=%q",
			stderr, stdout)
	}
}

func TestDoctorTreeDigestMatches(t *testing.T) {
	t.Run("match", func(t *testing.T) {
		h := newDoctorFourHome(t)
		plantDoctorFourFetch(t, h)
		linkDoctorFourGen(t, h, doctorFourSHA)
		setGalePATH(t, h.galeDir)
		_, stderr := runDoctorHome(t, h)
		if line := checkLine(t, stderr, "tree digest matches"); !strings.HasPrefix(line, "==> ") {
			t.Errorf("matching digest must pass, got %q", line)
		}
	})
	t.Run("mutated", func(t *testing.T) {
		h := newDoctorFourHome(t)
		plantDoctorFourFetch(t, h)
		dest, err := store.NewStore(h.storeRoot).FetchPath("just", "1.56.0", doctorFourSHA)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dest, "bin", "just"), []byte("tampered\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		linkDoctorFourGen(t, h, doctorFourSHA)
		setGalePATH(t, h.galeDir)
		_, stderr := runDoctorHome(t, h)
		if line := checkLine(t, stderr, "tree digest matches"); !strings.HasPrefix(line, "xxx ") {
			t.Errorf("mutated tree must fail digest, got %q", line)
		}
		if !strings.Contains(stderr, "gale verify") {
			t.Errorf("digest failure must name gale verify, stderr=%q", stderr)
		}
		if strings.Contains(stderr, "gale verify needs") {
			t.Errorf("doctor must not leak verify's command-prefixed errors: %q", stderr)
		}
	})
	t.Run("attestation", func(t *testing.T) {
		h := newDoctorFourHome(t)
		digest := plantDoctorFourTree(t, h, doctorFourSHA)
		writeDoctorFourLock(t, h, doctorFourSHA, digest, true)
		if err := provenance.WriteFetch(
			mustFetchPath(t, h.storeRoot, doctorFourSHA),
			provenance.FetchRecord{
				Name: "just", Version: "1.56.0", SHA256: doctorFourSHA,
				TreeDigest: digest, Method: provenance.MethodFetch,
			},
		); err != nil {
			t.Fatal(err)
		}
		linkDoctorFourGen(t, h, doctorFourSHA)
		setGalePATH(t, h.galeDir)
		_, stderr := runDoctorHome(t, h)
		if line := checkLine(t, stderr, "tree digest matches"); !strings.HasPrefix(line, "xxx ") {
			t.Errorf("locked attestation must fail digest, got %q", line)
		}
	})
}

type doctorFourHome struct {
	home, galeDir, storeRoot, lockPath string
}

func newDoctorFourHome(t *testing.T) doctorFourHome {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(galeDir, "gale.toml"),
		[]byte("[packages]\njust = \"1.56.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	chdirTo(t, home)
	lp, err := lockfilePath(filepath.Join(galeDir, "gale.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return doctorFourHome{
		home:      home,
		galeDir:   galeDir,
		storeRoot: filepath.Join(galeDir, "pkg"),
		lockPath:  lp,
	}
}

func setGalePATH(t *testing.T, galeDir string) {
	t.Helper()
	bin := filepath.Join(galeDir, "current", "bin")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func runDoctorHome(t *testing.T, h doctorFourHome) (stdout, stderr string) {
	t.Helper()
	return runDoctorAt(t, h.galeDir, h.home)
}

func runDoctorAt(t *testing.T, galeDir, cwd string) (stdout, stderr string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	_ = runDoctor(&doctorIO{
		galeDir: galeDir,
		cwd:     cwd,
		stdout:  &out,
		stderr:  &errBuf,
	})
	return out.String(), errBuf.String()
}

func assertNoSecondLockRed(t *testing.T, stderr, stdout string) {
	t.Helper()
	red := 0
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(line, "xxx ") {
			red++
		}
	}
	if red > 1 {
		t.Errorf("missing/v1 lock must not pile roots/digest on the same file, red=%d stderr=%q",
			red, stderr)
	}
	if strings.Contains(stdout, "of 4 checks") &&
		strings.Contains(stdout, "3 issue") {
		t.Errorf("one unreadable lock must not count as 3 of 4, stdout=%q", stdout)
	}
}

func plantDoctorFourFetch(t *testing.T, h doctorFourHome) {
	t.Helper()
	digest := plantDoctorFourTree(t, h, doctorFourSHA)
	if err := provenance.WriteFetch(
		mustFetchPath(t, h.storeRoot, doctorFourSHA),
		provenance.FetchRecord{
			Name: "just", Version: "1.56.0", SHA256: doctorFourSHA,
			TreeDigest: digest, Method: provenance.MethodFetch,
		},
	); err != nil {
		t.Fatal(err)
	}
	writeDoctorFourLock(t, h, doctorFourSHA, digest, false)
}

func plantDoctorFourTree(t *testing.T, h doctorFourHome, sha string) string {
	t.Helper()
	dest := mustFetchPath(t, h.storeRoot, sha)
	bin := filepath.Join(dest, "bin", "just")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("just-"+sha[:12]+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	digest, err := provenance.DigestTree(context.Background(), dest)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func writeDoctorFourLock(t *testing.T, h doctorFourHome, sha, digest string, attest bool) {
	t.Helper()
	art := lockfile.V2Artifact{
		URL:        "https://github.com/kelp/just/releases/download/1.56.0/just",
		Format:     "binary",
		SHA256:     sha,
		TreeDigest: digest,
		Method:     provenance.MethodFetch,
		Files: []lockfile.V2File{{
			Src: "just", Dest: "bin/just", Mode: 0o755,
		}},
	}
	if attest {
		art.Attestation = &lockfile.V2Attestation{}
	}
	if err := lockfile.WriteV2(h.lockPath, &lockfile.V2{
		Version: lockfile.SchemaV2,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{Roots: []string{"just@1.56.0"}},
		},
		Packages: map[string]lockfile.V2Package{
			"just@1.56.0": {Artifacts: map[string]lockfile.V2Artifact{
				currentPlatform(): art,
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func linkDoctorFourGen(t *testing.T, h doctorFourHome, sha string) {
	t.Helper()
	dest := mustFetchPath(t, h.storeRoot, sha)
	mkActiveGen(t, h.galeDir, 1, filepath.Join(dest, "bin", "just"))
}

func mustFetchPath(t *testing.T, storeRoot, sha string) string {
	t.Helper()
	dest, err := store.NewStore(storeRoot).FetchPath("just", "1.56.0", sha)
	if err != nil {
		t.Fatal(err)
	}
	return dest
}
