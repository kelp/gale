package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/store"
	"github.com/spf13/cobra"
)

var doctorRepair bool

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
	{"store", checkStore},
	{"packages installed", checkPackagesInstalled},
	{"generation", checkGeneration},
	{"symlinks", checkSymlinks},
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
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		out := newCmdOutput(cmd)

		galeDir, err := galeConfigDir()
		if err != nil {
			out.Error("Cannot find home directory")
			return err
		}

		cwd, _ := os.Getwd()

		// cmdCtx is best-effort — if it fails (e.g. no
		// recipes resolver), the stale-installs check
		// degrades gracefully.
		cmdCtx, _ := newCmdContext("", false, false)

		ctx := &doctorContext{
			galeDir:    galeDir,
			storeRoot:  defaultStoreRoot(),
			cwd:        cwd,
			globalPkgs: map[string]string{},
			projPkgs:   map[string]string{},
			out:        out,
			cmdCtx:     cmdCtx,
		}

		if doctorRepair {
			if err := repairDoctor(ctx); err != nil {
				return fmt.Errorf("repair doctor state: %w", err)
			}
		}

		var failed bool
		for _, check := range doctorChecks {
			if !check.run(ctx) {
				failed = true
			}
		}

		if failed {
			return fmt.Errorf("doctor found problems")
		}
		return nil
	},
}

// checkGaleHome verifies ~/.gale/ exists.
func checkGaleHome(ctx *doctorContext) bool {
	if _, err := os.Stat(ctx.galeDir); err != nil {
		ctx.out.Error(
			"~/.gale/ does not exist\n  Run: gale install <pkg>")
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
			"Global gale.toml parse error: %v", err))
		return false
	}
	ctx.out.Success(fmt.Sprintf(
		"Global config (%d packages)", len(cfg.Packages)))
	ctx.globalPkgs = cfg.Packages
	return true
}

// checkProjectConfig parses a project gale.toml if present.
func checkProjectConfig(ctx *doctorContext) bool {
	projPath, err := config.FindGaleConfig(ctx.cwd)
	if err != nil {
		return true // no project config is fine
	}
	data, err := os.ReadFile(projPath)
	if err != nil {
		return true
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		ctx.out.Error(fmt.Sprintf(
			"Project gale.toml parse error: %v", err))
		return false
	}
	ctx.out.Success(fmt.Sprintf(
		"Project config (%d packages)", len(cfg.Packages)))
	ctx.projPkgs = cfg.Packages
	return true
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
		"Store (%d versions in %s)", len(installed), ctx.storeRoot))
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
		ctx.out.Error(fmt.Sprintf(
			"Missing packages: %s\n  Run: gale sync",
			strings.Join(missing, ", ")))
		return false
	}
	if len(allPkgs) > 0 {
		ctx.out.Success("All packages installed")
	}
	return true
}

// checkGeneration verifies an active generation exists.
func checkGeneration(ctx *doctorContext) bool {
	gen, err := generation.Current(ctx.galeDir)
	if err != nil || gen == 0 {
		ctx.out.Error(
			"No active generation\n  Run: gale sync")
		return false
	}
	currentLink := filepath.Join(ctx.galeDir, "current")
	target, err := os.Readlink(currentLink)
	if err != nil {
		ctx.out.Error("current symlink broken")
		return false
	}
	ctx.out.Success(fmt.Sprintf(
		"Generation (current -> %s)", target))
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
			strings.Join(broken, ", ")))
		return false
	}
	ctx.out.Success(fmt.Sprintf(
		"Symlinks intact (%d binaries)", len(entries)))
	return true
}

// checkFarm verifies the shared dylib farm at
// ~/.gale/lib/ is in sync with the store.
func checkFarm(ctx *doctorContext) bool {
	farmDir := farm.Dir(ctx.galeDir)
	issues, err := farm.CheckDrift(ctx.storeRoot, farmDir)
	if err != nil {
		ctx.out.Error(fmt.Sprintf("Farm check failed: %v", err))
		return false
	}
	if len(issues) == 0 {
		ctx.out.Success(fmt.Sprintf(
			"Lib farm (%s)", farmDir))
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
func checkStaleInstalls(ctx *doctorContext) bool {
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
			pkg.Name, pkg.Version)
		if err != nil {
			continue
		}
		isStale, err := installer.IsStale(
			storeDir, r, ctx.cmdCtx.Resolver)
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
		len(stale))
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
			galeBin, galeBin))
		return false
	}
	ctx.out.Success(fmt.Sprintf(
		"PATH includes %s", galeBin))
	return true
}

// checkDirenvIntegration checks direnv setup when .envrc exists.
func checkDirenvIntegration(ctx *doctorContext) bool {
	if _, err := os.Stat(
		filepath.Join(ctx.cwd, ".envrc")); err != nil {
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
		filepath.Join(home, ".direnvrc")); err == nil {
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

// checkOrphans reports orphaned package versions.
func checkOrphans(ctx *doctorContext) bool {
	globalConfig := filepath.Join(ctx.galeDir, "gale.toml")
	referenced := map[string]bool{}
	mergeConfig(globalConfig, referenced, ctx.out)
	if projPath, err := config.FindGaleConfig(ctx.cwd); err == nil {
		mergeConfig(projPath, referenced, ctx.out)
	}
	var orphaned int
	for _, pkg := range ctx.installed {
		if !referenced[pkg.Name+"@"+pkg.Version] {
			orphaned++
		}
	}
	if orphaned > 0 {
		ctx.out.Warn(fmt.Sprintf(
			"%d orphaned version(s) (run gale gc)", orphaned))
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
		"gh CLI available (attestation verification)")
	return true
}

func repairDoctor(ctx *doctorContext) error {
	globalConfig := filepath.Join(ctx.galeDir, "gale.toml")
	if err := rebuildGeneration(ctx.galeDir, ctx.storeRoot, globalConfig); err != nil {
		return fmt.Errorf("rebuild global generation: %w", err)
	}
	if projConfig, err := projectConfigPath(ctx.cwd); err == nil {
		projDir := filepath.Dir(projConfig)
		projGaleDir := filepath.Join(projDir, ".gale")
		if err := rebuildGeneration(projGaleDir, ctx.storeRoot, projConfig); err != nil {
			return fmt.Errorf("rebuild project generation: %w", err)
		}
	}
	ctx.out.Success("Repaired Gale generations")
	return nil
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorRepair, "repair", false,
		"Repair active generations from current config and store")
	rootCmd.AddCommand(doctorCmd)
}
