package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/kelp/gale/internal/inspect"
	"github.com/kelp/gale/internal/recipe"
	"github.com/spf13/cobra"
)

var (
	inspectAll     bool
	inspectJSON    bool
	inspectRecipes string
)

var inspectCmd = &cobra.Command{
	Use:   "inspect [package]",
	Short: "Audit installed packages for linkage issues",
	Long: `Walk installed packages and report linkage problems: unresolvable
@rpath references, stale rpath entries, and mismatches between a
binary's actual dep references and the recipe's declared deps.

Exits nonzero if any issues are found so CI can gate on this.

Issue kinds:
  unresolvable-ref   binary references @rpath/libX but no rpath resolves it
  stale-rpath        rpath entry points to a path that doesn't exist
  undeclared-dep     binary references a gale-store package not in the recipe
  over-declared-dep  recipe declares a runtime dep no binary references
  version-skew       two binaries reference different versions of the same dep`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInspect(cmd, args)
	},
}

func init() {
	inspectCmd.Flags().BoolVar(&inspectAll, "all", false,
		"Scan every installed package in the store")
	inspectCmd.Flags().BoolVar(&inspectJSON, "json", false,
		"Emit machine-readable JSON instead of text")
	inspectCmd.Flags().StringVar(&inspectRecipes, "recipes", "",
		"Use local recipes directory (default: ../gale-recipes/)")
	inspectCmd.Flags().Lookup("recipes").NoOptDefVal = "auto"
	rootCmd.AddCommand(inspectCmd)
}

func runInspect(cmd *cobra.Command, args []string) error {
	if inspectAll && len(args) > 0 {
		return fmt.Errorf("--all cannot be combined with a package name")
	}
	if !inspectAll && len(args) == 0 {
		return fmt.Errorf(
			"specify a package name or pass --all\n" +
				"  usage: gale inspect <package>  |  gale inspect --all")
	}

	ctx, err := newCmdContext(inspectRecipes, false, false)
	if err != nil {
		return fmt.Errorf("creating context: %w", err)
	}

	var targets []target
	if inspectAll {
		pkgs, err := ctx.Installer.Store.List()
		if err != nil {
			return fmt.Errorf("list store: %w", err)
		}
		for _, p := range pkgs {
			targets = append(targets, target{
				name:    p.Name,
				version: p.Version,
			})
		}
	} else {
		name := args[0]
		pkgs, err := ctx.Installer.Store.List()
		if err != nil {
			return fmt.Errorf("list store: %w", err)
		}
		found := false
		for _, p := range pkgs {
			if p.Name == name {
				targets = append(targets, target{
					name:    p.Name,
					version: p.Version,
				})
				found = true
			}
		}
		if !found {
			return fmt.Errorf(
				"%s is not installed — nothing to inspect", name)
		}
	}

	sort.Slice(targets, func(i, j int) bool {
		if targets[i].name != targets[j].name {
			return targets[i].name < targets[j].name
		}
		return targets[i].version < targets[j].version
	})

	var allIssues []inspect.Issue
	for _, t := range targets {
		prefix := filepath.Join(
			ctx.StoreRoot, t.name, t.version)

		var r *recipe.Recipe
		if resolved, err := resolveVersionedRecipe(
			ctx, t.name, t.version); err == nil {
			r = resolved
		}
		// If recipe resolution fails, keep going — the
		// rpath-only checks don't need it.

		issues, err := inspect.ScanInstalled(
			prefix, t.name, t.version, r)
		if err != nil {
			return fmt.Errorf(
				"scanning %s@%s: %w", t.name, t.version, err)
		}
		allIssues = append(allIssues, issues...)
	}

	if inspectJSON {
		if allIssues == nil {
			// JSON consumers expect [] not null.
			allIssues = []inspect.Issue{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(allIssues); err != nil {
			return err
		}
	} else {
		printHumanIssues(cmd, allIssues, targets)
	}

	if len(allIssues) > 0 {
		os.Exit(1)
	}
	return nil
}

type target struct {
	name, version string
}

func printHumanIssues(
	cmd *cobra.Command,
	issues []inspect.Issue,
	scanned []target,
) {
	out := newCmdOutput(cmd)

	if len(issues) == 0 {
		if len(scanned) == 1 {
			out.Success(fmt.Sprintf(
				"%s@%s: no issues",
				scanned[0].name, scanned[0].version))
		} else {
			out.Success(fmt.Sprintf(
				"Scanned %d package(s): no issues",
				len(scanned)))
		}
		return
	}

	// Group by package+version.
	byPkg := map[string][]inspect.Issue{}
	var keys []string
	for _, iss := range issues {
		k := iss.Package + "@" + iss.Version
		if _, ok := byPkg[k]; !ok {
			keys = append(keys, k)
		}
		byPkg[k] = append(byPkg[k], iss)
	}
	sort.Strings(keys)

	for _, k := range keys {
		fmt.Println(k)
		for _, iss := range byPkg[k] {
			line := formatIssueLine(iss)
			if iss.Kind.Severity() == "error" {
				out.Error("  " + line)
			} else {
				out.Warn("  " + line)
			}
		}
	}

	fmt.Fprintf(os.Stderr,
		"\n%d issue(s) across %d package(s)\n",
		len(issues), len(byPkg))
}

func formatIssueLine(iss inspect.Issue) string {
	if iss.Binary != "" {
		return fmt.Sprintf("%s  %s  %s",
			iss.Kind, iss.Binary, iss.Details)
	}
	return fmt.Sprintf("%s  %s", iss.Kind, iss.Details)
}
