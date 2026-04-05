package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"text/tabwriter"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/spf13/cobra"
)

var sbomJSON bool

// sbomEntry holds the metadata for one package.
type sbomEntry struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	SourceURL     string `json:"source_url,omitempty"`
	SourceSHA256  string `json:"source_sha256,omitempty"`
	ArchiveSHA256 string `json:"archive_sha256,omitempty"`
	License       string `json:"license,omitempty"`
	Homepage      string `json:"homepage,omitempty"`
	Method        string `json:"method"`
}

var sbomCmd = &cobra.Command{
	Use:   "sbom [package]",
	Short: "Show software bill of materials",
	Long:  "List installed packages with source, version, license, and install method.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working dir: %w", err)
		}
		globalDir, err := galeConfigDir()
		if err != nil {
			return fmt.Errorf("finding config dir: %w", err)
		}
		data, configPath, err := resolveSbomConfig(
			cwd, globalDir)
		if err != nil {
			return fmt.Errorf("reading config: %w", err)
		}
		cfg, err := config.ParseGaleConfig(string(data))
		if err != nil {
			return fmt.Errorf("parsing config: %w", err)
		}

		lp, lpErr := lockfilePath(configPath)
		if lpErr != nil {
			return lpErr
		}
		lf, err := lockfile.Read(lp)
		if err != nil {
			return fmt.Errorf("reading lockfile: %w", err)
		}

		// Filter to single package if specified.
		packages := cfg.Packages
		if len(args) == 1 {
			name := args[0]
			ver, ok := packages[name]
			if !ok {
				return fmt.Errorf(
					"%s not found in gale.toml", name)
			}
			packages = map[string]string{name: ver}
		}

		// Resolve recipes for metadata.
		ctx, err := newCmdContext("", false, false)
		if err != nil {
			return fmt.Errorf("creating context: %w", err)
		}

		var entries []sbomEntry

		for name, version := range packages {
			e := sbomEntry{
				Name:    name,
				Version: version,
				Method:  "source",
			}

			// Get archive hash from lockfile.
			if locked, ok := lf.Packages[name]; ok {
				e.ArchiveSHA256 = locked.SHA256
			}

			// Get recipe metadata.
			r, err := ctx.ResolveVersionedRecipe(
				name, version)
			if err == nil {
				e.SourceURL = r.Source.URL
				e.SourceSHA256 = r.Source.SHA256
				e.License = r.Package.License
				e.Homepage = r.Package.Homepage

				// Infer install method.
				if bin := r.BinaryForPlatform(
					runtime.GOOS, runtime.GOARCH); bin != nil {
					if e.ArchiveSHA256 == bin.SHA256 {
						e.Method = "binary"
					}
				}
			}

			entries = append(entries, e)
		}

		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Name < entries[j].Name
		})

		if sbomJSON {
			return outputJSON(entries)
		}
		outputTable(entries)
		return nil
	},
}

func outputJSON(entries []sbomEntry) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(entries)
}

func outputTable(entries []sbomEntry) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PACKAGE\tVERSION\tLICENSE\tSOURCE\tMETHOD")
	for _, e := range entries {
		source := shortenURL(e.SourceURL)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			e.Name, e.Version, e.License, source, e.Method)
	}
	w.Flush()
}

// shortenURL extracts just the hostname from a URL.
func shortenURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return u.Host
}

// resolveSbomConfig reads gale.toml, trying the project
// config first (found by walking up from cwd), then
// falling back to the global config in globalDir.
func resolveSbomConfig(cwd, globalDir string) ([]byte, string, error) {
	path, err := config.FindGaleConfig(cwd)
	if err == nil {
		data, readErr := os.ReadFile(path)
		if readErr == nil {
			return data, path, nil
		}
	}

	globalPath := filepath.Join(globalDir, "gale.toml")
	data, err := os.ReadFile(globalPath)
	if err != nil {
		return nil, "", fmt.Errorf("reading config: %w", err)
	}
	return data, globalPath, nil
}

func init() {
	sbomCmd.Flags().BoolVar(&sbomJSON, "json", false,
		"Output as JSON")
	rootCmd.AddCommand(sbomCmd)
}
