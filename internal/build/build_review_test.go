package build

// Tests for architectural review findings H1–H4 on
// build.go. Each test describes the invariant the fix
// defends, not the mechanic used to implement it. See
// gale-project/TODO.md for the full findings text.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kelp/gale/internal/recipe"
)

// --- H1: HOME and TMPDIR are build-scoped ---

// TestBuildEnvHomeIsBuildScoped asserts HOME does not
// inherit the host user's HOME. Autotools config.log,
// libtool .la files, and a few CMake artifacts bake
// absolute HOME paths into output — a leak here produces
// byte-nondeterministic archives across machines.
func TestBuildEnvHomeIsBuildScoped(t *testing.T) {
	t.Setenv("HOME", "/host/home/value")

	env, cleanup, err := buildEnv(&BuildContext{
		PrefixDir: "/tmp/prefix",
		Jobs:      "4",
		Version:   "1.0.0",
	})
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("buildEnv: %v", err)
	}
	m := envToMap(env)

	home := m["HOME"]
	if home == "" {
		t.Fatal("HOME should be set")
	}
	if home == "/host/home/value" {
		t.Errorf("HOME = %q, want build-scoped path "+
			"(not host HOME)", home)
	}
	info, statErr := os.Stat(home)
	if statErr != nil {
		t.Fatalf("build-scoped HOME should exist: %v", statErr)
	}
	if !info.IsDir() {
		t.Errorf("HOME = %q is not a directory", home)
	}
}

// TestBuildEnvTmpDirIsBuildScoped asserts TMPDIR does
// not inherit the host TMPDIR. Build tools that write
// to TMPDIR (libtool intermediate files in particular)
// embed paths that leak the host layout into output.
func TestBuildEnvTmpDirIsBuildScoped(t *testing.T) {
	t.Setenv("TMPDIR", "/host/tmp/value")

	env, cleanup, err := buildEnv(&BuildContext{
		PrefixDir: "/tmp/prefix",
		Jobs:      "4",
		Version:   "1.0.0",
	})
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("buildEnv: %v", err)
	}
	m := envToMap(env)

	tmp := m["TMPDIR"]
	if tmp == "" {
		t.Fatal("TMPDIR should be set")
	}
	if tmp == "/host/tmp/value" {
		t.Errorf("TMPDIR = %q, want build-scoped path "+
			"(not host TMPDIR)", tmp)
	}
	info, statErr := os.Stat(tmp)
	if statErr != nil {
		t.Fatalf("build-scoped TMPDIR should exist: %v",
			statErr)
	}
	if !info.IsDir() {
		t.Errorf("TMPDIR = %q is not a directory", tmp)
	}
}

// TestBuildEnvCleanupRemovesHomeAndTmpDir asserts the
// build-scoped HOME and TMPDIR are removed when the
// build finishes. Leaking these across builds defeats
// the isolation and fills ~/.gale/tmp/.
func TestBuildEnvCleanupRemovesHomeAndTmpDir(t *testing.T) {
	env, cleanup, err := buildEnv(&BuildContext{
		PrefixDir: "/tmp/prefix",
		Jobs:      "4",
		Version:   "1.0.0",
	})
	if err != nil {
		t.Fatalf("buildEnv: %v", err)
	}
	if cleanup == nil {
		t.Fatal("expected non-nil cleanup")
	}

	m := envToMap(env)
	home := m["HOME"]
	tmp := m["TMPDIR"]

	cleanup()

	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("HOME %q should be removed after cleanup, "+
			"got err: %v", home, err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("TMPDIR %q should be removed after cleanup, "+
			"got err: %v", tmp, err)
	}
}

// --- H2a: setDefault does not consult the host env ---

// TestSetDefaultDoesNotFallBackToHostEnv is the
// tightening that closes the silent inheritance leak.
// The previous implementation read os.Getenv(key) when
// the slice did not yet contain the key; that made the
// compiler flag set host-dependent. The new contract:
// setDefault appends the provided default unless the
// slice already has an entry. No host-env read.
func TestSetDefaultDoesNotFallBackToHostEnv(t *testing.T) {
	// Planting a host CFLAGS must not influence the
	// final env slice.
	t.Setenv("CFLAGS", "-march=native-from-host")

	var env []string
	setDefault(&env, "CFLAGS", "-O2-from-gale")

	m := envToMap(env)
	if m["CFLAGS"] != "-O2-from-gale" {
		t.Errorf("CFLAGS = %q, want %q (host env must not "+
			"leak through setDefault)", m["CFLAGS"],
			"-O2-from-gale")
	}
}

// TestBuildEnvCFLAGSIgnoresHostCFLAGS is the end-to-end
// version of the same invariant through buildEnv. A
// host CFLAGS=-march=native is ignored; the deterministic
// gale default wins.
func TestBuildEnvCFLAGSIgnoresHostCFLAGS(t *testing.T) {
	t.Setenv("CFLAGS", "-march=native")

	env, cleanup, err := buildEnv(&BuildContext{
		PrefixDir: "/tmp/prefix",
		Jobs:      "4",
		Version:   "1.0.0",
	})
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("buildEnv: %v", err)
	}
	m := envToMap(env)
	got := m["CFLAGS"]
	if strings.Contains(got, "-march=native") {
		t.Errorf("CFLAGS = %q leaks host CFLAGS", got)
	}
}

// --- H2b: CC/CXX pass-through closed ---

// TestBuildEnvDoesNotPassThroughHostCC asserts that a
// host CC (e.g., CC=gcc-11 on a dev laptop) does not
// reach the build. Recipes that need a specific compiler
// set CC=... inline in their step; the implicit
// pass-through was a non-determinism leak and no recipe
// in the catalog depends on it. See build.go near the
// compiler-flag construction for the rationale comment.
func TestBuildEnvDoesNotPassThroughHostCC(t *testing.T) {
	t.Setenv("CC", "/host/bin/gcc-11")
	t.Setenv("CXX", "/host/bin/g++-11")

	env, cleanup, err := buildEnv(&BuildContext{
		PrefixDir: "/tmp/prefix",
		Jobs:      "4",
		Version:   "1.0.0",
	})
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("buildEnv: %v", err)
	}
	m := envToMap(env)
	if got, ok := m["CC"]; ok {
		t.Errorf("CC = %q leaked through from host, want "+
			"unset (no toolchain)", got)
	}
	if got, ok := m["CXX"]; ok {
		t.Errorf("CXX = %q leaked through from host, want "+
			"unset (no toolchain)", got)
	}
}

// --- H3: SOURCE_DATE_EPOCH, deterministic touchAll ---

// TestSourceDateEpochFromReleasedAt parses the recipe's
// source.released_at field (YYYY-MM-DD, UTC) into a
// stable time.Time. This is the canonical input for
// touchAll when the recipe declares a release date.
func TestSourceDateEpochFromReleasedAt(t *testing.T) {
	r := &recipe.Recipe{
		Source: recipe.Source{ReleasedAt: "2024-12-15"},
	}
	got := sourceDateEpoch(r)
	want := time.Date(2024, 12, 15, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("sourceDateEpoch(%q) = %v, want %v",
			r.Source.ReleasedAt, got, want)
	}
}

// TestSourceDateEpochFallbackIsUnixZero falls back to
// the Unix epoch when no released_at is available. Any
// other sentinel (time.Now, build start) would reintroduce
// the determinism leak H3 closes.
func TestSourceDateEpochFallbackIsUnixZero(t *testing.T) {
	r := &recipe.Recipe{}
	got := sourceDateEpoch(r)
	want := time.Unix(0, 0).UTC()
	if !got.Equal(want) {
		t.Errorf("sourceDateEpoch(empty) = %v, want Unix 0 (%v)",
			got, want)
	}
}

// TestSourceDateEpochIgnoresMalformed guards against a
// malformed released_at silently falling back to
// time.Now(). A garbage date must fail closed to the
// Unix-epoch sentinel so the archive still hashes
// deterministically.
func TestSourceDateEpochIgnoresMalformed(t *testing.T) {
	r := &recipe.Recipe{
		Source: recipe.Source{ReleasedAt: "garbage"},
	}
	got := sourceDateEpoch(r)
	want := time.Unix(0, 0).UTC()
	if !got.Equal(want) {
		t.Errorf("sourceDateEpoch(garbage) = %v, want "+
			"Unix 0 (%v)", got, want)
	}
}

// TestTouchAllStampsFilesWithFixedTime asserts that
// touchAll applies the provided time uniformly to every
// file. The previous implementation used time.Now(),
// which leaked wall-clock into tarball mtimes.
func TestTouchAllStampsFilesWithFixedTime(t *testing.T) {
	dir := t.TempDir()
	paths := []string{"a.txt", "sub/b.txt"}
	for _, p := range paths {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	stamp := time.Date(2024, 12, 15, 0, 0, 0, 0, time.UTC)
	if err := touchAll(dir, stamp); err != nil {
		t.Fatalf("touchAll: %v", err)
	}

	for _, p := range paths {
		info, err := os.Stat(filepath.Join(dir, p))
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		// Compare at second precision; some filesystems
		// round mtime.
		got := info.ModTime().UTC().Truncate(time.Second)
		want := stamp.Truncate(time.Second)
		if !got.Equal(want) {
			t.Errorf("%s mtime = %v, want %v", p, got, want)
		}
	}
}

// TestBuildWithReleasedAtProducesIdenticalArchiveHash
// runs a full build twice against the same source and
// released_at and asserts the resulting archive hashes
// match. This is the end-to-end determinism invariant H3
// and H4 together guarantee: byte-identical inputs
// produce byte-identical output.
func TestBuildWithReleasedAtProducesIdenticalArchiveHash(t *testing.T) {
	tarball, hash := createSourceTarGz(t,
		map[string]string{
			"testpkg-1.0/README":        "hello\n",
			"testpkg-1.0/include/hdr.h": "int v;\n",
			"testpkg-1.0/src/lib.c":     "int v = 0;\n",
		},
	)
	srv := serveFile(t, tarball)

	newRecipe := func() *recipe.Recipe {
		return &recipe.Recipe{
			Package: recipe.Package{
				Name:    "testpkg",
				Version: "1.0",
			},
			Source: recipe.Source{
				URL:        srv.URL + "/testpkg-1.0.tar.gz",
				SHA256:     hash,
				ReleasedAt: "2024-12-15",
			},
			Build: recipe.Build{
				Steps: []string{
					"mkdir -p $PREFIX/share && " +
						"cp README $PREFIX/share/README",
				},
			},
		}
	}

	out1 := t.TempDir()
	res1, err := Build(newRecipe(), out1, false, nil)
	if err != nil {
		t.Fatalf("build 1: %v", err)
	}

	// Sleep-free — the bug was that time.Now() leaked
	// into mtimes regardless of wait. Run the two builds
	// back to back; if any wall-clock creeps in, SHA256s
	// still differ.
	out2 := t.TempDir()
	res2, err := Build(newRecipe(), out2, false, nil)
	if err != nil {
		t.Fatalf("build 2: %v", err)
	}

	if res1.SHA256 != res2.SHA256 {
		t.Errorf("archive SHA256 differs across runs: "+
			"\n  run 1: %s\n  run 2: %s",
			res1.SHA256, res2.SHA256)
	}
}

// --- H4: touchAll surfaces real errors ---

// TestTouchAllTolerantOfBrokenSymlinks confirms the
// fix does not regress the documented tolerance: source
// tarballs (especially test fixtures in upstream
// projects) routinely contain broken symlinks. Those
// must not fail the build.
func TestTouchAllTolerantOfBrokenSymlinks(t *testing.T) {
	dir := t.TempDir()

	real := filepath.Join(dir, "real.txt")
	if err := os.WriteFile(real, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Broken symlink — target does not exist.
	if err := os.Symlink(
		filepath.Join(dir, "does-not-exist"),
		filepath.Join(dir, "broken")); err != nil {
		t.Fatal(err)
	}

	stamp := time.Unix(1_700_000_000, 0).UTC()
	if err := touchAll(dir, stamp); err != nil {
		t.Errorf("touchAll should tolerate broken symlinks, "+
			"got: %v", err)
	}
}

// TestTouchAllPropagatesWalkErrors asserts the fix
// against the silent `return nil` on Walk errors. An
// unreadable directory (mode 0) produces a Walk error
// that is not ENOENT; that must surface.
func TestTouchAllPropagatesWalkErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses directory perms")
	}
	dir := t.TempDir()

	sub := filepath.Join(dir, "unreadable")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// Put a file inside so Walk has something to try to
	// visit.
	if err := os.WriteFile(
		filepath.Join(sub, "hidden"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Drop read+execute so the Walk descent fails with
	// EACCES, not ENOENT.
	if err := os.Chmod(sub, 0o000); err != nil {
		t.Fatal(err)
	}
	// Restore perms so t.TempDir cleanup works.
	t.Cleanup(func() {
		_ = os.Chmod(sub, 0o755)
	})

	stamp := time.Unix(1_700_000_000, 0).UTC()
	err := touchAll(dir, stamp)
	if err == nil {
		t.Error("touchAll should surface directory read " +
			"errors, got nil")
	}
}

// Test helpers are shared with build_test.go:
// createSourceTarGz, serveFile, envToMap.

// --- sccache: narrow opt-in passthrough for Rust caching ---

// fakeSccacheOnPATH plants an executable named "sccache" in
// a tempdir and sets PATH to that directory (plus /usr/bin
// and /bin so other tools resolve). Lets buildEnv/buildPath
// detect sccache via exec.LookPath without depending on the
// host environment.
func fakeSccacheOnPATH(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "sccache")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake sccache: %v", err)
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
}

// TestBuildEnvNoSccacheLeavesWrapperUnset: when sccache is
// not on the host PATH, the sandbox must not advertise it.
// Setting RUSTC_WRAPPER=sccache for a wrapper that won't
// resolve makes every rustc invocation fail with a confusing
// "sccache: command not found".
func TestBuildEnvNoSccacheLeavesWrapperUnset(t *testing.T) {
	// PATH without sccache anywhere.
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("SCCACHE_GHA_ENABLED", "")

	env, cleanup, err := buildEnv(&BuildContext{
		PrefixDir: "/tmp/prefix",
		Jobs:      "4",
		Version:   "1.0.0",
	})
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("buildEnv: %v", err)
	}
	m := envToMap(env)

	if got, ok := m["RUSTC_WRAPPER"]; ok {
		t.Errorf("RUSTC_WRAPPER = %q with sccache absent, want unset", got)
	}
	for k := range m {
		if strings.HasPrefix(k, "SCCACHE_") {
			t.Errorf("env leaked %s=%q with sccache absent", k, m[k])
		}
	}
}

// TestBuildEnvSccachePresentSetsWrapper: detecting sccache
// on host PATH flips two things on simultaneously — the
// RUSTC_WRAPPER env var so cargo/rustc shim through it, and
// the sandbox PATH so the wrapper binary actually resolves.
// Either alone is broken; tested together.
func TestBuildEnvSccachePresentSetsWrapper(t *testing.T) {
	fakeSccacheOnPATH(t)

	env, cleanup, err := buildEnv(&BuildContext{
		PrefixDir: "/tmp/prefix",
		Jobs:      "4",
		Version:   "1.0.0",
	})
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("buildEnv: %v", err)
	}
	m := envToMap(env)

	if got := m["RUSTC_WRAPPER"]; got != "sccache" {
		t.Errorf("RUSTC_WRAPPER = %q, want %q", got, "sccache")
	}

	// The sandbox PATH must resolve sccache. buildPath puts
	// resolved tools into a symlink directory; walking the
	// PATH and checking that <dir>/sccache exists is the
	// invariant a build step would rely on.
	if !pathHasExecutable(m["PATH"], "sccache") {
		t.Errorf("sandbox PATH=%q does not resolve sccache", m["PATH"])
	}
}

// TestBuildEnvPassesGitHubActionsCacheVars: when sccache is
// the active wrapper, the GitHub Actions cache backend needs
// a handful of runner-provided env vars to reach the build
// step. Only forward them when sccache is the wrapper —
// passing GHA tokens into a vanilla local build is needless
// surface area.
func TestBuildEnvPassesGitHubActionsCacheVars(t *testing.T) {
	fakeSccacheOnPATH(t)
	t.Setenv("SCCACHE_GHA_ENABLED", "true")
	t.Setenv("ACTIONS_CACHE_URL", "https://example/cache")
	t.Setenv("ACTIONS_RUNTIME_TOKEN", "secret-token")
	t.Setenv("ACTIONS_RESULTS_URL", "https://example/results")
	t.Setenv("ACTIONS_CACHE_SERVICE_V2", "true")

	env, cleanup, err := buildEnv(&BuildContext{
		PrefixDir: "/tmp/prefix",
		Jobs:      "4",
		Version:   "1.0.0",
	})
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("buildEnv: %v", err)
	}
	m := envToMap(env)

	want := map[string]string{
		"SCCACHE_GHA_ENABLED":      "true",
		"ACTIONS_CACHE_URL":        "https://example/cache",
		"ACTIONS_RUNTIME_TOKEN":    "secret-token",
		"ACTIONS_RESULTS_URL":      "https://example/results",
		"ACTIONS_CACHE_SERVICE_V2": "true",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %q, want %q", k, m[k], v)
		}
	}
}

// TestBuildEnvForwardsArbitrarySccachePrefixVars: any
// SCCACHE_* host var is forwarded when sccache is the
// active wrapper. Covers SCCACHE_DIR, SCCACHE_REDIS, and
// future keys without needing a fixed allowlist.
func TestBuildEnvForwardsArbitrarySccachePrefixVars(t *testing.T) {
	fakeSccacheOnPATH(t)
	t.Setenv("SCCACHE_DIR", "/tmp/sccache-dir")
	t.Setenv("SCCACHE_BUCKET", "my-s3-bucket")

	env, cleanup, err := buildEnv(&BuildContext{
		PrefixDir: "/tmp/prefix",
		Jobs:      "4",
		Version:   "1.0.0",
	})
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("buildEnv: %v", err)
	}
	m := envToMap(env)

	if got := m["SCCACHE_DIR"]; got != "/tmp/sccache-dir" {
		t.Errorf("SCCACHE_DIR = %q, want %q", got, "/tmp/sccache-dir")
	}
	if got := m["SCCACHE_BUCKET"]; got != "my-s3-bucket" {
		t.Errorf("SCCACHE_BUCKET = %q, want %q", got, "my-s3-bucket")
	}
}

// TestBuildEnvSccacheConfigNotPassedWhenWrapperAbsent:
// passing sccache configuration without the wrapper itself
// is worse than passing neither — cargo would have no idea
// what to do with SCCACHE_GHA_ENABLED. The trigger is
// strictly the presence of the sccache binary on host PATH.
func TestBuildEnvSccacheConfigNotPassedWhenWrapperAbsent(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("SCCACHE_GHA_ENABLED", "true")
	t.Setenv("ACTIONS_CACHE_URL", "https://example/cache")
	t.Setenv("ACTIONS_RUNTIME_TOKEN", "secret-token")

	env, cleanup, err := buildEnv(&BuildContext{
		PrefixDir: "/tmp/prefix",
		Jobs:      "4",
		Version:   "1.0.0",
	})
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("buildEnv: %v", err)
	}
	m := envToMap(env)

	if _, ok := m["RUSTC_WRAPPER"]; ok {
		t.Errorf("RUSTC_WRAPPER set with sccache absent")
	}
	for _, k := range []string{
		"SCCACHE_GHA_ENABLED",
		"ACTIONS_CACHE_URL",
		"ACTIONS_RUNTIME_TOKEN",
	} {
		if got, ok := m[k]; ok {
			t.Errorf("%s = %q forwarded without sccache wrapper", k, got)
		}
	}
}

// TestBuildEnvRecipeRustcWrapperWinsOverAutoSet: a recipe
// that explicitly sets RUSTC_WRAPPER (e.g. to an alternative
// wrapper or empty string for opt-out) must override the
// auto-set. Same precedence rule as every other recipe-
// declared env key: [build] env wins.
func TestBuildEnvRecipeRustcWrapperWinsOverAutoSet(t *testing.T) {
	fakeSccacheOnPATH(t)

	env, cleanup, err := buildEnv(&BuildContext{
		PrefixDir: "/tmp/prefix",
		Jobs:      "4",
		Version:   "1.0.0",
		Env:       map[string]string{"RUSTC_WRAPPER": "/opt/custom-wrapper"},
	})
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("buildEnv: %v", err)
	}
	m := envToMap(env)

	if got := m["RUSTC_WRAPPER"]; got != "/opt/custom-wrapper" {
		t.Errorf("RUSTC_WRAPPER = %q, want recipe value %q",
			got, "/opt/custom-wrapper")
	}
}

// pathHasExecutable returns true if path (a colon-separated
// PATH string) contains a directory holding an executable
// named name. Used to assert the sandbox PATH resolves
// build tools.
func pathHasExecutable(path, name string) bool {
	for _, dir := range strings.Split(path, ":") {
		if dir == "" {
			continue
		}
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		if info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	return false
}
