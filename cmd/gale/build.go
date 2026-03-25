package main

import (
	"fmt"
	"os"

	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/recipe"
	"github.com/spf13/cobra"
)

var buildCmd = &cobra.Command{
	Use:   "build <recipe.toml>",
	Short: "Build a package from source",
	Long:  "Build a package from a recipe file and produce a tar.zst archive.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		recipePath := args[0]
		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

		data, err := os.ReadFile(recipePath)
		if err != nil {
			return fmt.Errorf("reading recipe: %w", err)
		}

		r, err := recipe.Parse(string(data))
		if err != nil {
			return fmt.Errorf("parsing recipe: %w", err)
		}

		out.Info(fmt.Sprintf("Building %s@%s from source...",
			r.Package.Name, r.Package.Version))

		outputDir, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working dir: %w", err)
		}

		result, err := build.Build(r, outputDir)
		if err != nil {
			return fmt.Errorf("build failed: %w", err)
		}

		out.Success(fmt.Sprintf("Built %s", result.Archive))
		fmt.Printf("archive: %s\n", result.Archive)
		fmt.Printf("sha256:  %s\n", result.SHA256)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(buildCmd)
}
