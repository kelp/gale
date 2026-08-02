package farm

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestFarmGuard_AgreeingClaimantAllowsMutation is the case an
// implementation is most likely to get wrong (design §4, acceptance
// test 37): an external claimant that AGREES with the proposed farm
// mapping — it requires exactly the soname targets the mutation
// would leave behind — must never block the mutation. A guard that
// refuses on the mere existence of an external claimant is a verb
// veto, and a verb veto deadlocks every legitimate operation.
func TestFarmGuard_AgreeingClaimantAllowsMutation(t *testing.T) {
	root := t.TempDir()
	storeDir := storeLayout(t, root, "curl", "8.19.0-1",
		[]string{versionedName("libcurl", "4")})

	// The external claimant's closure resolves to the same store
	// dir, so its claim (libcurl.4 -> curl@8.19.0-1) is exactly
	// what populating storeDir would write.
	agreeing := Claimant{
		Label:  "project /home/other",
		Claims: At(storeDir),
	}

	err := GuardPopulate(At(storeDir), []Claimant{agreeing})
	if err != nil {
		t.Fatalf(
			"agreeing external claimant must allow the mutation, got: %v",
			err,
		)
	}
}

// TestFarmGuard_SelfUpdateAllowed: a scope updating its own package
// proposes the new version while the farm still points at the old
// one. The scope's old closure is superseded, not a claim, so with
// no external claim on the soname the retarget must be allowed —
// refusing it would deadlock every update (design §4, test 37).
// The external claimant here claims an unrelated soname, so its
// mere presence must not block anything.
func TestFarmGuard_SelfUpdateAllowed(t *testing.T) {
	root := t.TempDir()
	oldDir := storeLayout(t, root, "curl", "8.19.0-1",
		[]string{versionedName("libcurl", "4")})
	newDir := storeLayout(t, root, "curl", "8.20.0-1",
		[]string{versionedName("libcurl", "4")})
	otherDir := storeLayout(t, root, "zstd", "1.5.6-1",
		[]string{versionedName("libzstd", "1")})

	farmDir := filepath.Join(root, "lib")
	if err := Populate(oldDir, farmDir); err != nil {
		t.Fatal(err)
	}

	unrelated := Claimant{
		Label:  "project /home/other",
		Claims: At(otherDir),
	}
	if err := GuardPopulate(
		At(newDir), []Claimant{unrelated},
	); err != nil {
		t.Fatalf("self-update with no external claim on the soname "+
			"must be allowed, got: %v", err)
	}
}

// TestFarmGuard_RemoveUnclaimedAllowed: a scope removing its own
// package deletes farm links nobody else claims. An external
// claimant that claims only other sonames must not block the
// removal (design §4, test 37).
func TestFarmGuard_RemoveUnclaimedAllowed(t *testing.T) {
	root := t.TempDir()
	goneDir := storeLayout(t, root, "curl", "8.19.0-1",
		[]string{versionedName("libcurl", "4")})
	otherDir := storeLayout(t, root, "zstd", "1.5.6-1",
		[]string{versionedName("libzstd", "1")})

	farmDir := filepath.Join(root, "lib")
	for _, d := range []string{goneDir, otherDir} {
		if err := Populate(d, farmDir); err != nil {
			t.Fatal(err)
		}
	}

	external := Claimant{
		Label:  "project /home/other",
		Claims: At(otherDir),
	}
	if err := GuardDepopulate(
		goneDir, farmDir, []Claimant{external},
	); err != nil {
		t.Fatalf("removing an unclaimed soname must be allowed, "+
			"got: %v", err)
	}
}

// TestFarmGuard_RebuildKeepsExternalClaim: a rebuild proposed from
// one scope's closure alone must not delete a soname only another
// scope claims. The guard allows the rebuild and returns the union
// of dirs, so the resulting farm satisfies the external claim —
// this is also how a rebuild repairs a missing link with exactly
// the target every claimant wants (design §4, test 37).
func TestFarmGuard_RebuildKeepsExternalClaim(t *testing.T) {
	root := t.TempDir()
	mineDir := storeLayout(t, root, "curl", "8.19.0-1",
		[]string{versionedName("libcurl", "4")})
	theirDir := storeLayout(t, root, "zstd", "1.5.6-1",
		[]string{versionedName("libzstd", "1")})

	external := Claimant{
		Label:  "project /home/other",
		Claims: At(theirDir),
	}
	union, err := GuardRebuild(
		[]string{mineDir}, []Claimant{external},
	)
	if err != nil {
		t.Fatalf("disjoint external claim must allow the rebuild, "+
			"got: %v", err)
	}
	for _, want := range []string{mineDir, theirDir} {
		if !slices.Contains(union, want) {
			t.Errorf("union %v must include %s", union, want)
		}
	}
}

// TestFarmGuard_RebuildAgreeingClaimantNoDuplicates: an external
// claimant sharing the proposed dirs is agreement, not conflict,
// and the union must not repeat a dir (Populate would then race
// itself on identical links).
func TestFarmGuard_RebuildAgreeingClaimantNoDuplicates(t *testing.T) {
	root := t.TempDir()
	shared := storeLayout(t, root, "curl", "8.19.0-1",
		[]string{versionedName("libcurl", "4")})

	agreeing := Claimant{
		Label:  "project /home/other",
		Claims: At(shared),
	}
	union, err := GuardRebuild(
		[]string{shared}, []Claimant{agreeing},
	)
	if err != nil {
		t.Fatalf("agreeing claimant must allow the rebuild, got: %v", err)
	}
	if len(union) != 1 || union[0] != shared {
		t.Errorf("union = %v, want exactly [%s]", union, shared)
	}
}

// conflictFixture builds two versions of one soname-providing
// package plus a populated farm, and returns a claimant locked to
// the old version. Every negative test needs a conflicting — not
// merely present — external claimant (acceptance test 28).
type conflictFixture struct {
	oldDir, newDir, farmDir string
	claimant                Claimant
}

func newConflictFixture(t *testing.T) conflictFixture {
	t.Helper()
	root := t.TempDir()
	f := conflictFixture{
		oldDir: storeLayout(t, root, "curl", "8.19.0-1",
			[]string{versionedName("libcurl", "4")}),
		newDir: storeLayout(t, root, "curl", "8.20.0-1",
			[]string{versionedName("libcurl", "4")}),
		farmDir: filepath.Join(root, "lib"),
	}
	if err := Populate(f.oldDir, f.farmDir); err != nil {
		t.Fatal(err)
	}
	f.claimant = Claimant{
		Label:  "project /home/other",
		Claims: At(f.oldDir),
	}
	return f
}

// wantConflict asserts a guard refusal that carries the sentinel
// and names every listed identity (acceptance 28: the refusal must
// name both versions).
func wantConflict(t *testing.T, err error, names ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("want a conflict refusal, got nil")
	}
	if !errors.Is(err, ErrClaimConflict) {
		t.Fatalf("refusal must wrap ErrClaimConflict, got: %v", err)
	}
	for _, name := range names {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("refusal %q must name %s", err, name)
		}
	}
}

// TestFarmGuard_SelfContradictingClaimIsRefused: a claimant that
// provides one soname from two store dirs describes a state no farm
// mapping can satisfy, so the guard refuses rather than tie-breaking.
//
// This strictness is a contract with the claim SOURCE: whoever
// assembles a claimant must resolve which version the scope
// actually requires before handing it over (see
// generation.FarmClaimants, where a scope's lock and its live
// generation can name different versions of one package). A
// tie-break here would silently pick one and let the refusal the
// scope needed pass.
func TestFarmGuard_SelfContradictingClaimIsRefused(t *testing.T) {
	f := newConflictFixture(t)
	contradictory := Claimant{
		Label:  "project /home/other",
		Claims: At(f.oldDir, f.newDir),
	}
	wantConflict(
		t, GuardPopulate(At(f.oldDir), []Claimant{contradictory}),
		"curl@8.19.0-1", "curl@8.20.0-1",
	)
}

// TestFarmGuard_PlacementJudgesTheDestination: a placement is
// enumerated from ScanDir and judged at FinalDir, so an install
// staged elsewhere is checked against where its links will point,
// not against where its bytes happen to sit right now.
//
// Both directions matter. A staged reinstall of the version a
// claimant already requires must be ALLOWED, which fails if the
// staging path is compared against the claimant's canonical target;
// and a staged install that would repoint a claimed soname must
// still be REFUSED, naming the canonical identity rather than a
// temporary directory the user has never seen.
func TestFarmGuard_PlacementJudgesTheDestination(t *testing.T) {
	f := newConflictFixture(t)
	staging := storeLayout(t, t.TempDir(), "curl", ".build-xyz",
		[]string{versionedName("libcurl", "4")})

	if err := GuardPopulate(
		[]Placement{{ScanDir: staging, FinalDir: f.oldDir}},
		[]Claimant{f.claimant},
	); err != nil {
		t.Errorf("staged reinstall of the claimed version must be "+
			"allowed, got: %v", err)
	}

	err := GuardPopulate(
		[]Placement{{ScanDir: staging, FinalDir: f.newDir}},
		[]Claimant{f.claimant},
	)
	wantConflict(t, err, "curl@8.19.0-1", "curl@8.20.0-1")
	if strings.Contains(err.Error(), staging) {
		t.Errorf("refusal %q names the staging dir; it must name the "+
			"canonical destination", err)
	}
}

// TestFarmGuard_ConflictingClaimRefusesPopulate: repointing a
// soname an external scope requires at another version is refused,
// naming both versions.
func TestFarmGuard_ConflictingClaimRefusesPopulate(t *testing.T) {
	f := newConflictFixture(t)
	err := GuardPopulate(At(f.newDir), []Claimant{f.claimant})
	wantConflict(t, err, "curl@8.19.0-1", "curl@8.20.0-1")
}

// TestFarmGuard_ConflictingClaimRefusesRebuild: a rebuild whose
// proposed closure carries the other version of a claimed soname
// cannot satisfy the claim by union, so it is refused naming both
// versions.
func TestFarmGuard_ConflictingClaimRefusesRebuild(t *testing.T) {
	f := newConflictFixture(t)
	_, err := GuardRebuild([]string{f.newDir}, []Claimant{f.claimant})
	wantConflict(t, err, "curl@8.19.0-1", "curl@8.20.0-1")
}

// TestFarmGuard_ClaimedSonameRefusesDepopulate: deleting the farm
// entry of a soname an external scope requires is refused — a rule
// written only against repointing would leave the claimed entry
// simply gone (design §4).
func TestFarmGuard_ClaimedSonameRefusesDepopulate(t *testing.T) {
	f := newConflictFixture(t)
	err := GuardDepopulate(f.oldDir, f.farmDir, []Claimant{f.claimant})
	wantConflict(t, err, "curl@8.19.0-1", versionedName("libcurl", "4"))
}

// TestFarmGuard_SelfConflictRefusedWithoutExternalClaimant: a
// proposed closure providing one soname from two store dirs
// conflicts with itself and is refused with no external claimant
// present (acceptance test 28).
func TestFarmGuard_SelfConflictRefusedWithoutExternalClaimant(t *testing.T) {
	f := newConflictFixture(t)
	dirs := []string{f.oldDir, f.newDir}

	if err := GuardPopulate(At(dirs...), nil); err == nil ||
		!errors.Is(err, ErrClaimConflict) {
		t.Errorf("populate self-conflict must be refused, got: %v", err)
	}
	if _, err := GuardRebuild(dirs, nil); err == nil ||
		!errors.Is(err, ErrClaimConflict) {
		t.Errorf("rebuild self-conflict must be refused, got: %v", err)
	}
}

// TestFarmGuard_UnreadableClaimantFailsClosed: a scope known to
// exist whose closure could not be read refuses every verb — an
// unreadable claim could be hiding exactly the conflict the guard
// exists to catch (acceptance test 28).
func TestFarmGuard_UnreadableClaimantFailsClosed(t *testing.T) {
	f := newConflictFixture(t)
	unreadable := []Claimant{{
		Label: "project /home/broken",
		Err:   errors.New("permission denied"),
	}}

	cases := []struct {
		verb string
		run  func() error
	}{
		{"populate", func() error {
			return GuardPopulate(At(f.newDir), unreadable)
		}},
		{"depopulate", func() error {
			return GuardDepopulate(f.oldDir, f.farmDir, unreadable)
		}},
		{"rebuild", func() error {
			_, err := GuardRebuild([]string{f.newDir}, unreadable)
			return err
		}},
	}
	for _, c := range cases {
		if err := c.run(); err == nil ||
			!errors.Is(err, ErrClaimConflict) {
			t.Errorf("%s with an unreadable claimant must fail "+
				"closed, got: %v", c.verb, err)
		}
	}
}

// TestGuardStoreRemoval_ClaimedDirRefusedWithoutFarmLink is the
// regression that redirecting the project farm at the shared one
// exposed.
//
// dropFromStore performs TWO mutations: it depopulates the farm and
// it deletes the directory. Only the first was guarded, and
// GuardDepopulate derives what is being removed from the LIVE farm.
// That was sound while a project generation rebuild wiped its own
// local lib dir. Once the rebuild targets the shared farm, it runs
// before dropFromStore and can remove the very link the guard then
// looks for, so the guard reads state its own command already
// rewrote and approves deleting a directory another scope claims.
//
// Deleting a claimed directory is a violation whether or not the
// symlink happens to exist at the instant of the check.
func TestGuardStoreRemoval_ClaimedDirRefusedWithoutFarmLink(t *testing.T) {
	root := t.TempDir()
	storeDir := storeLayout(t, root, "curl", "8.19.0-1",
		[]string{versionedName("libcurl", "4")})
	farmDir := filepath.Join(root, "lib")
	// Farm deliberately empty: the earlier rebuild already wiped it.

	claimant := Claimant{
		Label:  "project /home/other",
		Claims: At(storeDir),
	}

	err := GuardStoreRemoval(storeDir, farmDir, []Claimant{claimant})
	if !errors.Is(err, ErrClaimConflict) {
		t.Fatalf("deleting a claimed store dir must be refused even "+
			"with no farm link present, got: %v", err)
	}
	if !strings.Contains(err.Error(), "project /home/other") {
		t.Errorf("error does not name the claimant: %v", err)
	}
}

// TestGuardStoreRemoval_OtherVersionClaimAllowed is the
// over-refusal twin, and it is why the fix is not "every soname
// under the directory".
//
// The farm resolves libcurl to a DIFFERENT version, so Depopulate
// leaves that link alone and the claim on it survives the removal.
// Refusing here would block a scope from collecting an old revision
// nobody resolves through, which is a verb veto in the same family
// design §4 forbids.
func TestGuardStoreRemoval_OtherVersionClaimAllowed(t *testing.T) {
	root := t.TempDir()
	oldDir := storeLayout(t, root, "curl", "8.19.0-1",
		[]string{versionedName("libcurl", "4")})
	newDir := storeLayout(t, root, "curl", "8.20.0-1",
		[]string{versionedName("libcurl", "4")})

	farmDir := filepath.Join(root, "lib")
	if err := Populate(newDir, farmDir); err != nil {
		t.Fatal(err)
	}

	// The claimant resolves libcurl through the NEW dir, which the
	// removal does not touch.
	claimant := Claimant{
		Label:  "project /home/other",
		Claims: At(newDir),
	}

	if err := GuardStoreRemoval(
		oldDir, farmDir, []Claimant{claimant},
	); err != nil {
		t.Fatalf("removing a version nothing claims must be allowed, "+
			"got: %v", err)
	}
}

// TestGuardStoreRemoval_KeepsTheLiveFarmCheck: the directory-claim
// test is ADDED to the live-farm one, never substituted for it.
//
// Here the claimant records a different version of curl, so the
// exact-directory test passes — but the farm currently resolves the
// soname through the directory being deleted, so the link vanishes
// and the claim goes unsatisfied. That is precisely the case
// GuardDepopulate was written for, and it is the twin of
// OtherVersionClaimAllowed above: same claimant, same two dirs, and
// the answer turns entirely on where the farm points.
func TestGuardStoreRemoval_KeepsTheLiveFarmCheck(t *testing.T) {
	root := t.TempDir()
	soname := versionedName("libcurl", "4")
	oldDir := storeLayout(t, root, "curl", "8.19.0-1", []string{soname})
	newDir := storeLayout(t, root, "curl", "8.20.0-1", []string{soname})

	farmDir := filepath.Join(root, "lib")
	if err := Populate(oldDir, farmDir); err != nil {
		t.Fatal(err)
	}

	claimant := Claimant{
		Label:  "project /home/other",
		Claims: At(newDir),
	}

	err := GuardStoreRemoval(oldDir, farmDir, []Claimant{claimant})
	if !errors.Is(err, ErrClaimConflict) {
		t.Fatalf("removal that deletes the farm entry a claimant "+
			"resolves must still be refused, got: %v", err)
	}
}

// A claimant whose bytes are still STAGED is protected against
// deletion of the directory they are about to land in.
//
// This is the defect that motivated merging the claim halves
// (gh#194). guardClaimedDir read the dir list and ignored the
// placement list, so a scope about to commit into a directory had no
// claim on it and another scope could delete it out from under the
// commit. Nothing recombined the two halves wrongly; one consumer
// simply forgot a half existed, which is what a type encoding one
// thing twice invites.
func TestFarmGuard_StagedClaimProtectsItsDestination(t *testing.T) {
	root := t.TempDir()
	soname := versionedName("libcurl", "4")
	staging := storeLayout(t, root, "curl", ".build-tmp", []string{soname})
	final := filepath.Join(root, "pkg", "curl", "8.19.0-1")

	claimant := Claimant{
		Label:  "project /home/other",
		Claims: []Placement{{ScanDir: staging, FinalDir: final}},
	}

	err := GuardStoreRemoval(final, filepath.Join(root, "lib"),
		[]Claimant{claimant})
	if !errors.Is(err, ErrClaimConflict) {
		t.Errorf("deleting a directory a staged claim lands in was "+
			"allowed: %v", err)
	}
}
