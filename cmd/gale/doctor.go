package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kelp/gale/internal/attestation"
	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/store"
	"github.com/spf13/cobra"
)

var (
	doctorRepair bool

	// doctorForce lets --repair rebuild a scope whose lock is
	// present and cannot be modeled. Repair refuses such a scope
	// otherwise, since it cannot tell whether the generation it is
	// about to publish matches the lock (gh#197) — but a machine
	// with an unrepairable lock is exactly where repair is run, so
	// the refusal needs a way past it.
	doctorForce bool

	// doctorCheckRegistry gates the network-touching checks
	// (stale-installs deps resolution, orphan runtime-dep
	// expansion) behind an explicit opt-in. Default is off so
	// `gale doctor` is airplane-mode-clean: no HTTP requests,
	// no cache writes under ~/.gale/cache/. Pins
	// audit/readonly/read-only-invariant/0002 and
	// network-perf/0004.
	doctorCheckRegistry bool
)

// cappedList builds a message body listing items, capped at
// 5. header is the opening line. If len(items) > 5, an
// overflow line "... N more" is appended. footer is appended
// after the list when non-empty (e.g. a remediation command).
func cappedList(header string, items []string, footer string) string {
	const maxShown = 5
	msg := header
	shown := items
	if len(shown) > maxShown {
		shown = shown[:maxShown]
	}
	for _, item := range shown {
		msg += "\n  " + item
	}
	if len(items) > maxShown {
		msg += fmt.Sprintf("\n  ... %d more", len(items)-maxShown)
	}
	if footer != "" {
		msg += "\n  " + footer
	}
	return msg
}

// doctorContext holds resolved state shared across checks.
type doctorContext struct {
	galeDir    string
	storeRoot  string
	cwd        string
	globalPkgs map[string]string
	projPkgs   map[string]string
	installed  []store.InstalledPackage
	store      *store.Store
	out        *output.Output
	// cmdCtx is set from the top-level cobra command so
	// checks can resolve recipes for staleness detection.
	cmdCtx *cmdContext
}

// doctorCheck is a single health check.
type doctorCheck struct {
	name string
	run  func(ctx *doctorContext) bool // true = passed
}

// doctorScope is one scope a doctor run covers: the gale dir whose
// generation, lock and farm it owns, and the manifest that declares
// its packages.
type doctorScope struct {
	label      string
	galeDir    string
	configPath string
}

// doctorScopes enumerates the scopes a run covers: the global one
// always, plus the project the cwd resolves to when that is not the
// global manifest reached from under ~/.gale (gh#96).
//
// One enumeration for the reports and for the repair. A check that
// disagreed with repair about which scopes exist would report a state
// --repair never touches, or stay silent about one it does.
func doctorScopes(ctx *doctorContext) ([]doctorScope, error) {
	scopes := []doctorScope{{
		label:      "global",
		galeDir:    ctx.galeDir,
		configPath: filepath.Join(ctx.galeDir, "gale.toml"),
	}}
	projConfig, err := projectConfigPath(ctx.cwd)
	if err != nil {
		// No project manifest resolves from the cwd, which is the
		// ordinary case: global is the whole run.
		return scopes, nil //nolint:nilerr // absence is an answer, not a failure
	}
	if configInGaleDir(projConfig, ctx.galeDir) {
		// From under ~/.gale the "project" config IS the global one
		// already listed, and deriving a gale dir from it would name
		// the bogus <galeDir>/.gale (gh#96).
		return scopes, nil
	}
	projGaleDir, err := galeDirForConfig(projConfig)
	if err != nil {
		return nil, fmt.Errorf("resolving project gale dir: %w", err)
	}
	return append(scopes, doctorScope{
		label:      "project",
		galeDir:    projGaleDir,
		configPath: projConfig,
	}), nil
}

// doctorStore returns the run's store handle, creating one when a
// check is driven before checkStore has resolved it.
func doctorStore(ctx *doctorContext) *store.Store {
	if ctx.store != nil {
		return ctx.store
	}
	return store.NewStore(ctx.storeRoot)
}

var doctorChecks = []doctorCheck{
	{"gale home", checkGaleHome},
	{"global config", checkGlobalConfig},
	{"project config", checkProjectConfig},
	{"host overrides", checkHostOverrides},
	{"lockfiles", checkLegacyLockfile},
	{"store", checkStore},
	{"dependency metadata", checkDepsMetadata},
	{"packages installed", checkPackagesInstalled},
	{"sync state", checkSyncState},
	{"generation", checkGeneration},
	{"symlinks", checkSymlinks},
	{"shadowed executables", checkShadowedProviders},
	{"shadowed files", checkShadowedFiles},
	{"revision drift", checkRevisionDrift},
	{"lib farm", checkFarm},
	{"stale installs", checkStaleInstalls},
	{"PATH", checkPATH},
	{"direnv", checkDirenvIntegration},
	{"orphans", checkOrphans},
	{"sigstore trust root", checkSigstoreRoot},
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check your gale installation for problems",
	// ExactArgs(0) over NoArgs: NoArgs emits the confusing
	// "unknown command" message for a stray positional, but
	// doctor has no subcommands. ExactArgs(0) keeps the
	// error literal: "accepts 0 arg(s), received 1".
	Args: cobra.ExactArgs(0),
	RunE: func(cmd *cobra.Command, args []string) error {
		galeDir, err := galeConfigDir()
		if err != nil {
			fmt.Fprintln(cmd.ErrOrStderr(),
				"xxx Cannot find home directory")
			return err
		}
		cwd, _ := os.Getwd()
		return runDoctor(&doctorIO{
			galeDir: galeDir,
			cwd:     cwd,
			stdout:  cmd.OutOrStdout(),
			stderr:  cmd.ErrOrStderr(),
		})
	},
}

// doctorIO bundles the writers and resolved paths a doctor
// run needs. Extracted so tests can drive runDoctor without
// going through cobra and can assert on stdout/stderr
// independently — the stdout summary is the user-facing
// "answer", per-check progress lines are on stderr.
type doctorIO struct {
	galeDir string
	cwd     string
	stdout  io.Writer
	stderr  io.Writer
}

// runDoctor executes every doctor check and writes a final
// summary block to stdout. Each check still emits its own
// success/warn/error line to stderr via *output.Output, so
// the existing color/TTY discipline is preserved.
// Stream discipline:
//   - stderr: per-check progress (==>, !!!, xxx). Human-
//     readable, suppressible via 2>/dev/null.
//   - stdout: one final summary line. "OK: N checks passed"
//     when everything is green, "PROBLEMS: M issue(s) of N
//     checks" otherwise. `gale doctor > status.txt` captures
//     the answer; exit code stays the programmatic signal.
func runDoctor(d *doctorIO) error {
	// Per-check output writer — uses the same TTY/color
	// resolution as the rest of the CLI, but redirected to
	// the provided stderr writer so tests can capture it.
	out := newOutputForWriter(d.stderr)

	// cmdCtx is best-effort — if it fails (e.g. no
	// recipes resolver), the stale-installs check
	// degrades gracefully.
	cmdCtx, _ := newCmdContext("", false, false)

	ctx := &doctorContext{
		galeDir:    d.galeDir,
		storeRoot:  defaultStoreRoot(),
		cwd:        d.cwd,
		globalPkgs: map[string]string{},
		projPkgs:   map[string]string{},
		out:        out,
		cmdCtx:     cmdCtx,
	}

	if doctorRepair {
		if err := repairDoctor(ctx); err != nil {
			fmt.Fprintln(d.stdout,
				"PROBLEMS: repair failed before checks ran")
			return fmt.Errorf("repair doctor state: %w", err)
		}
	}

	var failed int
	for _, check := range doctorChecks {
		if !check.run(ctx) {
			failed++
		}
	}
	total := len(doctorChecks)

	if failed > 0 {
		fmt.Fprintf(d.stdout,
			"PROBLEMS: %d issue(s) of %d checks\n", failed, total)
		return fmt.Errorf("doctor found problems")
	}
	fmt.Fprintf(d.stdout, "OK: %d checks passed\n", total)
	return nil
}

// checkGaleHome verifies ~/.gale/ exists.
func checkGaleHome(ctx *doctorContext) bool {
	if _, err := os.Stat(ctx.galeDir); err != nil {
		ctx.out.Error(
			"~/.gale/ does not exist\n  Run: gale install <pkg>",
		)
		return false
	}
	ctx.out.Success("Gale home (~/.gale/)")
	return true
}

// checkGlobalConfig parses the global gale.toml.
func checkGlobalConfig(ctx *doctorContext) bool {
	globalConfig := filepath.Join(ctx.galeDir, "gale.toml")
	data, err := os.ReadFile(globalConfig)
	if err != nil {
		ctx.out.Warn("No global gale.toml")
		return true // warn, not a failure
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		ctx.out.Error(fmt.Sprintf(
			"Global gale.toml parse error: %v", err,
		))
		return false
	}
	cfg.ApplyHost(config.CurrentHost())
	ctx.out.Success(fmt.Sprintf(
		"Global config (%d packages)", len(cfg.Packages),
	))
	ctx.globalPkgs = cfg.Packages
	return true
}

// checkProjectConfig parses a project gale.toml if present.
func checkProjectConfig(ctx *doctorContext) bool {
	projPath, err := config.FindGaleConfig(ctx.cwd)
	if err != nil {
		return true // no project config is fine
	}
	if configInGaleDir(projPath, ctx.galeDir) {
		// cwd is under the global gale home, so FindGaleConfig
		// resolved to the GLOBAL config — already covered by
		// checkGlobalConfig; reporting it again as "project"
		// would double-count its packages (gh#96).
		return true
	}
	data, err := os.ReadFile(projPath)
	if err != nil {
		return true
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		ctx.out.Error(fmt.Sprintf(
			"Project gale.toml parse error: %v", err,
		))
		return false
	}
	cfg.ApplyHost(config.CurrentHost())
	ctx.out.Success(fmt.Sprintf(
		"Project config (%d packages)", len(cfg.Packages),
	))
	ctx.projPkgs = cfg.Packages
	return true
}

// checkHostOverrides reports packages that appear in both
// shared [packages] and a matching [hosts.<host>.packages]
// overlay for the current machine. Host-wins is intentional
// (so per-machine version pins work) but easy to forget; the
// shared entry effectively becomes dead config. Warns so the
// user can decide whether to clean up; never fails.
func checkHostOverrides(ctx *doctorContext) bool {
	host := config.CurrentHost()
	overrides := loadHostOverrides(
		filepath.Join(ctx.galeDir, "gale.toml"), host,
	)
	if projPath, err := config.FindGaleConfig(ctx.cwd); err == nil &&
		!configInGaleDir(projPath, ctx.galeDir) {
		// configInGaleDir: from under ~/.gale the "project"
		// path IS the global config already counted above —
		// appending it again doubles every shadow (gh#96).
		overrides = append(overrides,
			loadHostOverrides(projPath, host)...)
	}
	if len(overrides) == 0 {
		ctx.out.Success("No host-override shadows")
		return true
	}
	ctx.out.Warn(cappedList(
		fmt.Sprintf("Host overlay shadows %d shared package(s):", len(overrides)),
		overrides,
		"(host overlay wins — remove shared entry or the overlay to silence)",
	))
	return true
}

// loadHostOverrides returns formatted "<name>: shared
// <v1> overridden by [hosts.<key>] <v2>" lines for every
// shared package that a matching host overlay shadows for
// host. Returns nil for missing or unparseable files —
// other checks already surface those failures.
func loadHostOverrides(configPath, host string) []string {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		return nil
	}
	if host == "" || len(cfg.Hosts) == 0 || len(cfg.Packages) == 0 {
		return nil
	}
	var lines []string
	for name, sharedVer := range cfg.Packages {
		for key, h := range cfg.Hosts {
			hostVer, ok := h.Packages[name]
			if !ok {
				continue
			}
			if !config.HostKeyMatches(key, host) {
				continue
			}
			lines = append(lines, fmt.Sprintf(
				"%s: shared %s overridden by [hosts.%s] %s",
				name, sharedVer, key, hostVer,
			))
		}
	}
	sort.Strings(lines)
	return lines
}

// checkLegacyLockfile reports a scope whose lockfile this build
// cannot use as a version selector — a legacy one above all, which
// every gale before enforcement wrote.
//
// The state is not cosmetic. gc and `doctor --repair` refuse such a
// scope's rebuild (#226), and a project's activation gate refuses its
// PATH on the next cd, so a machine can be stuck with no command
// saying why.
//
// The question goes to lockedRebuildPkgs, the function those rebuilds
// decide by. Asking it rather than re-reading the lock is what keeps
// the report from blessing a lock the next rebuild refuses.
func checkLegacyLockfile(ctx *doctorContext) bool {
	scopes, err := doctorScopes(ctx)
	if err != nil {
		ctx.out.Warn(fmt.Sprintf("lockfile check skipped: %v", err))
		return true
	}
	host := config.CurrentHost()
	var unusable []string
	for _, s := range scopes {
		lockPath, pErr := lockfilePath(s.configPath)
		if pErr != nil {
			unusable = append(unusable,
				fmt.Sprintf("%s: %v", s.label, pErr))
			continue
		}
		if _, _, lErr := lockedRebuildPkgs(lockPath, host); lErr != nil {
			unusable = append(unusable,
				fmt.Sprintf("%s: %v", s.label, lErr))
		}
	}
	if len(unusable) == 0 {
		ctx.out.Success("Lockfiles usable")
		return true
	}
	ctx.out.Error(cappedList(
		fmt.Sprintf("Unusable lockfile in %d scope(s):", len(unusable)),
		unusable,
		"Run: gale lock --refresh (or gale doctor --repair --force "+
			"to rebuild without it)",
	))
	return false
}

// checkStore verifies the package store is readable.
func checkStore(ctx *doctorContext) bool {
	ctx.store = store.NewStore(ctx.storeRoot)
	installed, err := ctx.store.List()
	if err != nil {
		ctx.out.Error(fmt.Sprintf("Store error: %v", err))
		return false
	}
	ctx.installed = installed
	ctx.out.Success(fmt.Sprintf(
		"Store (%d versions in %s)", len(installed), ctx.storeRoot,
	))
	return true
}

// checkDepsMetadata reports store directories whose .gale-deps.toml
// is present and cannot be read strictly.
//
// One such directory fails the strict farm-claim walk machine-wide:
// every install and every removal in every scope refuses until it
// clears, and none of the obvious repairs work. Deleting the metadata
// file alone is worse than the corruption — the lenient reader then
// returns an empty closure and the claim silently shrinks — and a
// reinstall never rewrites it, because an install over an existing
// store dir returns cached before it writes anything.
//
// The escape is a deletion, which is why it is reported here and
// performed only under --repair. StateAbsent is deliberately not
// reported: a missing record is a pre-metadata install, and offering
// to delete every one of those would be the fail-open case in
// reverse.
func checkDepsMetadata(ctx *doctorContext) bool {
	scan, err := scanDepsMeta(doctorStore(ctx))
	if err != nil {
		// checkStore reports an unreadable store; a second error
		// here would double-count the same failure.
		ctx.out.Warn(fmt.Sprintf("dependency metadata check skipped: %v", err))
		return true
	}
	if len(scan.unusable) == 0 {
		ctx.out.Success("Dependency metadata readable")
		return true
	}
	ctx.out.Error(cappedList(
		fmt.Sprintf(
			"Unusable dependency metadata (%d store dir(s)):",
			len(scan.unusable),
		),
		scan.unusable,
		"Run: gale doctor --repair (deletes them and every package "+
			"directory on a dependency path to them), then gale sync",
	))
	return false
}

// depsMetaScan is what one pass over the store can say about its
// recorded dependency closures.
type depsMetaScan struct {
	// pkgOf maps a store dir to the package that owns it, so a
	// deletion names the identity the store resolves rather than a
	// path parsed back into one.
	pkgOf map[string]store.InstalledPackage
	// dependents maps a store dir to the dirs whose records name it.
	// Reverse edges, because the repair walks up from the corruption
	// to everything that would still depend on it.
	dependents map[string][]string
	// unusable lists the dirs whose record is present and cannot be
	// read strictly, sorted.
	unusable []string
}

// scanDepsMeta reads every installed store dir's dependency record.
//
// ReadStrict, not Read: the answer authorizes a deletion, and the
// lenient reader collapses "no dependencies" into "I cannot tell" —
// the one distinction a destructive decision rests on.
func scanDepsMeta(s *store.Store) (*depsMetaScan, error) {
	installed, err := s.List()
	if err != nil {
		return nil, fmt.Errorf("list store: %w", err)
	}
	scan := &depsMetaScan{
		pkgOf:      make(map[string]store.InstalledPackage, len(installed)),
		dependents: make(map[string][]string, len(installed)),
	}
	for _, pkg := range installed {
		scan.pkgOf[filepath.Join(s.Root, pkg.Name, pkg.Version)] = pkg
	}
	for dir := range scan.pkgOf {
		deps, state := depsmeta.ReadStrict(dir)
		switch state {
		case depsmeta.StateUnusable:
			scan.unusable = append(scan.unusable, dir)
		case depsmeta.StateRecorded:
			for _, dep := range deps {
				// By bare version through the canonical resolver, the
				// way the farm walk resolves a recorded dep (gh#172).
				depDir := s.ResolveDir(dep.Name, dep.Version)
				scan.dependents[depDir] = append(scan.dependents[depDir], dir)
			}
		case depsmeta.StateAbsent:
			// A pre-metadata install records nothing and depends on
			// nothing this scan can see. Not corruption.
		}
	}
	sort.Strings(scan.unusable)
	return scan, nil
}

// purgeSet returns the store dirs a repair must delete: every dir
// whose record is unusable, plus every dir that reaches one of them
// through a recorded dependency path.
//
// The dependents go because sync would not restore them otherwise.
// It evaluates declared roots alone, and an install over an existing
// dir returns cached before it installs any dep, so one surviving
// directory anywhere on the chain stops the repair descending: for
// A -> B -> C, deleting C alone leaves A cached and C never comes
// back. Deleting the whole path — the declared package at the top of
// it included — is what makes the reinstall walk all the way down.
//
// Only dirs the store enumerated are ever returned, so nothing
// outside <storeRoot>/<name>/<version> can be named for deletion.
func (scan *depsMetaScan) purgeSet() []string {
	queue := append([]string(nil), scan.unusable...)
	seen := make(map[string]bool, len(queue))
	for _, dir := range queue {
		seen[dir] = true
	}
	var out []string
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		out = append(out, dir)
		for _, dependent := range scan.dependents[dir] {
			if seen[dependent] {
				continue
			}
			seen[dependent] = true
			queue = append(queue, dependent)
		}
	}
	sort.Strings(out)
	return out
}

// purgeUnusableDepsMeta performs the deletion checkDepsMetadata
// reports, and returns the store dirs it removed.
//
// It runs after the generations have been rebuilt, never before: the
// rebuild refuses a scope whose lock cannot be modeled (#226), and a
// refusal that has already destroyed store directories would leave
// the machine worse than it found it. The caller rebuilds again once
// anything is gone, so no generation is left linking a deleted dir.
func purgeUnusableDepsMeta(ctx *doctorContext) ([]string, error) {
	s := doctorStore(ctx)
	scan, err := scanDepsMeta(s)
	if err != nil {
		return nil, err
	}
	if len(scan.unusable) == 0 {
		return nil, nil
	}
	purged := scan.purgeSet()
	for _, dir := range purged {
		pkg := scan.pkgOf[dir]
		if err := s.Remove(pkg.Name, pkg.Version); err != nil {
			return nil, fmt.Errorf(
				"remove %s@%s: %w", pkg.Name, pkg.Version, err,
			)
		}
		ctx.out.Warn(fmt.Sprintf(
			"Removed %s@%s (%s)", pkg.Name, pkg.Version, dir,
		))
	}
	ctx.out.Success(fmt.Sprintf(
		"Purged %d store dir(s) with unusable dependency metadata; "+
			"run gale sync to reinstall them", len(purged),
	))
	return purged, nil
}

// checkPackagesInstalled verifies all declared packages are
// present in the store.
func checkPackagesInstalled(ctx *doctorContext) bool {
	allPkgs := map[string]string{}
	for k, v := range ctx.globalPkgs {
		allPkgs[k] = v
	}
	for k, v := range ctx.projPkgs {
		allPkgs[k] = v
	}
	var missing []string
	for name, version := range allPkgs {
		if ctx.store != nil && !ctx.store.IsInstalled(name, version) {
			missing = append(missing, name+"@"+version)
		}
	}
	if len(missing) > 0 {
		// Surface both remediations: `gale sync` for the
		// "never installed" case and `gale remove` for the
		// "tried to remove but config still lists it" case.
		// Doctor can't tell which one the user wants, so it
		// shows both.
		names := make([]string, 0, len(missing))
		for _, m := range missing {
			names = append(names,
				strings.SplitN(m, "@", 2)[0])
		}
		ctx.out.Error(fmt.Sprintf(
			"Missing packages: %s\n"+
				"  Run: gale sync          (to reinstall)\n"+
				"  Or:  gale remove %s (to delete from config)",
			strings.Join(missing, ", "),
			strings.Join(names, " "),
		))
		return false
	}
	if len(allPkgs) > 0 {
		ctx.out.Success("All packages installed")
	}
	return true
}

// checkSyncState reports a scope whose sync completion stamp records
// that the last sync gave up, naming the packages it gave up on
// (gh#221).
//
// Until now the stamp was read by `gale sync --if-needed` alone, which
// prints its one line during an activation. A user who wants to know
// why a binary is missing from PATH had to cd out and back in to see
// it. The state is exactly what doctor exists to surface: recorded, not
// obvious, and cleared by one command.
//
// It is advisory — the check always passes. The environment really is
// short a package, but the exit code for that is already
// checkPackagesInstalled's, and a second red line for one condition
// double-counts it. The stamp is also a record of a past run rather
// than a fact about the environment now: a package installed by hand
// after the failure leaves it reading "incomplete" until the next sync,
// and failing there would turn doctor red on a machine that is fine.
// That is the unfixable-red shape gh#50 and gh#219 both rejected.
//
// A stale fingerprint is deliberately not reported. Whether the stamp
// still describes the current manifest and lock is the question
// `sync --if-needed` is built to answer, and answering it again here
// would flag a comment-only edit of gale.toml as a problem.
func checkSyncState(ctx *doctorContext) bool {
	scopes, err := doctorScopes(ctx)
	if err != nil {
		ctx.out.Warn(fmt.Sprintf("sync state check skipped: %v", err))
		return true
	}
	for _, s := range scopes {
		reportScopeSyncState(ctx, s)
	}
	return true
}

// reportScopeSyncState is checkSyncState for one scope. Each scope
// stamps beside the generation it describes — <project>/.gale for a
// project, ~/.gale globally — so the answer is per-scope by
// construction.
//
// The four on-disk states stay four. gh#186 draws the missing/
// unreadable line deliberately: a machine that has never synced under
// this gale has no stamp and no finding, while one whose stamp cannot
// be read has a file worth looking at. Collapsing them would invent a
// problem on every fresh install or hide a corrupt one.
//
// The incomplete report is doctor's own text rather than
// incompleteNotice's. That line offers the wait — "or wait up to 10m
// for the next automatic attempt" — because an activation printed it
// while withholding a retry. A user reading doctor has already decided
// to look, so the report names the escape hatch alone.
func reportScopeSyncState(ctx *doctorContext, s doctorScope) {
	st, err := readSyncState(s.galeDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		ctx.out.Success(fmt.Sprintf("No sync recorded (%s)", s.label))
	case err != nil:
		ctx.out.Warn(fmt.Sprintf(
			"Sync state unreadable (%s): %v\n"+
				"  Run: gale sync (rewrites the stamp)",
			s.label, err,
		))
	case st.Status == syncStatusIncomplete:
		ctx.out.Warn(cappedList(
			fmt.Sprintf(
				"The last sync did not complete (%s, recorded %s):",
				s.label, st.RecordedAt.Format(time.RFC3339),
			),
			st.Failed,
			"Run: gale sync (retries now, ignoring the retry interval)",
		))
	default:
		ctx.out.Success(fmt.Sprintf("Last sync completed (%s)", s.label))
	}
}

// checkGeneration verifies an active generation exists and
// its directory is on disk. Resolve (not Current) is used so
// a dangling current symlink — where the target gen
// directory has been deleted — surfaces as a hard error.
// Doctor exists specifically to catch this case; Current
// alone only Readlinks the symlink and parses the trailing
// integer, which is why the bug went undetected for so long.
func checkGeneration(ctx *doctorContext) bool {
	gen, target, err := generation.Resolve(ctx.galeDir)
	if err != nil {
		// Target missing or symlink unparseable. Surface the
		// raw error so the user can see what's wrong, and
		// point at the only safe remediation.
		ctx.out.Error(fmt.Sprintf(
			"Generation broken: %v\n  Run: gale sync", err,
		))
		return false
	}
	if gen == 0 {
		ctx.out.Error(
			"No active generation\n  Run: gale sync",
		)
		return false
	}
	ctx.out.Success(fmt.Sprintf(
		"Generation (current -> %s)", target,
	))
	return true
}

// checkSymlinks verifies no broken symlinks in current/bin.
func checkSymlinks(ctx *doctorContext) bool {
	binDir := filepath.Join(ctx.galeDir, "current", "bin")
	entries, err := os.ReadDir(binDir)
	if err != nil {
		return true // no bin dir is handled by other checks
	}
	var broken []string
	for _, e := range entries {
		link := filepath.Join(binDir, e.Name())
		if _, err := os.Stat(link); err != nil {
			broken = append(broken, e.Name())
		}
	}
	if len(broken) > 0 {
		ctx.out.Error(fmt.Sprintf(
			"Broken symlinks: %s\n  Run: gale sync",
			strings.Join(broken, ", "),
		))
		return false
	}
	ctx.out.Success(fmt.Sprintf(
		"Symlinks intact (%d binaries)", len(entries),
	))
	return true
}

// checkShadowedProviders reports the executable-name collisions a
// rebuild of each scope would refuse: two declared packages shipping
// the same bin/ basename, with no [bin] winner naming one of them.
//
// Nothing clears this state by itself. gc and `doctor --repair`
// rebuild the same generation from the same manifest and hit the same
// refusal, so until the user edits [bin] every command that touches a
// generation fails on it — which is exactly when doctor is run.
//
// The verdict comes from generation.BinArbiter, the arbiter the
// rebuild itself decides by. gh#190 exported it for this: a doctor
// that restated the rule would drift from it, reporting collisions
// gale accepts or missing ones it refuses.
func checkShadowedProviders(ctx *doctorContext) bool {
	scopes, err := doctorScopes(ctx)
	if err != nil {
		ctx.out.Warn(fmt.Sprintf(
			"shadowed-executable check skipped: %v", err,
		))
		return true
	}
	ok := true
	for _, s := range scopes {
		if !checkScopeShadowedProviders(ctx, s) {
			ok = false
		}
	}
	return ok
}

// checkScopeShadowedProviders is checkShadowedProviders for one
// scope. Scopes are judged separately because each builds its own
// generation from its own manifest, so a collision is a collision
// only among the packages one scope declares.
func checkScopeShadowedProviders(
	ctx *doctorContext, s doctorScope,
) bool {
	cfg, err := loadEffectiveConfig(s.configPath)
	if err != nil {
		// checkGlobalConfig and checkProjectConfig already report an
		// unreadable manifest.
		return true
	}
	if err := shadowedProviders(
		cfg.Packages, cfg.Bin, ctx.storeRoot,
	); err != nil {
		ctx.out.Error(fmt.Sprintf("%s scope: %v", s.label, err))
		return false
	}
	ctx.out.Success(fmt.Sprintf(
		"No shadowed executables (%s)", s.label,
	))
	return true
}

// shadowedProviders offers the arbiter every bin/ entry the packages
// in pkgs would contribute to a generation, in the order a rebuild
// offers them, and returns its verdict.
//
// The enumeration mirrors populateGeneration: packages in sorted
// name order, so the same claimant wins a contested name, and only
// the top-level files of bin/, since a rebuild recurses into a bin/
// subdirectory without arbitrating it. The RULE — who wins, what
// counts as a collision, and what the user is told — is the
// arbiter's alone.
//
// A package with no store dir, or none with a bin/, contributes
// nothing: a rebuild skips it with a warning rather than failing
// (gh#68), and doctor's own checks report the missing install.
func shadowedProviders(
	pkgs, overrides map[string]string, storeRoot string,
) error {
	s := store.NewStore(storeRoot)
	bins := generation.NewBinArbiter(overrides)
	for _, name := range slices.Sorted(maps.Keys(pkgs)) {
		binDir := filepath.Join(s.ResolveDir(name, pkgs[name]), "bin")
		entries, err := os.ReadDir(binDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			bins.Claim(name, e.Name())
		}
	}
	return bins.Err()
}

// checkShadowedFiles reports the man pages and root-level files more
// than one declared package provides. It is advisory: it always
// passes, so it never changes doctor's exit code.
//
// The verdict is a report because the state is legitimate. Two
// packages shipping man/man1/foo.1 is expected — a library and its
// CLI, a compat shim — and gh#190's argument for refusing bin/ does
// not survive the move: a shadowed man page shows the wrong docs, it
// does not run the wrong program. Refusing would reject setups that
// have always worked, and warning on every activation is the trap
// gh#190 already rejected. doctor is the home for a state worth
// knowing about that nothing needs to act on (gh#219).
func checkShadowedFiles(ctx *doctorContext) bool {
	scopes, err := doctorScopes(ctx)
	if err != nil {
		ctx.out.Warn(fmt.Sprintf(
			"shadowed-file check skipped: %v", err,
		))
		return true
	}
	for _, s := range scopes {
		reportScopeShadowedFiles(ctx, s)
	}
	return true
}

// reportScopeShadowedFiles is checkShadowedFiles for one scope.
// Scopes are judged separately because each builds its own generation
// from its own manifest, so a shadowed path is shadowed only among
// the packages one scope declares.
func reportScopeShadowedFiles(ctx *doctorContext, s doctorScope) {
	cfg, err := loadEffectiveConfig(s.configPath)
	if err != nil {
		// checkGlobalConfig and checkProjectConfig already report an
		// unreadable manifest.
		return
	}
	shadowed := generation.ShadowedFiles(cfg.Packages, ctx.storeRoot)
	if len(shadowed) == 0 {
		ctx.out.Success(fmt.Sprintf("No shadowed files (%s)", s.label))
		return
	}
	items := make([]string, 0, len(shadowed))
	for _, c := range shadowed {
		items = append(items, fmt.Sprintf(
			"%s: %s shadows %s", c.Path, c.Existing, c.Incoming,
		))
	}
	ctx.out.Warn(cappedList(
		fmt.Sprintf(
			"%d shadowed file(s) in %s scope "+
				"(man pages and root-level files provided by "+
				"more than one package):",
			len(shadowed), s.label,
		),
		items,
		"the first package in sorted order provides it; "+
			"remove one provider to change which",
	))
}

// checkRevisionDrift compares each declared package's
// active-generation symlink target against the revision a fresh
// Build would pick. A mismatch means the gen carries a stale
// link to an older revision while a higher one exists in the
// store — the silent corruption case behind the gen/308
// regression. validateGenerationSymlinks accepts these because
// the stale targets still resolve; only this check surfaces
// them. Repair: `gale doctor --repair`, which rebuilds the gen
// from current config + store state.
func checkRevisionDrift(ctx *doctorContext) bool {
	if len(ctx.globalPkgs) == 0 {
		ctx.out.Success("Revision drift (no global packages declared)")
		return true
	}
	actual, err := generation.CurrentVersionsStrict(
		ctx.galeDir, ctx.storeRoot,
	)
	if err != nil {
		// Report it, and name the path (gh#210). This check exists
		// to surface silent generation corruption, so a green line
		// over a generation that could not be enumerated is the one
		// answer it must never give. Under the lenient reader it did
		// not even reach this branch: an unwalkable generation came
		// back empty, every declared package was skipped as missing,
		// and the check printed "Revision drift (none)".
		//
		// Deferring to checkGeneration is not enough either. That
		// check catches a current pointer whose target will not
		// stat; a walk failing deeper in the tree resolves fine and
		// leaves nothing above to see.
		//
		// It reports and returns — it never aborts the run. This is
		// the thirteenth of nineteen checks, and stopping here would
		// suppress PATH, direnv, orphans and the sigstore trust root
		// on exactly the machine doctor exists to diagnose.
		ctx.out.Error(fmt.Sprintf(
			"Revision drift unknown: %v\n  Run: gale sync", err,
		))
		return false
	}
	expected := generation.ActiveVersions(ctx.globalPkgs, ctx.storeRoot)
	var drift []string
	for name, want := range expected {
		got, ok := actual[name]
		if !ok {
			// checkPackagesInstalled handles the missing case.
			continue
		}
		if got != want {
			drift = append(drift, fmt.Sprintf(
				"%s: gen has %s, store has %s", name, got, want,
			))
		}
	}
	if len(drift) == 0 {
		ctx.out.Success("Revision drift (none)")
		return true
	}
	sort.Strings(drift)
	ctx.out.Error(cappedList(
		fmt.Sprintf(
			"Revision drift in current generation (%d package(s))",
			len(drift),
		),
		drift,
		"Run: gale doctor --repair",
	))
	return false
}

// checkFarm verifies each shared dylib farm is in sync
// with the generation built from its own scope: the global
// farm (~/.gale/lib/) against global packages, and the
// project farm (<proj>/.gale/lib/) against project packages
// when a project config exists. Scopes are checked
// separately — generation.Build populates each farm from
// its per-scope package set, so validating the global farm
// against merged global+project packages reported false
// drift that `gale doctor --repair` could never fix (#50).
// Older revisions still on disk (awaiting `gale gc`) are
// out of scope — they aren't on PATH and aren't in the
// farm by design.
func checkFarm(ctx *doctorContext) bool {
	ok := checkFarmScope(ctx, ctx.globalPkgs)
	if projPath, err := config.FindGaleConfig(ctx.cwd); err == nil &&
		!configInGaleDir(projPath, ctx.galeDir) {
		// configInGaleDir: when cwd is under the global gale
		// home, FindGaleConfig resolves to the GLOBAL
		// gale.toml; deriving a project dir from it would
		// yield the bogus <galeDir>/.gale and report drift
		// --repair can never fix (gh#96). The global farm
		// was already checked above.
		if _, dirErr := galeDirForConfig(projPath); dirErr == nil &&
			!checkFarmScope(ctx, ctx.projPkgs) {
			ok = false
		}
	}
	return ok
}

// checkFarmScope validates the SHARED farm against one scope's
// store-dir set: config packages plus the transitive runtime-dep
// closure from .gale-deps.toml. Checking the config set alone is
// blind to a farm missing dep dylibs — the exact breakage
// FarmStoreDirs exists to prevent (gh#43).
//
// One farm, checked once per scope. It used to read
// <galeDir>/lib, so a project run inspected a directory nothing
// resolves through and reported a clean farm however broken the
// real one was. Both scopes now read the store-derived shared farm
// and each asserts its own closure is present there (design
// revision 6, section 4).
func checkFarmScope(
	ctx *doctorContext, pkgs map[string]string,
) bool {
	farmDir := farm.DirFromStoreRoot(ctx.storeRoot)
	active := generation.FarmStoreDirs(pkgs, ctx.storeRoot)
	reportContestedAliases(ctx, active)
	issues, err := farm.CheckDrift(active, farmDir)
	if err != nil {
		ctx.out.Error(fmt.Sprintf("Farm check failed: %v", err))
		return false
	}
	if len(issues) == 0 {
		ctx.out.Success(fmt.Sprintf(
			"Lib farm (%s)", farmDir,
		))
		return true
	}
	ctx.out.Error(cappedList(
		fmt.Sprintf("Lib farm drift (%d issue(s))", len(issues)),
		issues,
		"Run: gale doctor --repair",
	))
	return false
}

// reportContestedAliases warns about unversioned aliases the
// shared farm had to drop because more than one package in the
// closure provides them (openssl and openssl4 both ship
// libssl.dylib). A binary that recorded the unversioned name
// cannot resolve it through the farm while both are installed.
//
// Reported, never failed, and deliberately outside CheckDrift's
// issue list: those render as an Error telling the user to run
// `gale doctor --repair`, and no repair can clear a collision —
// the farm is already doing the only safe thing, and holding
// both packages is supported. Failing here would be gh#50's
// unfixable-drift shape all over again.
func reportContestedAliases(ctx *doctorContext, active []string) {
	_, conflicts, err := farm.FarmableAliases(active)
	if err != nil || len(conflicts) == 0 {
		// An enumeration error is CheckDrift's to report; it
		// reads the same directories a moment later.
		return
	}
	items := make([]string, 0, len(conflicts))
	for _, c := range conflicts {
		items = append(items, fmt.Sprintf(
			"%s — provided by %s",
			c.Name, strings.Join(c.Owners, " and "),
		))
	}
	ctx.out.Warn(cappedList(
		fmt.Sprintf(
			"Unversioned aliases not farmed (%d)", len(conflicts),
		),
		items,
		"Binaries recording these names resolve them only via "+
			"per-dep rpaths; rebuild them against one provider.",
	))
}

// checkStaleInstalls reports installed packages whose
// built-against dep closure no longer matches the current
// recipes for those deps. A stale install means one of
// its deps had a revision/version bump since the install
// happened; the package should be reinstalled to pick up
// the new dep artifacts.
//
// This check hits the recipe registry to look up the
// current dep versions. It is gated behind
// --check-registry so the default `gale doctor` run is
// airplane-mode-clean (no HTTP, no cache writes). Pins
// audit/readonly/read-only-invariant/0002 and
// network-perf/0004.
func checkStaleInstalls(ctx *doctorContext) bool {
	if !doctorCheckRegistry {
		ctx.out.Success(
			"Stale installs (skipped — pass --check-registry to probe)",
		)
		return true
	}
	if ctx.cmdCtx == nil || ctx.store == nil {
		// Can't resolve recipes without a cmd context.
		ctx.out.Success("Stale installs (skipped — no context)")
		return true
	}
	var stale []string
	for _, pkg := range ctx.installed {
		storeDir, ok := ctx.store.StorePath(pkg.Name, pkg.Version)
		if !ok {
			continue
		}
		// Missing .gale-deps.toml means the install predates
		// the revision system. Flag it stale without needing
		// the recipe, so old installs whose version is no
		// longer in the registry's .versions index still
		// surface as soft-migration candidates.
		if !depsmeta.Has(storeDir) {
			stale = append(stale, pkg.Name+"@"+pkg.Version)
			continue
		}
		r, err := ctx.cmdCtx.ResolveVersionedRecipe(
			pkg.Name, pkg.Version,
		)
		if err != nil {
			continue
		}
		isStale, err := installer.IsStale(
			storeDir, r, runtime.GOOS, runtime.GOARCH, ctx.cmdCtx.Resolver,
		)
		if err != nil {
			continue
		}
		if isStale {
			stale = append(stale, pkg.Name+"@"+pkg.Version)
		}
	}
	if len(stale) == 0 {
		ctx.out.Success("No stale installs")
		return true
	}
	ctx.out.Warn(cappedList(
		fmt.Sprintf(
			"Stale installs (%d) — deps changed since built:",
			len(stale),
		),
		stale,
		"Run: gale sync (reinstalls stale packages)",
	))
	// Warn, not fail — staleness is common during recipe
	// development and auto-resolves on next sync.
	return true
}

// checkPATH verifies ~/.gale/current/bin is on PATH.
func checkPATH(ctx *doctorContext) bool {
	galeBin := filepath.Join(ctx.galeDir, "current", "bin")
	pathDirs := strings.Split(os.Getenv("PATH"), ":")
	found := false
	for _, d := range pathDirs {
		if d == galeBin {
			found = true
			break
		}
	}
	if !found {
		ctx.out.Error(fmt.Sprintf(
			"PATH missing %s\n  Add to shell config: "+
				"export PATH=\"%s:$PATH\"",
			galeBin, galeBin,
		))
		return false
	}
	ctx.out.Success(fmt.Sprintf(
		"PATH includes %s", galeBin,
	))
	return true
}

// checkDirenvIntegration checks direnv setup when .envrc exists.
func checkDirenvIntegration(ctx *doctorContext) bool {
	if _, err := os.Stat(
		filepath.Join(ctx.cwd, ".envrc"),
	); err != nil {
		return true // no .envrc, skip
	}

	// Check direnv installed.
	path := os.Getenv("PATH")
	direnvFound := false
	for _, d := range strings.Split(path, ":") {
		p := filepath.Join(filepath.Clean(d), "direnv")
		if _, err := os.Stat(p); err == nil { //nolint:gosec // PATH dirs are trusted
			direnvFound = true
			break
		}
	}
	if !direnvFound {
		ctx.out.Error("direnv not found in PATH\n  " +
			"Run: gale install direnv")
		return false
	}

	// Check use_gale is defined.
	home, _ := os.UserHomeDir()
	direnvrc := filepath.Join(home, ".config", "direnv", "direnvrc")
	if data, err := os.ReadFile(direnvrc); err == nil {
		if strings.Contains(string(data), "use_gale") ||
			strings.Contains(string(data), "gale hook direnv") {
			ctx.out.Success("Direnv integration configured")
			return true
		}
	}
	// Also check ~/.direnvrc.
	if data, err := os.ReadFile(
		filepath.Join(home, ".direnvrc"),
	); err == nil {
		if strings.Contains(string(data), "use_gale") ||
			strings.Contains(string(data), "gale hook direnv") {
			ctx.out.Success("Direnv integration configured")
			return true
		}
	}

	ctx.out.Error("use_gale not found in direnvrc\n  " +
		"Run: echo 'eval \"$(gale hook direnv)\"' >> " +
		direnvrc)
	return false
}

// checkOrphans reports orphaned package versions. Walks the
// same retention set that gc uses — config + runtime-dep
// closure — so a package kept alive by a runtime dep of an
// active config entry is not reported as orphaned.
//
// Runtime-dep expansion calls the resolver, which hits the
// registry. We only thread the resolver through when
// --check-registry is set so the default run stays offline.
// Without it, a runtime-dep keepalive may be misreported as
// orphaned; the user can rerun with --check-registry or
// `gale gc --dry-run` to get the network-accurate count.
func checkOrphans(ctx *doctorContext) bool {
	globalConfig := filepath.Join(ctx.galeDir, "gale.toml")
	var projPath string
	if p, err := config.FindGaleConfig(ctx.cwd); err == nil &&
		!configInGaleDir(p, ctx.galeDir) {
		// configInGaleDir: from under ~/.gale this would pass
		// the global config as projPath. The referenced set
		// dedupes, so it happened to be benign — but only by
		// accident (gh#96).
		projPath = p
	}
	var resolver installer.RecipeResolver
	var pinResolve versionedRecipeResolver
	if doctorCheckRegistry && ctx.cmdCtx != nil {
		resolver = ctx.cmdCtx.Resolver
		pinResolve = ctx.cmdCtx.versionedRecipeResolver()
	}
	referenced, err := collectReferencedPackagesWithResolver(
		filepath.Dir(globalConfig), projPath,
		ctx.store, resolver, pinResolve,
	)
	if err != nil {
		// A count computed from a partial reference set would
		// name live packages as orphans (gh#188). Report the
		// gap instead of the number.
		ctx.out.Warn(fmt.Sprintf(
			"orphan count unavailable: %v", err,
		))
		return true
	}

	var orphaned int
	for _, pkg := range ctx.installed {
		if !referenced[pkg.Name+"@"+pkg.Version] {
			orphaned++
		}
	}
	if orphaned > 0 {
		ctx.out.Warn(fmt.Sprintf(
			"%d orphaned version(s) (run gale gc)", orphaned,
		))
	}
	return true // orphans are a warning, not a failure
}

// checkSigstoreRoot reports the state of the Sigstore TUF cache
// used by attestation verification. Informational only: it always
// returns true because verification falls back to the embedded
// trusted-root snapshot when the cache is missing or stale, so no
// cache state is a doctor failure.
func checkSigstoreRoot(ctx *doctorContext) bool {
	if override := os.Getenv(attestation.TrustedRootEnv); override != "" {
		// The override path is user-supplied by design; doctor
		// only reports whether it exists.
		if _, err := os.Stat(override); err != nil { //nolint:gosec
			ctx.out.Warn(fmt.Sprintf(
				"%s override set but file missing: %s\n  "+
					"attestation verification will fail until it exists",
				attestation.TrustedRootEnv, override,
			))
			return true
		}
		ctx.out.Warn(fmt.Sprintf(
			"%s override active: %s\n  attestation verification "+
				"trusts this root instead of the Sigstore TUF root",
			attestation.TrustedRootEnv, override,
		))
		return true
	}

	cacheDir := attestation.TUFCacheDir()
	info, err := os.Stat(cacheDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		ctx.out.Warn("sigstore TUF cache not yet populated — " +
			"will fetch on first sigstore install")
	case err != nil:
		ctx.out.Warn(fmt.Sprintf(
			"sigstore TUF cache unreadable: %v", err,
		))
	case !info.IsDir():
		ctx.out.Warn(fmt.Sprintf(
			"sigstore TUF cache is not a directory: %s", cacheDir,
		))
	case time.Since(newestModTime(cacheDir)) > 24*time.Hour:
		ctx.out.Warn("sigstore TUF cache stale — " +
			"will refresh on next verification")
	default:
		ctx.out.Success(
			"sigstore trust root cached and fresh " +
				"(attestation verification)",
		)
	}
	return true // informational, never a failure
}

// newestModTime returns the newest modification time of any file
// under dir (the dir itself when empty or unwalkable). Used to
// judge TUF cache freshness: the TUF client rewrites metadata files
// in place, so the directory mtime alone can under-report.
func newestModTime(dir string) time.Time {
	var newest time.Time
	if info, err := os.Stat(dir); err == nil {
		newest = info.ModTime()
	}
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // freshness is best-effort
		}
		if info, ierr := d.Info(); ierr == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	return newest
}

// repairDoctor rebuilds every scope's generation from what is
// declared and installed, clears the store directories whose
// dependency metadata cannot be read, and re-signs what remains.
//
// The versions come from the scope's lock whenever it has a usable
// one. Repair passes no pin resolver, so an unlocked rebuild takes
// store.ResolveDir's bare→highest-revision answer — which after a
// rolled-back install is an orphan the lock does not name, put on
// PATH by the command a user runs to fix things (gh#197). Unlike gc,
// repair rebuilds even when nothing about the version selection
// changes: a generation linking the right versions through broken
// symlinks, or over a stale farm, is exactly what it exists to fix.
func repairDoctor(ctx *doctorContext) error {
	if err := repairGenerations(ctx); err != nil {
		return err
	}
	purged, err := purgeUnusableDepsMeta(ctx)
	if err != nil {
		return err
	}
	if len(purged) > 0 {
		// A generation may link what was just deleted, and only a
		// second rebuild can drop those entries. Rebuilding before
		// the purge would not do: a scope whose lock cannot be
		// modeled refuses its rebuild, and a refusal that had
		// already destroyed store directories leaves the machine
		// worse than it found it.
		if err := repairGenerations(ctx); err != nil {
			return err
		}
	}
	return resignInstalled(ctx)
}

// repairGenerations rebuilds each scope's generation under that
// scope's lock. Unchanged from what --repair has always done; it is
// its own function so the deps-metadata purge can run between two
// rebuilds.
func repairGenerations(ctx *doctorContext) error {
	scopes, err := doctorScopes(ctx)
	if err != nil {
		return err
	}
	opt := recoveryRebuild{force: doctorForce, out: ctx.out}
	for _, s := range scopes {
		if err := rebuildUnderLock(genRebuild{
			galeDir:    s.galeDir,
			storeRoot:  ctx.storeRoot,
			configPath: s.configPath,
		}, opt); err != nil {
			return fmt.Errorf("rebuild %s generation: %w", s.label, err)
		}
	}
	ctx.out.Success("Repaired Gale generations")
	return nil
}

// resignInstalled re-signs Mach-Os in every installed package.
// Pre-fix installs (before f00f2b7) may carry unsigned binaries that
// SIGKILL on Apple Silicon. EnsureCodeSigned is a no-op on Linux and
// on already-signed binaries, so this is safe to run unconditionally
// on every package.
func resignInstalled(ctx *doctorContext) error {
	s := store.NewStore(ctx.storeRoot)
	installed, err := s.List()
	if err != nil {
		return fmt.Errorf("list store: %w", err)
	}
	for _, pkg := range installed {
		storeDir, ok := s.StorePath(pkg.Name, pkg.Version)
		if !ok {
			continue
		}
		if err := build.EnsureCodeSigned(storeDir); err != nil {
			return fmt.Errorf(
				"ensure code signed %s@%s: %w",
				pkg.Name, pkg.Version, err,
			)
		}
	}
	ctx.out.Success(fmt.Sprintf(
		"Re-signed Mach-Os in %d package(s)", len(installed),
	))
	return nil
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorRepair, "repair", false,
		"Repair active generations from current config and store")
	doctorCmd.Flags().BoolVar(&doctorForce, "force", false,
		"With --repair, rebuild a scope whose lockfile cannot be used")
	doctorCmd.Flags().BoolVar(&doctorCheckRegistry, "check-registry", false,
		"Probe the recipe registry for stale-install and "+
			"orphan-dep diagnosis (off by default — implies network access)")
	rootCmd.AddCommand(doctorCmd)
}
