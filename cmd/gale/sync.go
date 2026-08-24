package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var (
	syncGlobal       bool
	syncProject      bool
	syncOnlyIfNeeded bool
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Activate packages from the v2 lock",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateScopeFlags(syncGlobal, syncProject); err != nil {
			return err
		}
		return runSync(syncRun{
			Global:   syncGlobal,
			Project:  syncProject,
			IfNeeded: syncOnlyIfNeeded,
		})
	},
}

// syncRun is runSync's inputs. runSync already sat at the
// argument limit; IfNeeded cannot be a sixth bool without
// revive, and mutating the cobra syncOnlyIfNeeded global
// from shell/run would leak the flag across commands.
type syncRun struct {
	RecipesPath string
	Global      bool
	Project     bool
	ProjectDir  string
	IfNeeded    bool
}

// runSync activates the v2 lock: land fetch trees and rebuild
// the generation. When ProjectDir is non-empty, sync targets
// that project regardless of cwd or scope flags.
//
// Every exit records a completion stamp (gh#186). The result is a
// named one so the deferred writer records the command's own verdict
// rather than a separately maintained belief about it.
func runSync(s syncRun) (err error) {
	out := newOutput()

	cc, err := newCmdContext(s.RecipesPath, s.Global, s.Project)
	if err != nil {
		return err
	}

	retargetSync(cc, s.ProjectDir)

	host, err := config.CurrentHost()
	if err != nil {
		return err
	}

	ctx := context.Background()
	if s.IfNeeded {
		var cancel context.CancelFunc
		ctx, cancel = ifNeededContext()
		defer cancel()
	}

	record, withheld := beginSyncStamp(out, cc.GaleDir, cc.GalePath, host, s.IfNeeded)
	if withheld {
		return nil
	}
	// Declared here, before the recorder, so the stamp names whatever
	// landing failed on however this function returns.
	var outcomes []syncOutcome
	defer func() { record(err == nil, failedPackageNames(outcomes)) }()

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("sync --if-needed: %w", err)
	}

	outcomes, err = executeSync(ctx, cc, s, out, host)
	return err
}

func executeSync(
	ctx context.Context, cc *cmdContext, s syncRun, out *output.Output, host string,
) ([]syncOutcome, error) {
	_ = host
	if err := runSyncFetch(ctx, cc, s, out); err != nil {
		// A landing failure carries its artifact's identity so
		// the stamp can name the package that broke the sync.
		var se *stagingError
		if errors.As(err, &se) {
			return []syncOutcome{{
				name: se.name, version: se.version, installErr: se.err,
			}}, err
		}
		return nil, err
	}
	return nil, nil
}

func runSyncFetch(
	ctx context.Context, cc *cmdContext, s syncRun, out *output.Output,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := refuseSwitchHosts(cc.Host, cc.GalePath); err != nil {
		return err
	}
	cfg, err := cc.LoadConfig()
	if err != nil {
		return err
	}
	lp, err := lockfilePath(cc.GalePath)
	if err != nil {
		return err
	}
	lf, err := requireLiveV2(lp)
	if err != nil {
		return err
	}
	if err := checkV2Declared(lf, declaredPins(cfg)); err != nil {
		return err
	}
	arts, err := artsFromV2(lf)
	if err != nil {
		return err
	}
	if dryRun {
		out.Info(fmt.Sprintf("sync %d locked package(s)", len(arts)))
		return nil
	}
	if len(lf.Packages) == 0 {
		cur, curErr := generation.Current(cc.GaleDir)
		if curErr != nil {
			return curErr
		}
		if cur == 0 {
			out.Success("Sync complete: 0 locked, lock unchanged")
			return nil
		}
	}
	if err := landFetchArts(ctx, cc.StoreRoot, arts, installToStore); err != nil {
		return err
	}
	if err := rebuildFromV2(cc, lf); err != nil {
		return fmt.Errorf("rebuild generation: %w", err)
	}
	out.Success(fmt.Sprintf(
		"Sync complete: %d locked, lock unchanged", len(arts),
	))
	_ = s
	return nil
}

// retargetSync points ctx at an explicit project directory.
//
// The explicit directory takes precedence over scope flags; syncIfNeeded
// supplies it when shell/run are invoked with --project. Registration
// happens at publication (rebuildGenerationWith), not here.
func retargetSync(ctx *cmdContext, projectDir string) {
	if projectDir != "" {
		ctx.GalePath = filepath.Join(projectDir, "gale.toml")
		ctx.GaleDir = filepath.Join(projectDir, ".gale")
	}
}

// syncOutcome is the identity of a package a fetch landing failed
// on, so the completion stamp can name it.
type syncOutcome struct {
	name, version string
	installErr    error
}

func init() {
	syncCmd.Flags().BoolVarP(&syncGlobal, "global", "g",
		false, "Sync global packages")
	syncCmd.Flags().BoolVarP(&syncProject, "project", "p",
		false, "Sync project packages")
	syncCmd.Flags().BoolVar(&syncOnlyIfNeeded, "if-needed", false,
		"Sync only when the last sync did not complete on these inputs")
	rootCmd.AddCommand(syncCmd)
}
