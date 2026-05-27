package main

import (
	"context"
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
	"github.com/kelp/gale/internal/parallel"
	"github.com/spf13/cobra"
)

var (
	sbomJSON    bool
	sbomGlobal  bool
	sbomProject bool
	sbomAll     bool
)

// sbomEntry holds the metadata for one package.
type sbomEntry struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	Scope         string `json:"scope,omitempty"`
	SourceURL     string `json:"source_url,omitempty"`
	SourceSHA256  string `json:"source_sha256,omitempty"`
	ArchiveSHA256 string `json:"archive_sha256,omitempty"`
	License       string `json:"license,omitempty"`
	Homepage      string `json:"homepage,omitempty"`
	Method        string `json:"method"`
}

// sbomConfig holds a config path and an optional scope label
// to attach to each entry parsed from that config.
type sbomConfig struct {
	path  string
	scope string
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
//
// Scope flags (--global / --project / --all) select which
// config(s) to consult. With no flag, the existing
// project-then-global fallback applies.
func runSbom(stdout, stderr io.Writer, args []string) error {
	if err := validateScopeFlags(sbomGlobal, sbomProject); err != nil {
		return err
	}
	filter := ""
	if len(args) == 1 {
		filter = args[0]
	}

	configs, err := resolveSbomConfigs(sbomGlobal, sbomProject, sbomAll)
	if err != nil {
		return err
	}
	if len(configs) == 0 {
		// No gale.toml anywhere — treat as empty SBOM. Mirrors
		// `list` and `outdated`.
		return emitEmptySbom(stdout, stderr)
	}

	entries, err := collectSbomEntries(configs, filter)
	if err != nil {
		return err
	}

	if len(entries) == 0 {
		return emitEmptySbom(stdout, stderr)
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Scope != entries[j].Scope {
			return entries[i].Scope < entries[j].Scope
		}
		return entries[i].Name < entries[j].Name
	})

	if sbomJSON {
		return outputJSON(stdout, entries)
	}
	outputTable(stdout, entries, sbomAll)
	return nil
}

// resolveSbomConfigs returns the list of gale.toml configs to
// read. For --all, both project and global are included
// (filtered to those that exist). For --global / --project, a
// single explicit scope. With no flag, the same
// project-then-global fallback used by other read-only
// commands.
func resolveSbomConfigs(global, project, all bool) ([]sbomConfig, error) {
	if all {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("getting working dir: %w", err)
		}
		var configs []sbomConfig
		if projPath, err := projectConfigPath(cwd); err == nil {
			if _, statErr := os.Stat(projPath); statErr == nil {
				configs = append(configs, sbomConfig{
					path: projPath, scope: "project",
				})
			}
		}
		gPath, err := globalConfigPath()
		if err != nil {
			return nil, err
		}
		if _, statErr := os.Stat(gPath); statErr == nil {
			configs = append(configs, sbomConfig{
				path: gPath, scope: "global",
			})
		}
		return configs, nil
	}

	// Single-scope path. Reuse main's resolver so the
	// project-then-global fallback and ErrNotExist semantics
	// stay consistent with `list`, `outdated`, and `env`.
	if global || project {
		path, err := resolveReadOnlyConfigPath(global, project)
		if err != nil {
			return nil, err
		}
		if _, statErr := os.Stat(path); statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				return nil, nil
			}
			return nil, fmt.Errorf("stat %s: %w", path, statErr)
		}
		return []sbomConfig{{path: path}}, nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getting working dir: %w", err)
	}
	globalDir, err := galeConfigDir()
	if err != nil {
		return nil, fmt.Errorf("finding config dir: %w", err)
	}
	_, path, err := resolveSbomConfig(cwd, globalDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}
	return []sbomConfig{{path: path}}, nil
}

// collectSbomEntries reads each config + lockfile and returns
// the SBOM entries (one per package).
func collectSbomEntries(configs []sbomConfig, filter string) ([]sbomEntry, error) {
	ctx, err := newCmdContext("", false, false)
	if err != nil {
		return nil, fmt.Errorf("creating context: %w", err)
	}

	// Pre-size to zero so JSON emits `[]` not `null` even when
	// no config yields any entries.
	entries := make([]sbomEntry, 0)
	for _, sc := range configs {
		data, err := os.ReadFile(sc.path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", sc.path, err)
		}
		cfg, err := config.ParseGaleConfig(string(data))
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", sc.path, err)
		}
		cfg.ApplyHost(config.CurrentHost())

		lp, lpErr := lockfilePath(sc.path)
		if lpErr != nil {
			return nil, lpErr
		}
		lf, err := lockfile.Read(lp)
		if err != nil {
			return nil, fmt.Errorf("reading lockfile: %w", err)
		}

		packages := cfg.Packages
		if filter != "" {
			ver, ok := packages[filter]
			if !ok {
				if len(configs) == 1 {
					return nil, fmt.Errorf(
						"%s not found in gale.toml", filter)
				}
				continue
			}
			packages = map[string]string{filter: ver}
		}

		// Snapshot the lockfile state per package and dispatch
		// recipe resolution in parallel. The resolver is the only
		// slow part (per-package HTTP); the surrounding work is
		// pure data assembly. Worker pool of 8 matches the bound
		// used by sync/outdated.
		type item struct {
			name, version string
			lockedSHA     string
			hasLock       bool
		}
		items := make([]item, 0, len(packages))
		for name, version := range packages {
			locked, ok := lf.Packages[name]
			items = append(items, item{
				name: name, version: version,
				lockedSHA: locked.SHA256, hasLock: ok,
			})
		}
		results, _ := parallel.Map(context.Background(), items, 8,
			func(_ context.Context, p item) (sbomEntry, error) {
				e := sbomEntry{
					Name:    p.name,
					Version: p.version,
					Scope:   sc.scope,
					Method:  "source",
				}
				if p.hasLock {
					e.ArchiveSHA256 = p.lockedSHA
				}
				if r, err := ctx.ResolveVersionedRecipe(p.name, p.version); err == nil {
					e.SourceURL = r.Source.URL
					e.SourceSHA256 = r.Source.SHA256
					e.License = r.Package.License
					e.Homepage = r.Package.Homepage
					if bin := r.BinaryForPlatform(
						runtime.GOOS, runtime.GOARCH); bin != nil {
						if e.ArchiveSHA256 == bin.SHA256 {
							e.Method = "binary"
						}
					}
				}
				return e, nil
			})
		entries = append(entries, results...)
	}
	return entries, nil
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

func outputTable(w io.Writer, entries []sbomEntry, showScope bool) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if showScope {
		fmt.Fprintln(tw, "SCOPE\tPACKAGE\tVERSION\tLICENSE\tSOURCE\tMETHOD")
	} else {
		fmt.Fprintln(tw, "PACKAGE\tVERSION\tLICENSE\tSOURCE\tMETHOD")
	}
	for _, e := range entries {
		// Empty License / Source / Method happen when recipe
		// resolution failed mid-collect; rendering a "-" keeps
		// tabwriter padding aligned instead of producing
		// awkward double-space gaps mid-row.
		license := dashIfEmpty(e.License)
		source := dashIfEmpty(shortenURL(e.SourceURL))
		method := dashIfEmpty(e.Method)
		if showScope {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
				e.Scope, e.Name, e.Version, license,
				source, method)
		} else {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
				e.Name, e.Version, license, source, method)
		}
	}
	tw.Flush()
}

// dashIfEmpty substitutes "-" for an empty value so SBOM
// table rows align even when recipe metadata is missing.
// The placeholder is well-understood across CLI tools (apt,
// brew, ls -l), avoiding a custom glyph.
func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
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
	sbomCmd.Flags().BoolVarP(&sbomGlobal, "global", "g", false,
		"Show SBOM for the global gale.toml")
	sbomCmd.Flags().BoolVarP(&sbomProject, "project", "p", false,
		"Show SBOM for the project gale.toml")
	sbomCmd.Flags().BoolVarP(&sbomAll, "all", "a", false,
		"Show SBOM for both project and global gale.toml")
	rootCmd.AddCommand(sbomCmd)
}
