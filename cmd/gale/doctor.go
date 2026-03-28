package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/store"
	"github.com/spf13/cobra"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check your gale installation for problems",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))
		var failed bool

		// 1. Gale home directory.
		galeDir, err := galeConfigDir()
		if err != nil {
			out.Error("Cannot find home directory")
			return err
		}
		if _, err := os.Stat(galeDir); err != nil {
			out.Error(
				"~/.gale/ does not exist\n  Run: gale install <pkg>")
			failed = true
		} else {
			out.Success("Gale home (~/.gale/)")
		}

		// 2. Global gale.toml.
		globalConfig := filepath.Join(galeDir, "gale.toml")
		globalPkgs := map[string]string{}
		if data, err := os.ReadFile(globalConfig); err != nil {
			out.Warn("No global gale.toml")
		} else if cfg, err := config.ParseGaleConfig(string(data)); err != nil {
			out.Error(fmt.Sprintf(
				"Global gale.toml parse error: %v", err))
			failed = true
		} else {
			out.Success(fmt.Sprintf(
				"Global config (%d packages)", len(cfg.Packages)))
			globalPkgs = cfg.Packages
		}

		// Project gale.toml.
		cwd, _ := os.Getwd()
		projectPkgs := map[string]string{}
		if projPath, err := config.FindGaleConfig(cwd); err == nil {
			if data, err := os.ReadFile(projPath); err == nil {
				if cfg, err := config.ParseGaleConfig(string(data)); err != nil {
					out.Error(fmt.Sprintf(
						"Project gale.toml parse error: %v", err))
					failed = true
				} else {
					out.Success(fmt.Sprintf(
						"Project config (%d packages)", len(cfg.Packages)))
					projectPkgs = cfg.Packages
				}
			}
		}

		// 3. Store exists.
		storeRoot := defaultStoreRoot()
		s := store.NewStore(storeRoot)
		installed, err := s.List()
		if err != nil {
			out.Error(fmt.Sprintf("Store error: %v", err))
			failed = true
		} else {
			out.Success(fmt.Sprintf(
				"Store (%d versions in %s)", len(installed), storeRoot))
		}

		// 4. Packages installed.
		allPkgs := map[string]string{}
		for k, v := range globalPkgs {
			allPkgs[k] = v
		}
		for k, v := range projectPkgs {
			allPkgs[k] = v
		}
		var missing []string
		for name, version := range allPkgs {
			if !s.IsInstalled(name, version) {
				missing = append(missing, name+"@"+version)
			}
		}
		if len(missing) > 0 {
			out.Error(fmt.Sprintf(
				"Missing packages: %s\n  Run: gale sync",
				strings.Join(missing, ", ")))
			failed = true
		} else if len(allPkgs) > 0 {
			out.Success("All packages installed")
		}

		// 5. Generation valid.
		gen, err := generation.Current(galeDir)
		if err != nil || gen == 0 {
			out.Error(
				"No active generation\n  Run: gale sync")
			failed = true
		} else {
			currentLink := filepath.Join(galeDir, "current")
			target, err := os.Readlink(currentLink)
			if err != nil {
				out.Error("current symlink broken")
				failed = true
			} else {
				out.Success(fmt.Sprintf(
					"Generation (current -> %s)", target))
			}
		}

		// 6. Symlinks intact.
		binDir := filepath.Join(galeDir, "current", "bin")
		if entries, err := os.ReadDir(binDir); err == nil {
			var broken []string
			for _, e := range entries {
				link := filepath.Join(binDir, e.Name())
				if _, err := os.Stat(link); err != nil {
					broken = append(broken, e.Name())
				}
			}
			if len(broken) > 0 {
				out.Error(fmt.Sprintf(
					"Broken symlinks: %s\n  Run: gale sync",
					strings.Join(broken, ", ")))
				failed = true
			} else {
				out.Success(fmt.Sprintf(
					"Symlinks intact (%d binaries)", len(entries)))
			}
		}

		// 7. PATH configured.
		galeBin := filepath.Join(galeDir, "current", "bin")
		pathDirs := strings.Split(os.Getenv("PATH"), ":")
		found := false
		for _, d := range pathDirs {
			if d == galeBin {
				found = true
				break
			}
		}
		if !found {
			out.Error(fmt.Sprintf(
				"PATH missing %s\n  Add to shell config: "+
					"export PATH=\"%s:$PATH\"",
				galeBin, galeBin))
			failed = true
		} else {
			out.Success(fmt.Sprintf(
				"PATH includes %s", galeBin))
		}

		// 8. Direnv integration.
		if _, err := os.Stat(
			filepath.Join(cwd, ".envrc")); err == nil {
			checkDirenv(out, &failed)
		}

		// 9. Orphaned versions.
		referenced := map[string]string{}
		mergeConfig(globalConfig, referenced)
		if projPath, err := config.FindGaleConfig(cwd); err == nil {
			mergeConfig(projPath, referenced)
		}
		var orphaned int
		for _, pkg := range installed {
			if referenced[pkg.Name] != pkg.Version {
				orphaned++
			}
		}
		if orphaned > 0 {
			out.Warn(fmt.Sprintf(
				"%d orphaned version(s) (run gale gc)", orphaned))
		}

		if failed {
			return fmt.Errorf("doctor found problems")
		}
		return nil
	},
}

func checkDirenv(out *output.Output, failed *bool) {
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
		out.Error("direnv not found in PATH\n  " +
			"Run: gale install direnv")
		*failed = true
		return
	}

	// Check use_gale is defined.
	home, _ := os.UserHomeDir()
	direnvrc := filepath.Join(home, ".config", "direnv", "direnvrc")
	if data, err := os.ReadFile(direnvrc); err == nil {
		if strings.Contains(string(data), "use_gale") ||
			strings.Contains(string(data), "gale hook direnv") {
			out.Success("Direnv integration configured")
			return
		}
	}
	// Also check ~/.direnvrc.
	if data, err := os.ReadFile(
		filepath.Join(home, ".direnvrc")); err == nil {
		if strings.Contains(string(data), "use_gale") ||
			strings.Contains(string(data), "gale hook direnv") {
			out.Success("Direnv integration configured")
			return
		}
	}

	out.Error("use_gale not found in direnvrc\n  " +
		"Run: echo 'eval \"$(gale hook direnv)\"' >> " +
		direnvrc)
	*failed = true
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}
