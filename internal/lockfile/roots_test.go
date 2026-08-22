package lockfile

import (
	"errors"
	"strings"
	"testing"
)

// rootsOf builds a lock carrying only targets. Every test here is
// about the root set, and none of them reads a package node.
func rootsOf(def []string, host map[string][]string) *V1 {
	lf := &V1{Version: SchemaVersion, Packages: map[string]Package{}}
	if def != nil {
		lf.Targets.Default = &Target{Roots: def}
	}
	if host != nil {
		lf.Targets.Host = make(map[string]Target, len(host))
		for k, roots := range host {
			lf.Targets.Host[k] = Target{Roots: roots}
		}
	}
	return lf
}

// TestEffectiveRootsPrecedence pins that the lock reuses gale.toml's
// selector precedence rather than defining its own, and that a more
// specific selector replaces a pin instead of adding a second one.
func TestEffectiveRootsPrecedence(t *testing.T) {
	lf := rootsOf(
		[]string{"a@1.0-1", "b@1.0-1"},
		map[string][]string{
			"work-*":  {"a@2.0-1"},
			"work-mb": {"a@3.0-1"},
			"other":   {"b@9.0-1"},
		},
	)

	tests := []struct {
		host string
		want map[string]string
	}{
		{
			// No host: overlays are not consulted at all, which is what
			// lets a lock be read for a platform or machine other than
			// the one in hand.
			host: "",
			want: map[string]string{"a": "a@1.0-1", "b": "b@1.0-1"},
		},
		{
			host: "work-laptop",
			want: map[string]string{"a": "a@2.0-1", "b": "b@1.0-1"},
		},
		{
			// Both selectors match; the exact host is more specific.
			host: "work-mb",
			want: map[string]string{"a": "a@3.0-1", "b": "b@1.0-1"},
		},
		{
			host: "other",
			want: map[string]string{"a": "a@1.0-1", "b": "b@9.0-1"},
		},
	}

	for _, tt := range tests {
		t.Run("host="+tt.host, func(t *testing.T) {
			got, err := lf.EffectiveRoots(tt.host)
			if err != nil {
				t.Fatalf("EffectiveRoots: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("roots = %v, want %v", got, tt.want)
			}
			for name, id := range tt.want {
				if got[name] != id {
					t.Errorf("roots[%q] = %q, want %q", name, got[name], id)
				}
			}
		})
	}
}

// TestEffectiveRootsRejects covers the two malformed shapes. A
// duplicate inside one roots list is separated from replacement across
// selectors: replacement is the overlay's purpose, while list order
// deciding a winner is a lock that cannot be modeled.
func TestEffectiveRootsRejects(t *testing.T) {
	tests := []struct {
		name    string
		lf      *V1
		host    string
		wantErr error
		names   []string
	}{
		{
			name:    "two identities in one roots list",
			lf:      rootsOf([]string{"a@1.0-1", "a@2.0-1"}, nil),
			wantErr: ErrVersionConflict,
			names:   []string{"a@1.0-1", "a@2.0-1"},
		},
		{
			name:    "duplicate inside a host overlay",
			lf:      rootsOf(nil, map[string][]string{"mb": {"a@1.0-1", "a@2.0-1"}}),
			host:    "mb",
			wantErr: ErrVersionConflict,
		},
		{
			name:    "bare name is not an identity",
			lf:      rootsOf([]string{"a"}, nil),
			wantErr: ErrMalformedRoot,
		},
		{
			name:    "no revision suffix",
			lf:      rootsOf([]string{"a@1.0"}, nil),
			wantErr: ErrMalformedRoot,
		},
		{
			// The name is joined onto a --recipes directory downstream,
			// so a name that is itself a path must never get that far.
			name:    "name is a path",
			lf:      rootsOf([]string{"../../outside@1.0-1"}, nil),
			wantErr: ErrMalformedRoot,
		},
		{
			name:    "malformed identity in an overlay",
			lf:      rootsOf(nil, map[string][]string{"*": {"a@1.0"}}),
			host:    "anything",
			wantErr: ErrMalformedRoot,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.lf.EffectiveRoots(tt.host)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			for _, want := range tt.names {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error does not name %s: %v", want, err)
				}
			}
		})
	}
}

// TestEffectiveRootsRepeatedIdentityIsNotAConflict pins that the same
// identity listed twice is harmless. Only two *different* identities
// for one name make list order decide anything.
func TestEffectiveRootsRepeatedIdentityIsNotAConflict(t *testing.T) {
	got, err := rootsOf([]string{"a@1.0-1", "a@1.0-1"}, nil).EffectiveRoots("")
	if err != nil {
		t.Fatalf("EffectiveRoots: %v", err)
	}
	if got["a"] != "a@1.0-1" {
		t.Errorf("roots[a] = %q, want a@1.0-1", got["a"])
	}
}

// TestCheckDeclaredComparesPins is the case the name-only comparison
// missed: gale.toml pins an exact version, so an edited pin means the
// lock no longer describes what was asked for. Legacy IsStale caught
// this through VersionMatches, and losing it would make a pin edit
// invisible to the direnv staleness check.
func TestCheckDeclaredComparesPins(t *testing.T) {
	tests := []struct {
		name      string
		roots     map[string]string
		declared  map[string]string
		wantStale bool
		names     []string
	}{
		{
			name:      "exact agreement",
			roots:     map[string]string{"jq": "jq@1.8.1-1"},
			declared:  map[string]string{"jq": "1.8.1-1"},
			wantStale: false,
		},
		{
			// The normal state: gale.toml carries the bare version so
			// the entry tracks revision bumps, the lock carries the
			// canonical form it resolved to.
			name:      "bare manifest pin against a canonical root",
			roots:     map[string]string{"jq": "jq@1.8.1-2"},
			declared:  map[string]string{"jq": "1.8.1"},
			wantStale: false,
		},
		{
			name:      "edited pin",
			roots:     map[string]string{"jq": "jq@1.8.1-2"},
			declared:  map[string]string{"jq": "1.9.0"},
			wantStale: true,
			names:     []string{"jq", "1.9.0", "1.8.1-2"},
		},
		{
			// A revision suffix of 0 is not canonical, so it is not the
			// bare-vs-canonical case and must not be reconciled away.
			name:      "revision zero is not a match",
			roots:     map[string]string{"jq": "jq@1.8.1-0"},
			declared:  map[string]string{"jq": "1.8.1"},
			wantStale: true,
		},
		{
			name:      "declared with no root",
			roots:     map[string]string{},
			declared:  map[string]string{"jq": "1.8.1"},
			wantStale: true,
			names:     []string{"no locked root"},
		},
		{
			name:      "root no longer declared",
			roots:     map[string]string{"jq": "jq@1.8.1-1"},
			declared:  map[string]string{},
			wantStale: true,
			names:     []string{"no longer declares"},
		},
		{
			name:      "empty on both sides",
			roots:     map[string]string{},
			declared:  map[string]string{},
			wantStale: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckDeclared(tt.roots, tt.declared, Origins{})
			if tt.wantStale {
				if !errors.Is(err, ErrStaleLock) {
					t.Fatalf("err = %v, want ErrStaleLock", err)
				}
				for _, want := range tt.names {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error does not mention %q: %v", want, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("CheckDeclared: %v", err)
			}
		})
	}
}

// TestCheckDeclaredNamesEveryDisagreement pins that one message
// carries all three directions at once. A message naming one leaves
// the user fixing that and rerunning to discover the next.
func TestCheckDeclaredNamesEveryDisagreement(t *testing.T) {
	err := CheckDeclared(
		map[string]string{"jq": "jq@1.8.1-1", "orphan": "orphan@1.0-1"},
		map[string]string{"jq": "1.9.0", "missing": "2.0"},
		Origins{},
	)
	if !errors.Is(err, ErrStaleLock) {
		t.Fatalf("err = %v, want ErrStaleLock", err)
	}
	for _, want := range []string{"missing", "orphan", "jq"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// Design §9: a fail-closed condition is a "hard error naming the exact
// remedy". This one renders inside direnv output, where the user gets
// one line and no chance to ask a follow-up question, so a message
// that only states the disagreement leaves them stuck.
func TestCheckDeclaredNamesTheRemedy(t *testing.T) {
	// A root the manifest dropped. Only the lock can be brought back
	// into line, so there is exactly one remedy.
	err := CheckDeclared(
		map[string]string{"jq": "jq@1.8.1-1", "orphan": "orphan@1.0-1"},
		map[string]string{"jq": "1.8.1"},
		Origins{},
	)
	if err == nil {
		t.Fatal("orphaned root: want ErrStaleLock, got nil")
	}
	if !strings.Contains(err.Error(), "gale lock") {
		t.Errorf("orphaned root: message must name 'gale lock', got %q", err)
	}

	// A newly declared root, the `gale add` case. Two remedies apply
	// and the design names both: `gale install` to install and lock it,
	// or `gale lock` to lock what is already on disk.
	err = CheckDeclared(
		map[string]string{"jq": "jq@1.8.1-1"},
		map[string]string{"jq": "1.8.1", "ripgrep": "14.1.0"},
		Origins{},
	)
	if err == nil {
		t.Fatal("unlocked root: want ErrStaleLock, got nil")
	}
	for _, want := range []string{"gale install", "gale lock"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("unlocked root: message must name %q, got %q", want, err)
		}
	}
}

// The remedy must not claim more precision than it has. Plain `gale
// lock` regenerates [targets.default] from the shared [packages]
// section and carries every host target forward untouched, so a
// package declared under [hosts.<selector>.packages] is NOT repaired
// by it. The roots map here is already merged across selectors, so
// this function cannot tell which case it is looking at; naming only
// the default form would send a host-overlay user round a loop where
// the remedy reports success and the sync keeps failing.
func TestCheckDeclaredRemedyCoversHostOverlays(t *testing.T) {
	err := CheckDeclared(
		map[string]string{"jq": "jq@1.8.1-1", "orphan": "orphan@1.0-1"},
		map[string]string{"jq": "1.8.1"},
		Origins{},
	)
	if err == nil {
		t.Fatal("want ErrStaleLock, got nil")
	}
	if strings.Contains(err.Error(), "--host") {
		t.Errorf("remedy must not name leftover --host, got %q", err)
	}
	if !strings.Contains(err.Error(), "[hosts.*]") {
		t.Errorf("remedy must name leftover [hosts.*], got %q", err)
	}
}

// The remedy must name one command, not a form the user has to
// complete. `gale lock` regenerates [targets.default] and carries
// every host target forward, so a root that lives in an overlay is
// repaired only by `gale lock --host <that selector>`. EffectiveRoots
// merges the targets, so the selector has to be carried out with the
// merged map or the message cannot name it.
func TestCheckDeclaredRemedyNamesTheOwningTarget(t *testing.T) {
	lf := rootsOf(
		[]string{"jq@1.8.1-1"},
		map[string][]string{"work-*": {"ripgrep@14.1.0-1"}},
	)
	roots, origin, err := lf.EffectiveRootsWithOrigin("work-mb")
	if err != nil {
		t.Fatalf("EffectiveRootsWithOrigin: %v", err)
	}
	if origin["ripgrep"] != "work-*" {
		t.Fatalf("origin[ripgrep] = %q, want the selector that rooted it",
			origin["ripgrep"])
	}
	if origin["jq"] != "" {
		t.Fatalf("origin[jq] = %q, want \"\" for the default target",
			origin["jq"])
	}

	// ripgrep is orphaned: rooted in the overlay, gone from gale.toml.
	// Only `gale lock --host work-*` rewrites that target.
	err = CheckDeclared(roots, map[string]string{"jq": "1.8.1"}, Origins{Roots: origin})
	if err == nil {
		t.Fatal("want ErrStaleLock, got nil")
	}
	if strings.Contains(err.Error(), "--host") {
		t.Errorf("remedy must not name leftover --host, got %q", err)
	}
	if !strings.Contains(err.Error(), "[hosts.*]") ||
		!strings.Contains(err.Error(), "gale lock") {
		t.Errorf("remedy must name leftover [hosts.*] and gale lock, got %q", err)
	}

	// A default-target orphan needs the plain form, with no --host at
	// all: suggesting one would send the user to a target that does not
	// root the package.
	err = CheckDeclared(roots, map[string]string{"ripgrep": "14.1.0"}, Origins{Roots: origin})
	if err == nil {
		t.Fatal("want ErrStaleLock, got nil")
	}
	if strings.Contains(err.Error(), "--host") {
		t.Errorf("default-target orphan must not be sent to --host, got %q", err)
	}
}

// A repin's remedy follows the MANIFEST's origin, not the lock's.
// The two differ exactly when a host overlay re-pins a package the
// lock roots only in its default target:
//
//	[packages]              foo = "1"
//	[hosts."work-*".packages]  foo = "2"
//	lock default target        foo@1-1
//
// The effective declaration is "2" and the lock says 1-1, so this is a
// repin. The lock root's origin is the default target, and plain
// `gale lock` regenerates that target from [packages] — writing 1-1
// straight back and never creating the host target the sync needs.
// The user runs the suggested command, it reports success, and the
// sync keeps failing.
func TestCheckDeclaredRepinRemedyFollowsTheManifest(t *testing.T) {
	err := CheckDeclared(
		map[string]string{"foo": "foo@1-1"},
		map[string]string{"foo": "2"},
		Origins{
			Roots:    map[string]string{"foo": ""},
			Declared: map[string]string{"foo": "work-*"},
		},
	)
	if err == nil {
		t.Fatal("want ErrStaleLock, got nil")
	}
	if strings.Contains(err.Error(), "--host") {
		t.Errorf("repin remedy must not name leftover --host, got %q", err)
	}
	if !strings.Contains(err.Error(), "[hosts.*]") {
		t.Errorf("repin remedy must name leftover [hosts.*], got %q", err)
	}
}

// Every required action, not the first one found. A manifest can be
// stale in several directions at once, and a message naming one
// remedy leaves the user fixing that, rerunning, and discovering the
// next — the same failure the multi-part message above already avoids
// for the diagnosis half.
func TestCheckDeclaredRemedyRendersEveryAction(t *testing.T) {
	err := CheckDeclared(
		// ripgrep is orphaned in an overlay; newpkg is declared and
		// unlocked; both need an action.
		map[string]string{"ripgrep": "ripgrep@14.1.0-1"},
		map[string]string{"newpkg": "1.0"},
		Origins{
			Roots:    map[string]string{"ripgrep": "work-*"},
			Declared: map[string]string{"newpkg": ""},
		},
	)
	if err == nil {
		t.Fatal("want ErrStaleLock, got nil")
	}
	for _, want := range []string{"gale install", "[hosts.*]", "gale lock"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("remedy must include %q, got %q", want, err)
		}
	}
	if strings.Contains(err.Error(), "--host") {
		t.Errorf("remedy must not name leftover --host, got %q", err)
	}
}

// A newly declared root that is ALREADY INSTALLED is locked by `gale
// lock`, and that alternative is target-dependent even though the
// `gale install` one is not. Declared under an overlay, plain `gale
// lock` rewrites the default target and leaves the sync stale, so the
// alternative has to name the overlay too.
func TestCheckDeclaredNewOverlayRootNamesItsTarget(t *testing.T) {
	err := CheckDeclared(
		map[string]string{},
		map[string]string{"newpkg": "1.0"},
		Origins{
			Roots:    map[string]string{},
			Declared: map[string]string{"newpkg": "work-*"},
		},
	)
	if err == nil {
		t.Fatal("want ErrStaleLock, got nil")
	}
	if strings.Contains(err.Error(), "--host") {
		t.Errorf("new overlay root must not name leftover --host, got %q", err)
	}
	if !strings.Contains(err.Error(), "[hosts.*]") ||
		!strings.Contains(err.Error(), "gale lock") {
		t.Errorf("new overlay root must name leftover [hosts.*] and gale lock, got %q", err)
	}
}

// Origin knowledge is per side, not all-or-nothing. A caller that
// supplies Roots but omits Declared knows where an ORPHAN lives and
// knows nothing about where a REPIN is declared; reading the missing
// map returns "", which is indistinguishable from "the default
// target" and would print a command that rewrites the wrong section.
// An unknown side must fall back to the general wording.
func TestCheckDeclaredUnknownDeclaredOriginDoesNotAssumeDefault(t *testing.T) {
	err := CheckDeclared(
		map[string]string{"foo": "foo@1-1"},
		map[string]string{"foo": "2"},
		// Roots known, Declared absent: this is lockIsStale's shape.
		Origins{Roots: map[string]string{"foo": ""}},
	)
	if err == nil {
		t.Fatal("want ErrStaleLock, got nil")
	}
	if strings.Contains(err.Error(), "--host") {
		t.Errorf("unknown declared origin must not name leftover --host, got %q", err)
	}
	if !strings.Contains(err.Error(), "[hosts.*]") ||
		!strings.Contains(err.Error(), "gale lock") {
		t.Errorf("an unknown declared origin must name leftover [hosts.*] "+
			"and gale lock, got %q", err)
	}
}

// The mirror: orphans need Roots, so omitting that map must not make
// an orphan look default-rooted either.
func TestCheckDeclaredUnknownRootOriginDoesNotAssumeDefault(t *testing.T) {
	err := CheckDeclared(
		map[string]string{"orphan": "orphan@1.0-1"},
		map[string]string{},
		Origins{Declared: map[string]string{}},
	)
	if err == nil {
		t.Fatal("want ErrStaleLock, got nil")
	}
	if strings.Contains(err.Error(), "--host") {
		t.Errorf("unknown root origin must not name leftover --host, got %q", err)
	}
	if !strings.Contains(err.Error(), "[hosts.*]") ||
		!strings.Contains(err.Error(), "gale lock") {
		t.Errorf("an unknown root origin must name leftover [hosts.*] "+
			"and gale lock, got %q", err)
	}
}

// A lock can parse cleanly and still be incoherent: a root or a
// dependency edge naming a node the document omits. Load validates
// syntax and schema, not graph completeness, so nothing catches it
// until something walks the closure.
//
// That gap is dangerous for any consumer whose question is "does this
// lock reference X", because an incomplete document answers "no" for
// the very identity it is missing. Design §13's migration veto asks
// exactly that question before destroying a store directory.
func TestCheckReferencesRejectsARootWithNoNode(t *testing.T) {
	lf := &V1{
		Version:  SchemaVersion,
		Targets:  Targets{Default: &Target{Roots: []string{"jq@1.7-1"}}},
		Packages: map[string]Package{},
	}
	err := lf.CheckReferences()
	if !errors.Is(err, ErrMissingNode) {
		t.Fatalf("want ErrMissingNode, got %v", err)
	}
	if !strings.Contains(err.Error(), "jq@1.7-1") {
		t.Errorf("error must name the missing node, got %q", err)
	}
}

// The same for an edge, which is the likelier shape: a node exists,
// and one of its recorded dependencies does not.
func TestCheckReferencesRejectsAnEdgeWithNoNode(t *testing.T) {
	lf := &V1{
		Version: SchemaVersion,
		Targets: Targets{Default: &Target{Roots: []string{"jq@1.7-1"}}},
		Packages: map[string]Package{
			"jq@1.7-1": {Artifacts: map[string]Artifact{
				"darwin/arm64": {
					SHA256:      "aa",
					Method:      "binary",
					RuntimeDeps: []string{"oniguruma@6.9-1"},
				},
			}},
		},
	}
	err := lf.CheckReferences()
	if !errors.Is(err, ErrMissingNode) {
		t.Fatalf("want ErrMissingNode, got %v", err)
	}
	if !strings.Contains(err.Error(), "oniguruma@6.9-1") {
		t.Errorf("error must name the missing node, got %q", err)
	}
}

// Build edges count too. A source node records what it was built
// from, and a lock that names a build dependency it does not define
// is as incomplete as one missing a runtime dependency.
func TestCheckReferencesRejectsAMissingBuildDep(t *testing.T) {
	lf := &V1{
		Version: SchemaVersion,
		Targets: Targets{Default: &Target{Roots: []string{"jq@1.7-1"}}},
		Packages: map[string]Package{
			"jq@1.7-1": {Artifacts: map[string]Artifact{
				"darwin/arm64": {
					SHA256:    "aa",
					Method:    "source",
					BuildDeps: []string{"autoconf@2.72-1"},
				},
			}},
		},
	}
	if err := lf.CheckReferences(); !errors.Is(err, ErrMissingNode) {
		t.Fatalf("want ErrMissingNode for a build dep, got %v", err)
	}
}

// A complete document passes, including one whose nodes carry
// artifacts for platforms this machine will never ask about: the
// check is about references resolving, not about any one platform
// being present.
func TestCheckReferencesAcceptsACompleteDocument(t *testing.T) {
	lf := &V1{
		Version: SchemaVersion,
		Targets: Targets{
			Default: &Target{Roots: []string{"jq@1.7-1"}},
			Host:    map[string]Target{"work-*": {Roots: []string{"rg@14-1"}}},
		},
		Packages: map[string]Package{
			"jq@1.7-1": {Artifacts: map[string]Artifact{
				"darwin/arm64": {
					SHA256: "aa", Method: "binary",
					RuntimeDeps: []string{"oniguruma@6.9-1"},
				},
				"linux/amd64": {SHA256: "bb", Method: "binary"},
			}},
			"oniguruma@6.9-1": {Artifacts: map[string]Artifact{
				"darwin/arm64": {SHA256: "cc", Method: "source"},
			}},
			"rg@14-1": {Artifacts: map[string]Artifact{
				"darwin/arm64": {SHA256: "dd", Method: "binary"},
			}},
		},
	}
	if err := lf.CheckReferences(); err != nil {
		t.Errorf("complete document rejected: %v", err)
	}
}

// Coherence is not only "every referenced string is a key". A lock
// rooting and defining "jq@1.7" — no revision — is internally
// consistent by that measure while naming no canonical identity at
// all, so a consumer asking about "jq@1.7-1" gets "not referenced"
// and may conclude the document is a complete statement of what the
// scope requires. It is not: it cannot address a store directory.
func TestCheckReferencesRejectsANoncanonicalIdentity(t *testing.T) {
	for _, tt := range []struct {
		name string
		lf   *V1
	}{
		{
			name: "root and node with no revision",
			lf: &V1{
				Version: SchemaVersion,
				Targets: Targets{Default: &Target{Roots: []string{"jq@1.7"}}},
				Packages: map[string]Package{
					"jq@1.7": {Artifacts: map[string]Artifact{
						"darwin/arm64": {SHA256: "aa", Method: "binary"},
					}},
				},
			},
		},
		{
			name: "package key is not an identity",
			lf: &V1{
				Version: SchemaVersion,
				Targets: Targets{Default: &Target{Roots: []string{"jq@1.7-1"}}},
				Packages: map[string]Package{
					"jq@1.7-1": {Artifacts: map[string]Artifact{
						"darwin/arm64": {SHA256: "aa", Method: "binary"},
					}},
					"bare-name": {},
				},
			},
		},
		{
			name: "edge is not an identity",
			lf: &V1{
				Version: SchemaVersion,
				Targets: Targets{Default: &Target{Roots: []string{"jq@1.7-1"}}},
				Packages: map[string]Package{
					"jq@1.7-1": {Artifacts: map[string]Artifact{
						"darwin/arm64": {
							SHA256: "aa", Method: "binary",
							RuntimeDeps: []string{"oniguruma@6.9"},
						},
					}},
					"oniguruma@6.9": {},
				},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.lf.CheckReferences(); err == nil {
				t.Error("a noncanonical identity must not read as coherent")
			}
		})
	}
}
