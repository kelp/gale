package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/store"
	"github.com/spf13/cobra"
)

var (
	doctorRepair bool

	// doctorCheckRegistry gates the network-touching checks
	// (stale-installs deps resolution, orphan runtime-dep
	// expansion) behind an explicit opt-in. Default is off so
	// `gale doctor` is airplane-mode-clean: no HTTP requests,
	// no cache writes under ~/.gale/cache/. Pins
	// audit/readonly/read-only-invariant/0002 and
	// network-perf/0004.
	doctorCheckRegistry bool
)

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

var doctorChecks = []doctorCheck{
	{"gale home", checkGaleHome},
	{"global config", checkGlobalConfig},
	{"project config", checkProjectConfig},
	{"host overrides", checkHostOverrides},
	{"store", checkStore},
	{"packages installed", checkPackagesInstalled},
	{"generation", checkGeneration},
	{"symlinks", checkSymlinks},
	{"revision drift", checkRevisionDrift},
	{"lib farm", checkFarm},
	{"stale installs", checkStaleInstalls},
	{"PATH", checkPATH},
	{"direnv", checkDirenvIntegration},
	{"orphans", checkOrphans},
	{"gh CLI", checkGhCLI},
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
	const maxShown = 5
	shown := overrides
	if len(shown) > maxShown {
		shown = shown[:maxShown]
	}
	msg := fmt.Sprintf(
		"Host overlay shadows %d shared package(s):", len(overrides),
	)
	for _, line := range shown {
		msg += "\n  " + line
	}
	if len(overrides) > maxShown {
		msg += fmt.Sprintf("\n  ... %d more",
			len(overrides)-maxShown)
	}
	msg += "\n  (host overlay wins — remove shared entry or " +
		"the overlay to silence)"
	ctx.out.Warn(msg)
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
	actual, err := generation.CurrentVersions(ctx.galeDir, ctx.storeRoot)
	if err != nil {
		// checkGeneration already surfaces a broken current
		// symlink; stay quiet here to avoid a double-error.
		ctx.out.Success(
			"Revision drift (current generation unreadable; see above)",
		)
		return true
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
	const maxShown = 5
	msg := fmt.Sprintf(
		"Revision drift in current generation (%d package(s))",
		len(drift),
	)
	shown := drift
	if len(shown) > maxShown {
		shown = shown[:maxShown]
	}
	for _, d := range shown {
		msg += "\n  " + d
	}
	if len(drift) > maxShown {
		msg += fmt.Sprintf(
			"\n  ... %d more", len(drift)-maxShown,
		)
	}
	msg += "\n  Run: gale doctor --repair"
	ctx.out.Error(msg)
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
	ok := checkFarmScope(ctx, ctx.galeDir, ctx.globalPkgs)
	if projPath, err := config.FindGaleConfig(ctx.cwd); err == nil &&
		!configInGaleDir(projPath, ctx.galeDir) {
		// configInGaleDir: when cwd is under the global gale
		// home, FindGaleConfig resolves to the GLOBAL
		// gale.toml; deriving a project dir from it would
		// yield the bogus <galeDir>/.gale and report drift
		// --repair can never fix (gh#96). The global farm
		// was already checked above.
		projGaleDir, dirErr := galeDirForConfig(projPath)
		if dirErr == nil &&
			!checkFarmScope(ctx, projGaleDir, ctx.projPkgs) {
			ok = false
		}
	}
	return ok
}

// checkFarmScope validates one farm (galeDir/lib) against
// the same store-dir set generation.Build uses to populate
// it: config packages plus the transitive runtime-dep
// closure from .gale-deps.toml. Checking the config set
// alone is blind to a farm missing dep dylibs — the exact
// breakage FarmStoreDirs exists to prevent (gh#43).
func checkFarmScope(
	ctx *doctorContext, galeDir string, pkgs map[string]string,
) bool {
	farmDir := farm.Dir(galeDir)
	active := generation.FarmStoreDirs(pkgs, ctx.storeRoot)
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
	// Cap the printed list so a very out-of-sync farm
	// doesn't flood output.
	const maxShown = 5
	shown := issues
	if len(shown) > maxShown {
		shown = shown[:maxShown]
	}
	msg := fmt.Sprintf("Lib farm drift (%d issue(s))", len(issues))
	for _, i := range shown {
		msg += "\n  " + i
	}
	if len(issues) > maxShown {
		msg += fmt.Sprintf("\n  ... %d more",
			len(issues)-maxShown)
	}
	msg += "\n  Run: gale doctor --repair"
	ctx.out.Error(msg)
	return false
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
		if !installer.HasDepsMetadata(storeDir) {
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
			storeDir, r, ctx.cmdCtx.Resolver,
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
	const maxShown = 5
	shown := stale
	if len(shown) > maxShown {
		shown = shown[:maxShown]
	}
	msg := fmt.Sprintf(
		"Stale installs (%d) — deps changed since built:",
		len(stale),
	)
	for _, s := range shown {
		msg += "\n  " + s
	}
	if len(stale) > maxShown {
		msg += fmt.Sprintf("\n  ... %d more", len(stale)-maxShown)
	}
	msg += "\n  Run: gale sync (reinstalls stale packages)"
	ctx.out.Warn(msg)
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
	if doctorCheckRegistry && ctx.cmdCtx != nil {
		resolver = ctx.cmdCtx.Resolver
	}
	referenced := collectReferencedPackagesWithResolver(
		filepath.Dir(globalConfig), projPath,
		ctx.store, resolver, ctx.out,
	)

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

// checkGhCLI checks for the gh CLI (attestation verification).
func checkGhCLI(ctx *doctorContext) bool {
	if _, err := exec.LookPath("gh"); err != nil {
		ctx.out.Warn("gh CLI not found — attestation " +
			"verification disabled\n  " +
			"Install: https://cli.github.com")
		return true // warn, not a failure
	}
	ctx.out.Success(
		"gh CLI available (attestation verification)",
	)
	return true
}

func repairDoctor(ctx *doctorContext) error {
	globalConfig := filepath.Join(ctx.galeDir, "gale.toml")
	if err := rebuildGeneration(ctx.galeDir, ctx.storeRoot, globalConfig); err != nil {
		return fmt.Errorf("rebuild global generation: %w", err)
	}
	if projConfig, err := projectConfigPath(ctx.cwd); err == nil &&
		!configInGaleDir(projConfig, ctx.galeDir) {
		// configInGaleDir: from under ~/.gale the "project"
		// config IS the global one rebuilt above; treating it
		// as a project would CREATE the bogus ~/.gale/.gale
		// directory on disk (gh#96).
		projGaleDir, dirErr := galeDirForConfig(projConfig)
		if dirErr != nil {
			return fmt.Errorf("resolving project gale dir: %w", dirErr)
		}
		if err := rebuildGeneration(projGaleDir, ctx.storeRoot, projConfig); err != nil {
			return fmt.Errorf("rebuild project generation: %w", err)
		}
	}
	ctx.out.Success("Repaired Gale generations")

	// Re-sign Mach-Os in every installed package. Pre-fix
	// installs (before f00f2b7) may carry unsigned binaries
	// that SIGKILL on Apple Silicon. EnsureCodeSigned is a
	// no-op on Linux and on already-signed binaries, so this
	// is safe to run unconditionally on every package.
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
	doctorCmd.Flags().BoolVar(&doctorCheckRegistry, "check-registry", false,
		"Probe the recipe registry for stale-install and "+
			"orphan-dep diagnosis (off by default — implies network access)")
	rootCmd.AddCommand(doctorCmd)
}
