package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
		return runSbom(os.Stdout, os.Stderr, args)
	},
}

// runSbom is the testable entry point. It writes machine output
// (table or JSON) to stdout and human-readable empty-state
// messages to stderr. Empty surfaces (no config, empty config,
// or no matching packages) exit zero so downstream pipelines can
// rely on a stable contract — JSON mode always emits a JSON array,
// never the literal `null`.
func runSbom(stdout, stderr io.Writer, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working dir: %w", err)
	}
	globalDir, err := galeConfigDir()
	if err != nil {
		return fmt.Errorf("finding config dir: %w", err)
	}
	data, configPath, err := resolveSbomConfig(cwd, globalDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// No gale.toml anywhere — treat as empty SBOM. Mirrors
			// `list` and `outdated`.
			return emitEmptySbom(stdout, stderr)
		}
		return fmt.Errorf("reading config: %w", err)
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}
	cfg.ApplyHost(config.CurrentHost())

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

	if len(packages) == 0 {
		return emitEmptySbom(stdout, stderr)
	}

	lp, lpErr := lockfilePath(configPath)
	if lpErr != nil {
		return lpErr
	}
	lf, err := lockfile.Read(lp)
	if err != nil {
		return fmt.Errorf("reading lockfile: %w", err)
	}

	// Resolve recipes for metadata.
	ctx, err := newCmdContext("", false, false)
	if err != nil {
		return fmt.Errorf("creating context: %w", err)
	}

	// Pre-size to zero so JSON emits `[]` not `null` even if the
	// loop body never appends.
	entries := make([]sbomEntry, 0, len(packages))

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
		return outputJSON(stdout, entries)
	}
	outputTable(stdout, entries)
	return nil
}

// emitEmptySbom writes the consistent empty-state response: a
// single line to stderr in human mode, an empty JSON array on
// stdout in --json mode. Always exits zero.
func emitEmptySbom(stdout, stderr io.Writer) error {
	if sbomJSON {
		return outputJSON(stdout, []sbomEntry{})
	}
	fmt.Fprintln(stderr, "No packages installed.")
	return nil
}

func outputJSON(w io.Writer, entries []sbomEntry) error {
	if entries == nil {
		// JSON consumers expect [] not null.
		entries = []sbomEntry{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(entries)
}

func outputTable(w io.Writer, entries []sbomEntry) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PACKAGE\tVERSION\tLICENSE\tSOURCE\tMETHOD")
	for _, e := range entries {
		source := shortenURL(e.SourceURL)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			e.Name, e.Version, e.License, source, e.Method)
	}
	tw.Flush()
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
// falling back to the global config in globalDir. Returns
// an error wrapping os.ErrNotExist when neither exists so
// callers can distinguish "no config" from other read
// failures.
func resolveSbomConfig(cwd, globalDir string) ([]byte, string, error) {
	path, err := config.FindGaleConfig(cwd)
	if err == nil {
		data, readErr := os.ReadFile(path)
		if readErr == nil {
			return data, path, nil
		}
		if !errors.Is(readErr, os.ErrNotExist) {
			return nil, "", readErr
		}
	}

	globalPath := filepath.Join(globalDir, "gale.toml")
	data, err := os.ReadFile(globalPath)
	if err != nil {
		return nil, "", err
	}
	return data, globalPath, nil
}

func init() {
	sbomCmd.Flags().BoolVar(&sbomJSON, "json", false,
		"Output as JSON")
	rootCmd.AddCommand(sbomCmd)
}
