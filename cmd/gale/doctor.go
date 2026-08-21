package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kelp/gale/internal/activation"
	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/store"
)

// cappedList builds a message body listing items, capped at
// 5. header is the opening line. If len(items) > 5, an
// overflow line "... N more" is appended. footer is appended
// after the list when non-empty (e.g. a remediation command).
func cappedList(header string, items []string, footer string) string {
	const maxShown = 5
	msg := header
	shown := items
	if len(shown) > maxShown {
		shown = shown[:maxShown]
	}
	for _, item := range shown {
		msg += "\n  " + item
	}
	if len(items) > maxShown {
		msg += fmt.Sprintf("\n  ... %d more", len(items)-maxShown)
	}
	if footer != "" {
		msg += "\n  " + footer
	}
	return msg
}

// doctorContext holds resolved state shared across checks.
type doctorContext struct {
	galeDir   string
	storeRoot string
	cwd       string
	ctx       context.Context
	out       *output.Output
	// locks is the v2 lock loaded for each scope label by
	// checkLockReadable. A missing entry means that scope has
	// no readable v2 lock; roots and digest skip it.
	locks map[string]*lockfile.V2
}

// doctorCheck is a single health check.
type doctorCheck struct {
	name string
	run  func(ctx *doctorContext) bool // true = passed
}

const (
	scopeGlobal  = "global"
	scopeProject = "project"
)

// doctorScope is one scope a doctor run covers: the gale dir
// whose generation and lock it owns.
type doctorScope struct {
	label      string
	galeDir    string
	configPath string
}

// doctorScopes enumerates the scopes a run covers: the global one
// always, plus the project the cwd resolves to when that is not the
// global manifest reached from under ~/.gale (gh#96).
func doctorScopes(ctx *doctorContext) ([]doctorScope, error) {
	scopes := []doctorScope{{
		label:      scopeGlobal,
		galeDir:    ctx.galeDir,
		configPath: filepath.Join(ctx.galeDir, "gale.toml"),
	}}
	projConfig, err := projectConfigPath(ctx.cwd)
	if err != nil {
		return scopes, nil //nolint:nilerr // absence is an answer, not a failure
	}
	if configInGaleDir(projConfig, ctx.galeDir) {
		return scopes, nil
	}
	projGaleDir, err := galeDirForConfig(projConfig)
	if err != nil {
		return nil, fmt.Errorf("resolving project gale dir: %w", err)
	}
	return append(scopes, doctorScope{
		label:      scopeProject,
		galeDir:    projGaleDir,
		configPath: projConfig,
	}), nil
}

var doctorChecks = []doctorCheck{
	{"PATH", checkPATH},
	{"lock readable", checkLockReadable},
	{"generation matches lock roots", checkGenerationMatchesLockRoots},
	{"tree digest matches", checkTreeDigestMatches},
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check PATH, lock, generation, and tree digests",
	Args:  cobra.ExactArgs(0),
	RunE: func(cmd *cobra.Command, args []string) error {
		galeDir, err := galeConfigDir()
		if err != nil {
			fmt.Fprintln(cmd.ErrOrStderr(),
				"xxx Cannot find home directory")
			return err
		}
		cwd, _ := os.Getwd()
		return runDoctor(&doctorIO{
			galeDir: galeDir,
			cwd:     cwd,
			stdout:  cmd.OutOrStdout(),
			stderr:  cmd.ErrOrStderr(),
			ctx:     cmd.Context(),
		})
	},
}

// doctorIO bundles the writers and resolved paths a doctor
// run needs. Extracted so tests can drive runDoctor without
// going through cobra and can assert on stdout/stderr
// independently — the stdout summary is the user-facing
// "answer", per-check progress lines are on stderr.
type doctorIO struct {
	galeDir string
	cwd     string
	stdout  io.Writer
	stderr  io.Writer
	ctx     context.Context
}

// runDoctor executes every doctor check and writes a final
// summary block to stdout.
func runDoctor(d *doctorIO) error {
	out := newOutputForWriter(d.stderr)
	runCtx := d.ctx
	if runCtx == nil {
		runCtx = context.Background()
	}
	ctx := &doctorContext{
		galeDir:   d.galeDir,
		storeRoot: defaultStoreRoot(),
		cwd:       d.cwd,
		ctx:       runCtx,
		out:       out,
		locks:     map[string]*lockfile.V2{},
	}

	var failed int
	for _, check := range doctorChecks {
		if !check.run(ctx) {
			failed++
		}
	}
	total := len(doctorChecks)

	if failed > 0 {
		fmt.Fprintf(d.stdout,
			"PROBLEMS: %d issue(s) of %d checks\n", failed, total)
		return fmt.Errorf("doctor found problems")
	}
	fmt.Fprintf(d.stdout, "OK: %d checks passed\n", total)
	return nil
}

// checkPATH verifies ~/.gale/current/bin is on PATH.
func checkPATH(ctx *doctorContext) bool {
	galeBin := filepath.Join(ctx.galeDir, "current", "bin")
	pathDirs := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))
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
			galeBin, galeBin,
		))
		return false
	}
	ctx.out.Success(fmt.Sprintf(
		"PATH includes %s", galeBin,
	))
	return true
}

// checkLockReadable loads each scope's lock as v2. A missing
// or v1 lock fails this check only; roots and digest skip
// those scopes.
func checkLockReadable(ctx *doctorContext) bool {
	scopes, err := doctorScopes(ctx)
	if err != nil {
		ctx.out.Error(fmt.Sprintf("lock readable: %v", err))
		return false
	}
	var problems []string
	for _, s := range scopes {
		lp, pErr := lockfilePath(s.configPath)
		if pErr != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", s.label, pErr))
			continue
		}
		lf, lErr := readVerifyLock(lp)
		if lErr != nil {
			problems = append(problems, fmt.Sprintf("%s: %s", s.label, doctorLockErr(lErr)))
			continue
		}
		ctx.locks[s.label] = lf
	}
	if len(problems) == 0 {
		ctx.out.Success("lock readable")
		return true
	}
	ctx.out.Error(cappedList(
		fmt.Sprintf("lock readable (%d scope(s)):", len(problems)),
		problems,
		"",
	))
	return false
}

func doctorLockErr(err error) string {
	switch {
	case errors.Is(err, errVerifyNoLock):
		return "no v2 lock; run gale install <pkg>"
	case errors.Is(err, errVerifyV1):
		return "lock is v1; run gale fetch-adopt"
	default:
		return err.Error()
	}
}

// checkGenerationMatchesLockRoots compares each scope that
// has a v2 lock against activation.Check. A scope without a
// v2 lock is skipped so one missing file is not three reds.
func checkGenerationMatchesLockRoots(ctx *doctorContext) bool {
	scopes, err := doctorScopes(ctx)
	if err != nil {
		ctx.out.Error(fmt.Sprintf("generation matches lock roots: %v", err))
		return false
	}
	host, err := config.CurrentHost()
	if err != nil {
		ctx.out.Error(fmt.Sprintf("generation matches lock roots: %v", err))
		return false
	}
	var problems []string
	ran := 0
	for _, s := range scopes {
		if ctx.locks[s.label] == nil {
			continue
		}
		ran++
		installed, iErr := generation.CurrentVersionsStrict(s.galeDir, ctx.storeRoot)
		if iErr != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", s.label, iErr))
			continue
		}
		linked, lErr := generation.CurrentStoreDirs(s.galeDir, ctx.storeRoot)
		if lErr != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", s.label, lErr))
			continue
		}
		lp, pErr := lockfilePath(s.configPath)
		if pErr != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", s.label, pErr))
			continue
		}
		if cErr := activation.Check(activation.Request{
			LockPath:  lp,
			Host:      host,
			Platform:  currentPlatform(),
			StoreRoot: ctx.storeRoot,
			Installed: installed,
			Linked:    linked,
		}); cErr != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", s.label, cErr))
		}
	}
	if ran == 0 {
		ctx.out.Success("generation matches lock roots (skipped — no v2 lock)")
		return true
	}
	if len(problems) == 0 {
		ctx.out.Success("generation matches lock roots")
		return true
	}
	ctx.out.Error(cappedList(
		"generation matches lock roots:",
		problems,
		"",
	))
	return false
}

// checkTreeDigestMatches runs verifyOne per locked root on
// scopes that already loaded a v2 lock.
func checkTreeDigestMatches(ctx *doctorContext) bool {
	scopes, err := doctorScopes(ctx)
	if err != nil {
		ctx.out.Error(fmt.Sprintf("tree digest matches: %v", err))
		return false
	}
	st := store.NewStore(ctx.storeRoot)
	plat := currentPlatform()
	var problems []string
	ran := 0
	for _, s := range scopes {
		lf := ctx.locks[s.label]
		if lf == nil {
			continue
		}
		ran++
		roots, rErr := verifyRoots(lf, "")
		if rErr != nil {
			problems = append(problems, fmt.Sprintf("%s: %s", s.label, doctorDigestErr(rErr)))
			continue
		}
		for _, root := range roots {
			if vErr := verifyOne(ctx.ctx, st, lf, root, plat); vErr != nil {
				problems = append(problems, fmt.Sprintf("%s: %s", s.label, doctorDigestErr(vErr)))
			}
		}
	}
	if ran == 0 {
		ctx.out.Success("tree digest matches (skipped — no v2 lock)")
		return true
	}
	if len(problems) == 0 {
		ctx.out.Success("tree digest matches")
		return true
	}
	ctx.out.Error(cappedList(
		"tree digest matches:",
		problems,
		"Run: gale verify",
	))
	return false
}

func doctorDigestErr(err error) string {
	switch {
	case errors.Is(err, errVerifyDigest):
		return "tree digest mismatch"
	case errors.Is(err, errVerifyAttestation):
		return "locked attestation is not checkable"
	case errors.Is(err, errVerifyMissingStore):
		return "fetch store missing"
	case errors.Is(err, errVerifyEmptyDigest):
		return "empty tree_digest"
	case errors.Is(err, errVerifyNoPlatform):
		return "no current-platform artifact"
	case errors.Is(err, errVerifyUnknownRoot):
		return "package is not a default-target root"
	default:
		return strings.ReplaceAll(err.Error(), "gale verify", "doctor")
	}
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}
