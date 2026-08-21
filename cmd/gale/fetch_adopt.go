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
	"github.com/spf13/cobra"

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

var (
	errAdoptCI      = errors.New("gale fetch-adopt refuses CI")
	errAdoptNeedYes = errors.New(
		"gale fetch-adopt requires --yes when stdin is not a TTY",
	)
	errAdoptAborted   = errors.New("gale fetch-adopt aborted")
	errAdoptAlreadyV2 = errors.New(
		"gale fetch-adopt refuses an existing v2 lock",
	)
	errAdoptHosts = errors.New(
		"gale fetch-adopt refuses host overlays",
	)
	errAdoptNoPlatform = errors.New(
		"gale fetch-adopt: no current-platform artifact",
	)
	errAdoptLockMoved = errors.New(
		"gale fetch-adopt: lock changed after the printed diff",
	)
)

var fetchAdoptCmd = &cobra.Command{
	Use:   "fetch-adopt",
	Short: "Plan a fetch lock from gale.toml and publish it unused",
	Long: "Resolve every default-target root against one index " +
		"commit, print a lock diff, and after confirmation stage " +
		"fetch trees, write a v2 lock, and swap current last. " +
		"Other commands refuse that lock until fetch is the " +
		"installer. Not the installer.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateScopeFlags(adoptGlobal, adoptProject); err != nil {
			return err
		}
		c, err := newCmdContext("", adoptGlobal, adoptProject)
		if err != nil {
			return err
		}
		src := index.Source{}
		if adoptIndex != "" {
			src.Dir = adoptIndex
		}
		return runFetchAdopt(cmd.Context(), c, adoptReq{
			Source: src,
			Yes:    adoptYes,
			DryRun: dryRun,
			TTY:    adoptTTY(),
			In:     cmd.InOrStdin(),
			Out:    cmd.OutOrStdout(),
			Err:    cmd.ErrOrStderr(),
		})
	},
}

func init() {
	fetchAdoptCmd.Flags().BoolVarP(&adoptGlobal, "global", "g",
		false, "Adopt the global config")
	fetchAdoptCmd.Flags().BoolVarP(&adoptProject, "project", "p",
		false, "Adopt the project config")
	fetchAdoptCmd.Flags().BoolVar(&adoptYes, "yes",
		false, "Skip the confirmation prompt")
	fetchAdoptCmd.Flags().StringVar(&adoptIndex, "index",
		"", "Resolve against a local index checkout")
	rootCmd.AddCommand(fetchAdoptCmd)
}

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

	draft, arts, err := planAdopt(ctx, req.Source, declared)
	if err != nil {
		return err
	}
	printAdoptDiff(req.Out, oldRoots, draft)
	if adoptAfterDiff != nil {
		adoptAfterDiff()
	}
	if req.DryRun {
		return nil
	}
	if !req.Yes {
		if !req.TTY {
			return errAdoptNeedYes
		}
		if req.Err == nil {
			req.Err = os.Stderr
		}
		fmt.Fprint(req.Err, "Proceed? [y/N] Install still uses recipes; live Load still rejects the v2 lock.\n")
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
	return finalizeFetch(ctx, c, fetchPublish{
		Arts:    arts,
		Lock:    draft,
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
	})
}

func refuseHostOverlays(cfg *config.GaleConfig) error {
	for _, h := range cfg.Hosts {
		if len(h.Packages) > 0 {
			return errAdoptHosts
		}
	}
	return nil
}

func oldLockRoots(lp string, snap FileSnapshot) ([]string, error) {
	if !snap.Exists {
		return nil, nil
	}
	if _, err := lockfile.ReadV2(lp); err == nil {
		return nil, errAdoptAlreadyV2
	}
	doc, err := lockfile.ReadV1(lp)
	if err != nil {
		return nil, fmt.Errorf("reading lockfile: %w", err)
	}
	if len(doc.Targets.Host) > 0 {
		return nil, errAdoptHosts
	}
	if doc.Targets.Default == nil {
		return nil, nil
	}
	return append([]string(nil), doc.Targets.Default.Roots...), nil
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
		draft.Packages[key] = lockfile.V2Package{
			Artifacts: v2ArtifactsFromIndex(ver.Artifacts, sess.Commit),
		}
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

func printAdoptDiff(w io.Writer, old []string, draft *lockfile.V2) {
	if w == nil {
		w = os.Stdout
	}
	var commit string
	for _, pkg := range draft.Packages {
		for _, art := range pkg.Artifacts {
			commit = art.IndexCommit
			break
		}
		if commit != "" {
			break
		}
	}
	fmt.Fprintf(w, "index_commit %s\n", commit)
	oldSet := make(map[string]struct{}, len(old))
	for _, r := range old {
		oldSet[r] = struct{}{}
	}
	newRoots := append([]string(nil), draft.Targets.Default.Roots...)
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
