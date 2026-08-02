package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/projects"
)

// writeScopeLock puts a v1 lock naming one identity at one hash into a
// scope, so the veto scan has something to disagree with.
func writeScopeLock(t *testing.T, path, id, sha string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.WriteV1(path, &lockfile.V1{
		Version: lockfile.SchemaVersion,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{Roots: []string{id}},
		},
		Packages: map[string]lockfile.Package{
			id: {Artifacts: map[string]lockfile.Artifact{
				testPlatform: {SHA256: sha, Method: "binary"},
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

const (
	// One platform throughout: the veto is per-platform, and varying
	// it here would test lockfile's lookup rather than the veto.
	testPlatform = "darwin/arm64"
	// One package throughout; the veto keys on identity, and varying
	// the name would test the map rather than the rule.
	testPkg = "jq"

	shaX = "1111111111111111111111111111111111111111111111111111111111111111"
	shaY = "2222222222222222222222222222222222222222222222222222222222222222"
)

// Design §13's central scope limit. Replacing an unprovenanced store
// directory is convergence on the one supported state only while no
// other scope disagrees about what that state is. A second project
// whose lock names a different hash for the same identity turns the
// replacement from a migration into a conflict.
func TestCheckReplaceableRefusesWhenAnotherScopeNamesAnotherHash(t *testing.T) {
	home := t.TempDir()
	storeRoot := filepath.Join(home, "pkg")
	proj := t.TempDir()
	if err := projects.Register(home, proj); err != nil {
		t.Fatal(err)
	}
	writeScopeLock(t, filepath.Join(proj, "gale.lock"),
		"jq@1.7-1", shaY)

	err := checkReplaceable(replaceQuery{
		galeHome:    home,
		storeRoot:   storeRoot,
		selfGaleDir: filepath.Join(t.TempDir(), ".gale"),
		name:        "jq",
		version:     "1.7-1",
		targetDir:   filepath.Join(storeRoot, "jq", "1.7-1"),
		wantSHA:     shaX,
		platform:    testPlatform,
	})
	if err == nil {
		t.Fatal("another scope names a different hash: want a conflict")
	}
	if !errors.Is(err, errScopeDisagrees) {
		t.Errorf("want errScopeDisagrees, got %v", err)
	}
	// The message has to name the scope: the user has to go and look
	// at that project to decide which hash is right.
	if !strings.Contains(err.Error(), proj) {
		t.Errorf("conflict must name the disagreeing scope, got %q", err)
	}
}

// The global scope keeps its veto in full, and §13 calls it out
// specifically: global has no activation gate, so a global lock naming
// a hash that a project replaced underneath stays silently violated
// until someone runs a global sync, which may be never.
func TestCheckReplaceableRefusesWhenTheGlobalLockDisagrees(t *testing.T) {
	home := t.TempDir()
	storeRoot := filepath.Join(home, "pkg")
	writeScopeLock(t, filepath.Join(home, "gale.lock"),
		"jq@1.7-1", shaY)

	err := checkReplaceable(replaceQuery{
		galeHome:    home,
		storeRoot:   storeRoot,
		selfGaleDir: filepath.Join(t.TempDir(), ".gale"),
		name:        "jq",
		version:     "1.7-1",
		targetDir:   filepath.Join(storeRoot, "jq", "1.7-1"),
		wantSHA:     shaX,
		platform:    testPlatform,
	})
	if !errors.Is(err, errScopeDisagrees) {
		t.Errorf("the global lock must veto too, got %v", err)
	}
}

// The initiating scope is exempt, evaluated against the lock it is
// about to write rather than the one on disk. `gale lock --refresh`
// exists precisely to move an identity from one hash to another, so
// counting its own current value would reject every refresh as a
// conflict with itself.
func TestCheckReplaceableExemptsTheInitiatingScope(t *testing.T) {
	home := t.TempDir()
	storeRoot := filepath.Join(home, "pkg")
	proj := t.TempDir()
	if err := projects.Register(home, proj); err != nil {
		t.Fatal(err)
	}
	writeScopeLock(t, filepath.Join(proj, "gale.lock"),
		"jq@1.7-1", shaY)

	if err := checkReplaceable(replaceQuery{
		galeHome:    home,
		storeRoot:   storeRoot,
		selfGaleDir: filepath.Join(proj, ".gale"),
		name:        "jq",
		version:     "1.7-1",
		targetDir:   filepath.Join(storeRoot, "jq", "1.7-1"),
		wantSHA:     shaX,
		platform:    testPlatform,
	}); err != nil {
		t.Errorf("the initiating scope must not veto itself: %v", err)
	}
}

// Agreement is not a conflict: a scope naming the SAME hash has
// nothing to object to, and refusing there would make a shared
// dependency unmigratable on any machine with two projects.
func TestCheckReplaceableAllowsWhenEveryScopeAgrees(t *testing.T) {
	home := t.TempDir()
	storeRoot := filepath.Join(home, "pkg")
	proj := t.TempDir()
	if err := projects.Register(home, proj); err != nil {
		t.Fatal(err)
	}
	writeScopeLock(t, filepath.Join(proj, "gale.lock"),
		"jq@1.7-1", shaX)

	if err := checkReplaceable(replaceQuery{
		galeHome:    home,
		storeRoot:   storeRoot,
		selfGaleDir: filepath.Join(t.TempDir(), ".gale"),
		name:        "jq",
		version:     "1.7-1",
		targetDir:   filepath.Join(storeRoot, "jq", "1.7-1"),
		wantSHA:     shaX,
		platform:    testPlatform,
	}); err != nil {
		t.Errorf("every scope agrees, so replacement is allowed: %v", err)
	}
}

// A scope that does not reference the identity at all has no opinion.
// This is the ordinary case on any machine with more than one project.
func TestCheckReplaceableIgnoresAnUnrelatedScope(t *testing.T) {
	home := t.TempDir()
	storeRoot := filepath.Join(home, "pkg")
	proj := t.TempDir()
	if err := projects.Register(home, proj); err != nil {
		t.Fatal(err)
	}
	writeScopeLock(t, filepath.Join(proj, "gale.lock"),
		"ripgrep@14.1.0-1", shaY)

	if err := checkReplaceable(replaceQuery{
		galeHome:    home,
		storeRoot:   storeRoot,
		selfGaleDir: filepath.Join(t.TempDir(), ".gale"),
		name:        "jq",
		version:     "1.7-1",
		targetDir:   filepath.Join(storeRoot, "jq", "1.7-1"),
		wantSHA:     shaX,
		platform:    testPlatform,
	}); err != nil {
		t.Errorf("an unrelated scope has no opinion: %v", err)
	}
}

// "A registered project that is known but unreadable fails closed."
// A corrupt lock is exactly that: gale knows the scope exists and
// cannot tell what it requires, so it must not proceed to destroy
// bytes that scope may depend on.
func TestCheckReplaceableFailsClosedOnAnUnreadableLock(t *testing.T) {
	home := t.TempDir()
	storeRoot := filepath.Join(home, "pkg")
	proj := t.TempDir()
	if err := projects.Register(home, proj); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "gale.lock"),
		[]byte("this is not toml {{{"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := checkReplaceable(replaceQuery{
		galeHome:    home,
		storeRoot:   storeRoot,
		selfGaleDir: filepath.Join(t.TempDir(), ".gale"),
		name:        "jq",
		version:     "1.7-1",
		targetDir:   filepath.Join(storeRoot, "jq", "1.7-1"),
		wantSHA:     shaX,
		platform:    testPlatform,
	})
	if err == nil {
		t.Fatal("an unreadable scope lock must fail closed")
	}
	if !strings.Contains(err.Error(), proj) {
		t.Errorf("must name the scope it could not read, got %q", err)
	}
}

// An incomplete lock must fail closed, not read as consent. A
// document that roots or depends on an identity while omitting its
// node parses cleanly, and a lookup that only checks Packages reports
// "not referenced" — which for this scan means "go ahead and destroy
// the directory", justified by the other scope's own defect.
func TestCheckReplaceableFailsClosedOnAnIncompleteLock(t *testing.T) {
	home := t.TempDir()
	storeRoot := filepath.Join(home, "pkg")
	proj := t.TempDir()
	if err := projects.Register(home, proj); err != nil {
		t.Fatal(err)
	}
	// Roots the identity, defines no node for it.
	if err := lockfile.WriteV1(filepath.Join(proj, "gale.lock"), &lockfile.V1{
		Version: lockfile.SchemaVersion,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{Roots: []string{"jq@1.7-1"}},
		},
		Packages: map[string]lockfile.Package{},
	}); err != nil {
		t.Fatal(err)
	}

	err := checkReplaceable(replaceQuery{
		galeHome:    home,
		storeRoot:   storeRoot,
		selfGaleDir: filepath.Join(t.TempDir(), ".gale"),
		name:        "jq",
		version:     "1.7-1",
		targetDir:   filepath.Join(storeRoot, "jq", "1.7-1"),
		wantSHA:     shaX,
		platform:    testPlatform,
	})
	if err == nil {
		t.Fatal("an incomplete lock must fail closed, not approve")
	}
	if !errors.Is(err, lockfile.ErrMissingNode) {
		t.Errorf("want ErrMissingNode in the chain, got %v", err)
	}
}

// writeLegacyScopeLock puts a pre-enforcement lock into a scope. The
// flat schema is keyed by package name and records one SHA with no
// platform dimension. That SHA is read as a conservative, platformless
// byte claim rather than as this machine's artifact: a project
// gale.lock travels in git, so the hash may have been written
// elsewhere. Over-refusing beats destroying bytes another scope names.
func writeLegacyScopeLock(t *testing.T, path, version, sha string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Write(path, &lockfile.LockFile{
		Packages: map[string]lockfile.LockedPackage{
			testPkg: {Version: version, SHA256: sha},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

// A legacy lock names bytes, and a replacement that disagrees with
// them is the substitution this whole feature exists to detect.
// Skipping legacy scopes discarded that evidence because the witness
// speaks an older dialect.
//
// It cannot be a blanket fail-closed either: on upgrade day EVERY
// scope holds a legacy lock, and `gale lock` cannot mint a v1 one
// first because every pre-upgrade directory is occupied and
// unprovenanced, so lockRoot returns the error naming --refresh and
// migrate as the remedy. Blanket fail-closed deadlocks the upgrade it
// exists to make possible.
func TestCheckReplaceableRefusesWhenALegacyLockNamesOtherBytes(t *testing.T) {
	home := t.TempDir()
	storeRoot := filepath.Join(home, "pkg")
	proj := t.TempDir()
	if err := projects.Register(home, proj); err != nil {
		t.Fatal(err)
	}
	writeLegacyScopeLock(t, filepath.Join(proj, "gale.lock"),
		"1.7-1", shaY)

	err := checkReplaceable(replaceQuery{
		galeHome:    home,
		storeRoot:   storeRoot,
		selfGaleDir: filepath.Join(t.TempDir(), ".gale"),
		name:        "jq",
		version:     "1.7-1",
		targetDir:   filepath.Join(storeRoot, "jq", "1.7-1"),
		wantSHA:     shaX,
		platform:    testPlatform,
	})
	if !errors.Is(err, errScopeDisagrees) {
		t.Errorf("a legacy lock naming other bytes must veto, got %v", err)
	}
}

// The ordinary upgrade-day case: legacy sync recorded the SHA of the
// same registry artifact migrate refetches, so they agree and the
// migration proceeds. If this vetoed, upgrade day would be blocked
// for everyone.
func TestCheckReplaceableAllowsWhenALegacyLockAgrees(t *testing.T) {
	home := t.TempDir()
	storeRoot := filepath.Join(home, "pkg")
	proj := t.TempDir()
	if err := projects.Register(home, proj); err != nil {
		t.Fatal(err)
	}
	writeLegacyScopeLock(t, filepath.Join(proj, "gale.lock"),
		"1.7-1", shaX)

	if err := checkReplaceable(replaceQuery{
		galeHome:    home,
		storeRoot:   storeRoot,
		selfGaleDir: filepath.Join(t.TempDir(), ".gale"),
		name:        "jq",
		version:     "1.7-1",
		targetDir:   filepath.Join(storeRoot, "jq", "1.7-1"),
		wantSHA:     shaX,
		platform:    testPlatform,
	}); err != nil {
		t.Errorf("an agreeing legacy lock must not veto: %v", err)
	}
}

// Legacy locks spell the version bare where a v1 identity is
// canonical, so "1.7" and "1.7-1" are the same package and the veto
// has to see that. More matches means more chances to veto, which is
// the right direction for an operation that destroys bytes.
func TestCheckReplaceableMatchesABareLegacyVersion(t *testing.T) {
	home := t.TempDir()
	storeRoot := filepath.Join(home, "pkg")
	proj := t.TempDir()
	if err := projects.Register(home, proj); err != nil {
		t.Fatal(err)
	}
	writeLegacyScopeLock(t, filepath.Join(proj, "gale.lock"),
		"1.7", shaY)

	if err := checkReplaceable(replaceQuery{
		galeHome:    home,
		storeRoot:   storeRoot,
		selfGaleDir: filepath.Join(t.TempDir(), ".gale"),
		name:        "jq",
		version:     "1.7-1",
		targetDir:   filepath.Join(storeRoot, "jq", "1.7-1"),
		wantSHA:     shaX,
		platform:    testPlatform,
	}); !errors.Is(err, errScopeDisagrees) {
		t.Errorf("a bare legacy version names the same package, got %v", err)
	}
}

// A legacy entry for another version of the same package is about a
// different store directory and says nothing about this one.
func TestCheckReplaceableIgnoresALegacyEntryForAnotherVersion(t *testing.T) {
	home := t.TempDir()
	storeRoot := filepath.Join(home, "pkg")
	proj := t.TempDir()
	if err := projects.Register(home, proj); err != nil {
		t.Fatal(err)
	}
	writeLegacyScopeLock(t, filepath.Join(proj, "gale.lock"),
		"1.6-1", shaY)

	if err := checkReplaceable(replaceQuery{
		galeHome:    home,
		storeRoot:   storeRoot,
		selfGaleDir: filepath.Join(t.TempDir(), ".gale"),
		name:        "jq",
		version:     "1.7-1",
		targetDir:   filepath.Join(storeRoot, "jq", "1.7-1"),
		wantSHA:     shaX,
		platform:    testPlatform,
	}); err != nil {
		t.Errorf("another version is a different directory: %v", err)
	}
}

// An empty SHA names no bytes, so it cannot disagree with any. Legacy
// entries for source builds carry one.
func TestCheckReplaceableIgnoresALegacyEntryWithNoSHA(t *testing.T) {
	home := t.TempDir()
	storeRoot := filepath.Join(home, "pkg")
	proj := t.TempDir()
	if err := projects.Register(home, proj); err != nil {
		t.Fatal(err)
	}
	writeLegacyScopeLock(t, filepath.Join(proj, "gale.lock"),
		"1.7-1", "")

	if err := checkReplaceable(replaceQuery{
		galeHome:    home,
		storeRoot:   storeRoot,
		selfGaleDir: filepath.Join(t.TempDir(), ".gale"),
		name:        "jq",
		version:     "1.7-1",
		targetDir:   filepath.Join(storeRoot, "jq", "1.7-1"),
		wantSHA:     shaX,
		platform:    testPlatform,
	}); err != nil {
		t.Errorf("an empty SHA names no bytes: %v", err)
	}
}

// seedScopeClosure gives a scope an active generation linking one
// package, so the closure scan has something to find.
type scopePkg struct {
	galeDir, storeRoot, name, version string
	// withMeta writes an explicitly empty .gale-deps.toml, which is a
	// recorded closure. Omitting it leaves the closure unknown, which
	// is a different state and the one the incomplete-closure veto
	// keys on.
	withMeta bool
}

func seedScopeClosure(t *testing.T, p scopePkg) {
	t.Helper()
	galeDir, storeRoot := p.galeDir, p.storeRoot
	name, version, withMeta := p.name, p.version, p.withMeta
	dir := filepath.Join(storeRoot, name, version)
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "bin", name), []byte("#!/bin/sh\n"), 0o755,
	); err != nil {
		t.Fatal(err)
	}
	if withMeta {
		if err := depsmeta.Write(dir, depsmeta.Metadata{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := generation.Build(
		map[string]string{name: version}, galeDir, storeRoot,
	); err != nil {
		t.Fatal(err)
	}
}

// The closure-based vetoes, which cover what a lock cannot state. A
// legacy lock records roots only, so a transitive dependency carries
// no hash while the scope still loads the directory through its active
// generation.
func TestCheckReplaceableClosureVetoes(t *testing.T) {
	tests := []struct {
		name string
		// linked is the package the scope's generation links, and
		// withMeta whether that directory recorded its own closure.
		linked, linkedVer string
		withMeta          bool
		// legacyVer and legacySHA are the scope's lock entry, if any.
		legacyVer, legacySHA string
		why                  string
	}{
		{
			name: "known reference with unknown bytes",
			// The scope loads jq itself and its lock is about something
			// else entirely, so nothing records which bytes it needs.
			linked: "jq", linkedVer: "1.7-1", withMeta: true,
			legacyVer: "9.9-1", legacySHA: shaY,
			why: "the scope loads the directory and names no hash for it",
		},
		{
			name: "closure unreadable",
			// ripgrep recorded nothing, so what it depends on is
			// unknown — possibly the jq being replaced.
			linked: "ripgrep", linkedVer: "14.1.0-1", withMeta: false,
			why: "an unreadable closure cannot prove the scope does not load it",
		},
		{
			name: "agreeing hash does not bypass an unreadable closure",
			// The two answer different questions: the hash says which
			// bytes this scope needs of THIS identity, completeness
			// whether gale could read what else it loads.
			linked: "ripgrep", linkedVer: "14.1.0-1", withMeta: false,
			legacyVer: "1.7-1", legacySHA: shaX,
			why: "completeness is not settled by a target-specific claim",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			storeRoot := filepath.Join(home, "pkg")
			proj := t.TempDir()
			if err := projects.Register(home, proj); err != nil {
				t.Fatal(err)
			}
			seedScopeClosure(t, scopePkg{
				galeDir: filepath.Join(proj, ".gale"), storeRoot: storeRoot,
				name: tt.linked, version: tt.linkedVer, withMeta: tt.withMeta,
			})
			if tt.legacySHA != "" {
				writeLegacyScopeLock(t, filepath.Join(proj, "gale.lock"),
					tt.legacyVer, tt.legacySHA)
			}

			err := checkReplaceable(replaceQuery{
				galeHome:    home,
				storeRoot:   storeRoot,
				selfGaleDir: filepath.Join(t.TempDir(), ".gale"),
				name:        "jq",
				version:     "1.7-1",
				targetDir:   filepath.Join(storeRoot, "jq", "1.7-1"),
				wantSHA:     shaX,
				platform:    testPlatform,
			})
			if !errors.Is(err, errScopeDisagrees) {
				t.Errorf("%s: got %v", tt.why, err)
			}
		})
	}
}

// A scope with a complete closure that does not reach the directory
// has no opinion. Without this the veto would refuse everything on any
// machine with a second project, which is the deadlock the machine-
// wide migrate escape exists to avoid needing.
func TestCheckReplaceableAllowsAScopeThatDoesNotReachTheDir(t *testing.T) {
	home := t.TempDir()
	storeRoot := filepath.Join(home, "pkg")
	proj := t.TempDir()
	if err := projects.Register(home, proj); err != nil {
		t.Fatal(err)
	}
	seedScopeClosure(t, scopePkg{
		galeDir: filepath.Join(proj, ".gale"), storeRoot: storeRoot,
		name: "ripgrep", version: "14.1.0-1", withMeta: true,
	})

	if err := checkReplaceable(replaceQuery{
		galeHome:    home,
		storeRoot:   storeRoot,
		selfGaleDir: filepath.Join(t.TempDir(), ".gale"),
		name:        "jq",
		version:     "1.7-1",
		targetDir:   filepath.Join(storeRoot, "jq", "1.7-1"),
		wantSHA:     shaX,
		platform:    testPlatform,
	}); err != nil {
		t.Errorf("a complete closure that does not reach jq has no "+
			"opinion: %v", err)
	}
}

// An explicit agreeing hash settles the TARGET-SPECIFIC question: this
// scope has said which bytes it needs of this identity and they are
// the bytes being installed, so the known-reference veto does not
// fire. Its closure is complete here, so nothing else objects either.
func TestCheckReplaceableAgreeingHashSettlesTheTargetQuestion(t *testing.T) {
	home := t.TempDir()
	storeRoot := filepath.Join(home, "pkg")
	proj := t.TempDir()
	if err := projects.Register(home, proj); err != nil {
		t.Fatal(err)
	}
	seedScopeClosure(t, scopePkg{
		galeDir: filepath.Join(proj, ".gale"), storeRoot: storeRoot,
		name: "jq", version: "1.7-1", withMeta: true,
	})
	writeLegacyScopeLock(t, filepath.Join(proj, "gale.lock"), "1.7-1", shaX)

	if err := checkReplaceable(replaceQuery{
		galeHome:    home,
		storeRoot:   storeRoot,
		selfGaleDir: filepath.Join(t.TempDir(), ".gale"),
		name:        "jq",
		version:     "1.7-1",
		targetDir:   filepath.Join(storeRoot, "jq", "1.7-1"),
		wantSHA:     shaX,
		platform:    testPlatform,
	}); err != nil {
		t.Errorf("an agreeing hash settles it: %v", err)
	}
}

// A coherent v1 lock states its own closure, so the metadata-based
// completeness rule does not apply to it. Revision 7 scopes that rule
// to a legacy scope precisely because a legacy lock records roots
// only.
//
// Applying it to v1 too is not merely over-strict, it is unrepairable:
// a valid v1 package may legitimately carry no .gale-deps.toml (the
// staging path treats absence as a complete zero-dependency answer),
// so such a scope would veto every replacement on the machine forever,
// and `gale migrate` cannot fix it because the directory is already
// provenanced.
func TestCheckReplaceableV1ScopeDoesNotVetoOnMissingMetadata(t *testing.T) {
	home := t.TempDir()
	storeRoot := filepath.Join(home, "pkg")
	proj := t.TempDir()
	if err := projects.Register(home, proj); err != nil {
		t.Fatal(err)
	}
	// The scope links a package that recorded no dependency metadata,
	// and holds a coherent v1 lock that does not mention jq.
	seedScopeClosure(t, scopePkg{
		galeDir: filepath.Join(proj, ".gale"), storeRoot: storeRoot,
		name: "ripgrep", version: "14.1.0-1", withMeta: false,
	})
	writeScopeLock(t, filepath.Join(proj, "gale.lock"),
		"ripgrep@14.1.0-1", shaY)

	if err := checkReplaceable(replaceQuery{
		galeHome:    home,
		storeRoot:   storeRoot,
		selfGaleDir: filepath.Join(t.TempDir(), ".gale"),
		name:        "jq",
		version:     "1.7-1",
		targetDir:   filepath.Join(storeRoot, "jq", "1.7-1"),
		wantSHA:     shaX,
		platform:    testPlatform,
	}); err != nil {
		t.Errorf("a coherent v1 lock states its own closure, so missing "+
			"dependency metadata must not veto: %v", err)
	}
}

// A legacy scope's veto must name the whole sequence that clears it,
// not just the first step.
//
// Design §13 is explicit that `gale migrate` does not finish the job
// on its own: it replaces unprovenanced BINARY directories, so a
// scope with source-built packages is still legacy afterwards, still
// vetoing, and the user has been sent to a command that appears to
// have worked. The remaining steps are rebuilding the source-method
// packages migrate lists, running plain `gale lock` in every scope
// that is still legacy, and only then retrying `--refresh`.
//
// Both closure vetoes carry it, because both are the same condition:
// a scope gale cannot prove is uninvolved because it predates
// provenance.
func TestLegacyScopeRefusalNamesTheWholePostMigrateSequence(t *testing.T) {
	for _, tt := range []struct {
		name     string
		withMeta bool
	}{
		{"known reference with unknown bytes", true},
		{"closure unreadable", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			storeRoot := filepath.Join(home, "pkg")
			proj := t.TempDir()
			if err := projects.Register(home, proj); err != nil {
				t.Fatal(err)
			}
			seedScopeClosure(t, scopePkg{
				galeDir: filepath.Join(proj, ".gale"), storeRoot: storeRoot,
				name: "jq", version: "1.7-1", withMeta: tt.withMeta,
			})

			err := checkReplaceable(replaceQuery{
				galeHome:    home,
				storeRoot:   storeRoot,
				selfGaleDir: filepath.Join(t.TempDir(), ".gale"),
				name:        "jq",
				version:     "1.7-1",
				targetDir:   filepath.Join(storeRoot, "jq", "1.7-1"),
				wantSHA:     shaX,
				platform:    testPlatform,
			})
			if !errors.Is(err, errScopeDisagrees) {
				t.Fatalf("err = %v, want errScopeDisagrees", err)
			}
			// In order, because the order is the instruction: locking a
			// scope before its source packages are rebuilt locks nothing.
			at := 0
			for _, step := range []string{
				"gale migrate", "rebuild", "gale lock", "--refresh",
			} {
				i := strings.Index(err.Error()[at:], step)
				if i < 0 {
					t.Fatalf("the refusal omits %q or states it out of "+
						"order: %q", step, err)
				}
				at += i + len(step)
			}
		})
	}
}
