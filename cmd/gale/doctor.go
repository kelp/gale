package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/generation"
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

		ctx := &doctorContext{
			galeDir:    galeDir,
			storeRoot:  defaultStoreRoot(),
			cwd:        cwd,
			globalPkgs: map[string]string{},
			projPkgs:   map[string]string{},
			out:        out,
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
