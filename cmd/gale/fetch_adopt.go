package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/mattn/go-isatty"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/fetch"
	"github.com/kelp/gale/internal/index"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/store"
)

var (
	adoptGlobal  bool
	adoptProject bool
	adoptYes     bool
	adoptIndex   string
)

var adoptTTY = stdinIsTTY

// adoptAfterDiff is a test hook after the printed diff and
// before confirm/publish. Production stays nil.
var adoptAfterDiff func()

// adoptFailAfterManifest is a test hook after gale.toml is rewritten
// and before register/lock/swap. Production stays nil.
var adoptFailAfterManifest func() error

var (
	errAdoptCI      = errors.New("gale migrate refuses CI")
	errAdoptNeedYes = errors.New(
		"gale migrate requires --yes when stdin is not a TTY",
	)
	errAdoptAborted   = errors.New("gale migrate aborted")
	errAdoptAlreadyV2 = errors.New(
		"gale migrate refuses an existing v2 lock",
	)
	errAdoptHosts = errors.New(
		"gale migrate refuses host overlays",
	)
	errAdoptNoPlatform = errors.New(
		"gale migrate: no current-platform artifact",
	)
	errAdoptLockMoved = errors.New(
		"gale migrate: lock changed after the printed diff",
	)
)

func stdinIsTTY() bool {
	return isatty.IsTerminal(os.Stdin.Fd()) ||
		isatty.IsCygwinTerminal(os.Stdin.Fd())
}

func ciFrozen() bool {
	return os.Getenv("CI") != "" || os.Getenv("GITHUB_ACTIONS") != ""
}

func parseConfirm(r io.Reader) (bool, error) {
	br := bufio.NewReader(r)
	line, err := br.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("reading confirmation: %w", err)
	}
	s := strings.TrimSpace(strings.ToLower(line))
	return s == "y" || s == "yes", nil
}

type adoptReq struct {
	Source  index.Source
	Yes     bool
	DryRun  bool
	TTY     bool
	In      io.Reader
	Out     io.Writer
	Err     io.Writer
	ToStore func(context.Context, *store.Store, string, string, index.Artifact) (string, error)
}

func runFetchAdopt(ctx context.Context, c *cmdContext, req adoptReq) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ciFrozen() {
		return errAdoptCI
	}
	cfg, err := rawGaleConfig(c.GalePath)
	if err != nil {
		return err
	}
	if err := refuseHostOverlays(cfg); err != nil {
		return err
	}
	declared := declaredForTarget(cfg, "")
	if len(declared) == 0 {
		return fmt.Errorf("%w", errNoDeclarations)
	}

	lp, err := lockfilePath(c.GalePath)
	if err != nil {
		return err
	}
	before, err := readFileSnapshot(lp)
	if err != nil {
		return err
	}
	oldRoots, err := oldLockRoots(lp, before)
	if err != nil {
		return err
	}

	plan, err := planMigrate(ctx, req.Source, declared)
	if err != nil {
		return err
	}
	printAdoptDiff(req.Out, oldRoots, plan)
	if adoptAfterDiff != nil {
		adoptAfterDiff()
	}
	if req.DryRun {
		return nil
	}
	if len(plan.Arts) == 0 {
		return fmt.Errorf("%w", errNoDeclarations)
	}
	if !req.Yes {
		if !req.TTY {
			return errAdoptNeedYes
		}
		if req.Err == nil {
			req.Err = os.Stderr
		}
		fmt.Fprint(req.Err, adoptProceedPrompt(plan)+"\n")
		ok, err := parseConfirm(req.In)
		if err != nil {
			return err
		}
		if !ok {
			return errAdoptAborted
		}
	}

	toStore := req.ToStore
	if toStore == nil {
		toStore = fetch.ToStore
	}
	var (
		edited       bool
		prior, wrote config.FileState
	)
	err = finalizeFetch(ctx, c, fetchPublish{
		Arts:    plan.Arts,
		Lock:    plan.Draft,
		ToStore: toStore,
		afterLock: func() error {
			now, err := readFileSnapshot(lp)
			if err != nil {
				return err
			}
			if !now.Same(before) {
				return errAdoptLockMoved
			}
			return nil
		},
		afterStage: func() error {
			p, w, err := applyAdoptManifest(c.GalePath, plan)
			if err != nil {
				return err
			}
			if w.Exists || p.Exists {
				prior, wrote, edited = p, w, !p.Same(w)
			}
			if adoptFailAfterManifest != nil {
				return adoptFailAfterManifest()
			}
			return nil
		},
	})
	if err != nil && edited {
		return errors.Join(err, config.RestoreUnderLock(c.GalePath, prior, wrote))
	}
	return err
}

func refuseHostOverlays(cfg *config.GaleConfig) error {
	var names []string
	for k, h := range cfg.Hosts {
		if len(h.Packages) > 0 {
			names = append(names, k)
		}
	}
	if len(names) == 0 {
		return nil
	}
	slices.Sort(names)
	return fmt.Errorf(
		"%w: move pins from [hosts.%s] into [packages] and delete the [hosts.*] tables",
		errAdoptHosts, strings.Join(names, "], [hosts."),
	)
}

func oldLockRoots(lp string, snap FileSnapshot) ([]string, error) {
	if !snap.Exists {
		return nil, nil
	}
	view, err := lockfile.Load(lp)
	if err != nil {
		return nil, fmt.Errorf("reading lockfile: %w", err)
	}
	switch view.Kind {
	case lockfile.KindAbsent:
		return nil, nil
	case lockfile.KindV2:
		return nil, errAdoptAlreadyV2
	case lockfile.KindLegacy:
		return legacyLockRoots(view.Legacy), nil
	case lockfile.KindV1:
		if view.V1 == nil || view.V1.Targets.Default == nil {
			return nil, nil
		}
		if len(view.V1.Targets.Host) > 0 {
			return nil, errAdoptHosts
		}
		return append([]string(nil), view.V1.Targets.Default.Roots...), nil
	default:
		return nil, fmt.Errorf("reading lockfile: unhandled kind %s", view.Kind)
	}
}

func legacyLockRoots(lf *lockfile.LockFile) []string {
	if lf == nil {
		return nil
	}
	roots := make([]string, 0, len(lf.Packages))
	for name, pkg := range lf.Packages {
		if pkg.Version == "" {
			roots = append(roots, name)
			continue
		}
		roots = append(roots, name+"@"+pkg.Version)
	}
	slices.Sort(roots)
	return roots
}

type adoptDrop struct {
	Name, Pin, Reason string
}

type adoptBump struct {
	Name, From, To string
}

type adoptPlan struct {
	Draft  *lockfile.V2
	Arts   []fetchArt
	Drops  []adoptDrop
	Bumps  []adoptBump
	Commit string
}

func planAdopt(
	ctx context.Context, src index.Source, declared map[string]string,
) (*lockfile.V2, []fetchArt, error) {
	sess, err := index.Open(ctx, src)
	if err != nil {
		return nil, nil, fmt.Errorf("opening index: %w", err)
	}
	names := slices.Sorted(maps.Keys(declared))
	draft := &lockfile.V2{
		Version: lockfile.SchemaV2,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{},
		},
		Packages: make(map[string]lockfile.V2Package, len(names)),
	}
	arts := make([]fetchArt, 0, len(names))
	plat := currentPlatform()
	for _, name := range names {
		pin := stripNumericRevision(declared[name])
		got, ver, err := sess.Resolve(ctx, name, pin)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving %s: %w", name, err)
		}
		key := name + "@" + got
		draft.Targets.Default.Roots = append(draft.Targets.Default.Roots, key)
		v2Arts, err := v2ArtifactsFromIndex(name, ver.Artifacts, sess.Commit)
		if err != nil {
			return nil, nil, err
		}
		draft.Packages[key] = lockfile.V2Package{Artifacts: v2Arts}
		art, ok := ver.Artifacts[plat]
		if !ok {
			return nil, nil, fmt.Errorf(
				"%w: %s %s", errAdoptNoPlatform, key, plat,
			)
		}
		arts = append(arts, fetchArt{Name: name, Version: got, Art: art})
	}
	if _, err := pkgsFromV2Lock(draft); err != nil {
		return nil, nil, err
	}
	return draft, arts, nil
}

func planMigrate(
	ctx context.Context, src index.Source, declared map[string]string,
) (adoptPlan, error) {
	sess, err := index.Open(ctx, src)
	if err != nil {
		return adoptPlan{}, fmt.Errorf("opening index: %w", err)
	}
	names := slices.Sorted(maps.Keys(declared))
	draft := &lockfile.V2{
		Version: lockfile.SchemaV2,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{},
		},
		Packages: make(map[string]lockfile.V2Package, len(names)),
	}
	plan := adoptPlan{Draft: draft, Commit: sess.Commit}
	plat := currentPlatform()
	for _, name := range names {
		pin := stripNumericRevision(declared[name])
		got, err := resolveAdoptPin(ctx, sess, name, pin)
		if err != nil {
			return adoptPlan{}, err
		}
		if got.Kind == adoptKindDrop {
			plan.Drops = append(plan.Drops, adoptDrop{
				Name: name, Pin: pin, Reason: got.Version,
			})
			continue
		}
		art, ok := got.Doc.Artifacts[plat]
		if !ok {
			plan.Drops = append(plan.Drops, adoptDrop{
				Name: name, Pin: pin, Reason: "no " + plat,
			})
			continue
		}
		if got.Kind == adoptKindBump {
			plan.Bumps = append(plan.Bumps, adoptBump{
				Name: name, From: pin, To: got.Version,
			})
		}
		key := name + "@" + got.Version
		draft.Targets.Default.Roots = append(draft.Targets.Default.Roots, key)
		v2Arts, err := v2ArtifactsFromIndex(name, got.Doc.Artifacts, sess.Commit)
		if err != nil {
			return adoptPlan{}, err
		}
		draft.Packages[key] = lockfile.V2Package{Artifacts: v2Arts}
		plan.Arts = append(plan.Arts, fetchArt{Name: name, Version: got.Version, Art: art})
	}
	if _, err := pkgsFromV2Lock(draft); err != nil {
		return adoptPlan{}, err
	}
	return plan, nil
}

const (
	adoptKindKeep = iota
	adoptKindBump
	adoptKindDrop
)

type adoptResolve struct {
	Version string
	Doc     index.Version
	Kind    int
}

func resolveAdoptPin(
	ctx context.Context, sess *index.Session, name, pin string,
) (adoptResolve, error) {
	got, ver, err := sess.Resolve(ctx, name, pin)
	if err == nil {
		return adoptResolve{Version: got, Doc: ver, Kind: adoptKindKeep}, nil
	}
	if errors.Is(err, index.ErrNotFound) {
		return adoptResolve{Version: "not in index", Kind: adoptKindDrop}, nil
	}
	if pin == "" {
		return adoptResolve{}, fmt.Errorf("resolving %s: %w", name, err)
	}
	latest, ver, lerr := sess.Resolve(ctx, name, "")
	if lerr == nil {
		return adoptResolve{Version: latest, Doc: ver, Kind: adoptKindBump}, nil
	}
	if errors.Is(lerr, index.ErrNotFound) {
		return adoptResolve{Version: "not in index", Kind: adoptKindDrop}, nil
	}
	return adoptResolve{}, fmt.Errorf("resolving %s: %w", name, err)
}

func printAdoptDiff(w io.Writer, old []string, plan adoptPlan) {
	if w == nil {
		w = os.Stdout
	}
	fmt.Fprintf(w, "index_commit %s\n", plan.Commit)
	for _, d := range plan.Drops {
		fmt.Fprintf(w, "- %s (%s)\n", d.Name, d.Reason)
	}
	for _, b := range plan.Bumps {
		fmt.Fprintf(w, "~ %s %s -> %s\n", b.Name, b.From, b.To)
	}
	oldSet := make(map[string]struct{}, len(old))
	for _, r := range old {
		oldSet[r] = struct{}{}
	}
	newRoots := append([]string(nil), plan.Draft.Targets.Default.Roots...)
	slices.Sort(newRoots)
	newSet := make(map[string]struct{}, len(newRoots))
	for _, r := range newRoots {
		newSet[r] = struct{}{}
	}
	for _, r := range slices.Sorted(maps.Keys(oldSet)) {
		if _, ok := newSet[r]; !ok {
			fmt.Fprintln(w, "- "+r)
		}
	}
	for _, r := range newRoots {
		if _, ok := oldSet[r]; !ok {
			fmt.Fprintln(w, "+ "+r)
		}
	}
}

func adoptProceedPrompt(plan adoptPlan) string {
	var parts []string
	if n := len(plan.Drops); n > 0 {
		parts = append(parts, fmt.Sprintf("Drop %d packages from gale.toml", n))
	}
	if n := len(plan.Bumps); n > 0 {
		parts = append(parts, fmt.Sprintf("bump %d pins", n))
	}
	parts = append(parts, fmt.Sprintf("fetch %d", len(plan.Arts)))
	parts = append(parts, "write v2, swap current. Proceed? [y/N]")
	s := strings.Join(parts, ", ")
	return strings.ToUpper(s[:1]) + s[1:]
}

func applyAdoptManifest(path string, plan adoptPlan) (prior, wrote config.FileState, err error) {
	first := true
	for _, d := range plan.Drops {
		b, a, rerr := config.RemovePackageSections(
			path, locatePackageSections(path, d.Name), d.Name,
		)
		if rerr != nil {
			return prior, wrote, fmt.Errorf("dropping %s: %w", d.Name, rerr)
		}
		if first {
			prior = b
			first = false
		}
		wrote = a
	}
	for _, b := range plan.Bumps {
		w, uerr := config.UpsertPackageWitnessed(path, "", b.Name, b.To)
		if uerr != nil {
			return prior, wrote, fmt.Errorf("bumping %s: %w", b.Name, uerr)
		}
		if first {
			prior = w.Before
			first = false
		}
		wrote = w.After
	}
	return prior, wrote, nil
}
