package main

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/lockplan"
	"github.com/kelp/gale/internal/recipe"
)

// Acceptance 7 (design §9): every existing project holds a legacy
// sync-written gale.lock, so the first sync after upgrade fails
// everywhere, including inside direnv. Treating the legacy file as
// absent was considered and rejected — it is a silent downgrade of a
// security control — so the refusal must be an error, and it must
// name the one command that fixes it.
func TestLockedSyncPlanRefusesLegacySchema(t *testing.T) {
	view := &lockfile.View{
		Kind:   lockfile.KindLegacy,
		Legacy: &lockfile.LockFile{},
	}

	plan, warn, err := lockedSyncPlan(view, lockplan.Request{}, false)
	if err == nil {
		t.Fatalf("legacy lock: want refusal, got plan=%v warn=%q", plan, warn)
	}
	if plan != nil {
		t.Errorf("legacy lock: plan must be nil on refusal, got %v", plan)
	}
	if !errors.Is(err, lockfile.ErrLegacySchema) {
		t.Errorf("legacy lock: want ErrLegacySchema, got %v", err)
	}
	// The remedy has to be in the message itself: this error renders
	// inside direnv output, where the user sees one line and nothing
	// else.
	if !strings.Contains(err.Error(), "gale lock") {
		t.Errorf("legacy lock: message must name 'gale lock', got %q", err)
	}
}

// --no-frozen is the documented escape hatch (design §9): it
// downgrades a fail-closed condition to a warning and proceeds
// unlocked. It must not merely soften the message — the plan has to
// come back nil, or the sync would still enforce the lock the user
// asked it to ignore.
func TestLockedSyncPlanNoFrozenDowngradesLegacyToWarning(t *testing.T) {
	view := &lockfile.View{
		Kind:   lockfile.KindLegacy,
		Legacy: &lockfile.LockFile{},
	}

	plan, warn, err := lockedSyncPlan(view, lockplan.Request{}, true)
	if err != nil {
		t.Fatalf("--no-frozen: want no error, got %v", err)
	}
	if plan != nil {
		t.Errorf("--no-frozen: want unlocked mode (nil plan), got %v", plan)
	}
	if warn == "" {
		t.Error("--no-frozen: proceeding unlocked must warn, got no warning")
	}
}

// Acceptance 9 (design §9): `sync --build` is rejected when the locked
// method is binary, at plan validation, before any dep or store
// mutation. The installer already refuses to demote a locked binary
// node mid-install, but discovering that after installing three
// dependencies wastes the work and leaves the user reading a failure
// from deep inside a build.
//
// It is an ordinary usage failure, not an integrity one: nothing
// disagrees with the lock, the user asked for something the lock
// forbids.
func TestRejectSourceOnlyRefusesALockedBinaryNode(t *testing.T) {
	plan := &lockplan.Plan{
		Nodes: map[string]lockplan.Node{
			"jq@1.7-1": {
				Name: "jq", Version: "1.7-1",
				Method: lockgraph.MethodBinary,
			},
		},
		Order: []string{"jq@1.7-1"},
	}

	err := rejectSourceOnly(plan)
	if err == nil {
		t.Fatal("--build against a locked binary node: want refusal, got nil")
	}
	if !strings.Contains(err.Error(), "jq") {
		t.Errorf("refusal must name the offending package, got %q", err)
	}
	if got := exitCodeFor(err); got != exitFailure {
		t.Errorf("refusal is a usage error: got exit %d, want %d",
			got, exitFailure)
	}
}

// The flag stays honored where the lock permits it: a plan locked
// entirely to source is exactly what --build would produce anyway.
// Refusing here would make the flag unusable under any lock.
func TestRejectSourceOnlyAllowsALockedSourceNode(t *testing.T) {
	plan := &lockplan.Plan{
		Nodes: map[string]lockplan.Node{
			"jq@1.7-1": {
				Name: "jq", Version: "1.7-1",
				Method: lockgraph.MethodSource,
			},
		},
		Order: []string{"jq@1.7-1"},
	}

	if err := rejectSourceOnly(plan); err != nil {
		t.Errorf("--build against a locked source node: want allowed, got %v", err)
	}
}

// Unlocked, --build means what it always meant. A nil plan must not
// trip the check.
func TestRejectSourceOnlyIgnoresAnUnlockedSync(t *testing.T) {
	if err := rejectSourceOnly(nil); err != nil {
		t.Errorf("--build unlocked: want allowed, got %v", err)
	}
}

// A dry run must report a failure as a failure. Before the locked
// path existed this could not happen — the unlocked body returns at
// its dry-run step before anything can fail — so the switch in
// reportSyncOutcomes tested dryRun ahead of the error cases and any
// error was unreachable. Under a lock the dry run verifies provenance,
// which can conflict, and a conflict silently printed as "install
// (stale)" would tell the user their sync is fine minutes before it
// exits 3.
func TestReportSyncOutcomesReportsAFailureDuringDryRun(t *testing.T) {
	out := newOutputForWriter(io.Discard)
	installed, failures := reportSyncOutcomes(out, []syncOutcome{{
		name: "jq", version: "1.7-1",
		installErr: errors.New("provenance conflict"),
	}}, true)

	if len(failures) != 1 {
		t.Errorf("dry-run failure: failures = %d, want 1", len(failures))
	}
	if installed != 0 {
		t.Errorf("dry-run failure: installed = %d, want 0", installed)
	}
}

// The ordinary dry-run line survives: a clean item still reports as an
// install rather than being swallowed by the reordering above.
func TestReportSyncOutcomesStillReportsAPlannedInstallDuringDryRun(t *testing.T) {
	out := newOutputForWriter(io.Discard)
	installed, failures := reportSyncOutcomes(out, []syncOutcome{{
		name: "jq", version: "1.7-1",
	}}, true)

	if installed != 1 {
		t.Errorf("dry-run install: installed = %d, want 1", installed)
	}
	if len(failures) != 0 {
		t.Errorf("dry-run install: failures = %d, want 0", len(failures))
	}
}

// Under a plan every item must carry its locked node, and must report
// the canonical version the lock names rather than gale.toml's bare
// pin. Without the node the worker falls back to the unlocked body,
// which resolves the recipe itself, checks staleness against the
// store, and can call Reinstall — three selectors the lock is supposed
// to have replaced.
//
// Enforcement does not catch that on its own: Installer.Plan is set
// either way, so a mismatching artifact still fails. What silently
// comes back is the resolution work the lock exists to skip, and the
// stale-replacement path design §4 prohibits.
func TestSortedSyncItemsCarriesTheLockedNode(t *testing.T) {
	plan := &lockplan.Plan{
		Nodes: map[string]lockplan.Node{
			"jq@1.7-3": {
				Name: "jq", Version: "1.7-3",
				Method: lockgraph.MethodBinary,
			},
		},
		Order: []string{"jq@1.7-3"},
	}

	// gale.toml pins the bare version; the lock resolved it to a
	// revision. The two spellings are correct by design, and the lock's
	// is the one that names a store directory.
	items, err := sortedSyncItems(
		map[string]string{"jq": "1.7"}, plan,
	)
	if err != nil {
		t.Fatalf("sortedSyncItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].planned == nil {
		t.Fatal("locked item carries no plan node, so the worker would " +
			"take the unlocked path and resolve the recipe itself")
	}
	if items[0].planned.Version != "1.7-3" {
		t.Errorf("planned.Version = %q, want %q",
			items[0].planned.Version, "1.7-3")
	}
	if items[0].version != "1.7-3" {
		t.Errorf("item.version = %q, want the locked %q, not the bare pin",
			items[0].version, "1.7-3")
	}
}

// A declared package with no node in the plan cannot occur — plan
// construction proves roots and declared agree — so reaching it means
// an invariant broke. It must fail rather than degrade to unlocked,
// which would install exactly one package off-lock and report success.
func TestSortedSyncItemsRefusesADeclaredPackageMissingFromThePlan(t *testing.T) {
	plan := &lockplan.Plan{
		Nodes: map[string]lockplan.Node{
			"jq@1.7-3": {Name: "jq", Version: "1.7-3"},
		},
		Order: []string{"jq@1.7-3"},
	}

	_, err := sortedSyncItems(
		map[string]string{"jq": "1.7", "ripgrep": "14.1.0"}, plan,
	)
	if err == nil {
		t.Fatal("declared package with no plan node: want refusal, got nil")
	}
	if !errors.Is(err, lockfile.ErrMissingNode) {
		t.Errorf("want ErrMissingNode, got %v", err)
	}
	if !strings.Contains(err.Error(), "ripgrep") {
		t.Errorf("refusal must name the package, got %q", err)
	}
}

// The locked body resolves nothing. Under a plan the recipe, the
// version and the method all come from the lock, so a resolver call
// here means a second selector is live — the exact thing #182 removed,
// since before it every transitive dependency went to the registry and
// a committed lock never reached one.
//
// Enforcement alone does not reveal this: Installer.Plan is set either
// way, so a mismatch still fails and the sync still exits 3. What the
// unlocked fallthrough quietly restores is recipe resolution and the
// stale-replacement path (Reinstall), which design §4 prohibits
// in-plan. Only the dispatch itself can be asserted, so it is.
func TestRunSyncOneUnderAPlanResolvesNothing(t *testing.T) {
	tmp := t.TempDir()
	storeRoot := filepath.Join(tmp, "store")
	galeDir := filepath.Join(tmp, ".gale")

	resolved := false
	ctx := buildFakeCtx(t,
		filepath.Join(tmp, "gale.toml"), galeDir, storeRoot,
		func(_ context.Context, _ string) (*recipe.Recipe, error) {
			resolved = true
			return nil, errors.New("the locked path must not resolve")
		})

	// The store must already hold the package, or the unlocked body
	// short-circuits before its own resolve and the assertion passes
	// for the wrong reason. installedStale is reached only when
	// IsInstalled is true.
	seedStore(t, storeRoot, "jq", "1.7-1")

	r := minimalRecipe("jq", "1.7")
	node := lockplan.Node{
		Name: "jq", Version: "1.7-1",
		Method: lockgraph.MethodBinary,
		Recipe: r,
	}
	plan := &lockplan.Plan{
		Nodes: map[string]lockplan.Node{"jq@1.7-1": node},
		Order: []string{"jq@1.7-1"},
	}
	ctx.Installer.Plan = plan

	// Dry run: read-only, so the assertion is about which body ran and
	// nothing else. The unlocked body resolves inside installedStale
	// before its own dry-run return.
	runSyncOne(context.Background(), ctx, syncItem{
		name: "jq", version: "1.7-1", planned: &node,
	}, true)

	if resolved {
		t.Error("locked sync resolved a recipe; the lock is supposed to " +
			"be the only selector")
	}
}

// No lockfile at all is unlocked mode with one warning (design §9),
// distinct from a lock that is present and unusable. Absent is the
// state a project is in before its first `gale lock`, so it cannot be
// an error.
func TestLockedSyncPlanAbsentLockWarnsAndProceeds(t *testing.T) {
	view := &lockfile.View{Kind: lockfile.KindAbsent}

	plan, warn, err := lockedSyncPlan(view, lockplan.Request{}, false)
	if err != nil {
		t.Fatalf("absent lock: want no error, got %v", err)
	}
	if plan != nil {
		t.Errorf("absent lock: want unlocked mode (nil plan), got %v", plan)
	}
	if warn == "" {
		t.Error("absent lock: want a warning, got none")
	}
}
