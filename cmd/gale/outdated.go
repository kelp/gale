package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

var outdatedRecipes string

// outdatedItem represents a package with a newer version.
type outdatedItem struct {
	Name    string
	Current string
	Latest  string
}

var outdatedCmd = &cobra.Command{
	Use:   "outdated",
	Short: "Show packages with newer versions available",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		out := newCmdOutput(cmd)

		ctx, err := newCmdContext(outdatedRecipes, false, false)
		if err != nil {
			return err
		}

		cfg, err := ctx.LoadConfig()
		if err != nil {
			return err
		}

		if len(cfg.Packages) == 0 {
			out.Info("No packages installed.")
			return nil
		}

		var items []outdatedItem
		for name, version := range cfg.Packages {
			r, err := ctx.Resolver(name)
			if err != nil {
				out.Warn(fmt.Sprintf(
					"Skipping %s: %v", name, err))
				continue
			}

			if versionNewer(r.Package.Version, version) {
				items = append(items, outdatedItem{
					Name:    name,
					Current: version,
					Latest:  r.Package.Version,
				})
			}
		}

		if len(items) == 0 {
			out.Success("Everything is up to date.")
			return nil
		}

		for _, line := range formatOutdated(items) {
			fmt.Println(line)
		}

		return nil
	},
}

// formatOutdated formats outdated items as lines of text.
func formatOutdated(items []outdatedItem) []string {
	lines := make([]string, len(items))
	for i, item := range items {
		lines[i] = fmt.Sprintf("%s %s → %s",
			item.Name, item.Current, item.Latest)
	}
	return lines
}

// versionNewer reports whether registry is a newer semver
// than current. Returns false for non-semver strings or
// when registry is equal or older.
func versionNewer(registry, current string) bool {
	rParts, rOK := parseSemver(registry)
	cParts, cOK := parseSemver(current)
	if !rOK || !cOK {
		return false
	}
	for i := 0; i < 3; i++ {
		if rParts[i] > cParts[i] {
			return true
		}
		if rParts[i] < cParts[i] {
			return false
		}
	}
	return false
}

// parseSemver extracts major, minor, patch from a version
// string. Strips a leading "v" if present. Returns false
// if the string is not a valid semver triple.
func parseSemver(v string) ([3]int, bool) {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var result [3]int
	for i, p := range parts {
		// Strip pre-release suffix (e.g. "1-rc1").
		if dash := strings.IndexByte(p, '-'); dash >= 0 {
			p = p[:dash]
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return [3]int{}, false
		}
		result[i] = n
	}
	return result, true
}

func init() {
	outdatedCmd.Flags().StringVar(&outdatedRecipes, "recipes", "",
		"Use local recipes directory (default: ../gale-recipes/)")
	outdatedCmd.Flags().Lookup("recipes").NoOptDefVal = "auto"
	rootCmd.AddCommand(outdatedCmd)
}
