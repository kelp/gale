package main

import (
	"io"
	"os"

	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/output"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

type outputModeInput struct {
	tty         bool
	noColor     bool
	plain       bool
	quiet       bool
	verbose     bool
	errorFormat string
}

type outputMode struct {
	color    bool
	steps    bool
	progress bool
	quiet    bool
	// TODO: wire to output.Output once it supports verbose mode.
	verbose     bool
	errorFormat string
}

func resolveOutputMode(in outputModeInput) outputMode {
	mode := outputMode{
		color:       in.tty,
		steps:       in.tty,
		progress:    in.tty,
		quiet:       in.quiet,
		verbose:     in.verbose,
		errorFormat: in.errorFormat,
	}
	if mode.errorFormat == "" {
		mode.errorFormat = "text"
	}
	if in.noColor {
		mode.color = false
	}
	if in.plain {
		mode.color = false
		mode.steps = false
		mode.progress = false
	}
	if in.quiet {
		mode.steps = false
		mode.progress = false
	}
	return mode
}

func stderrIsTTY() bool {
	return isatty.IsTerminal(os.Stderr.Fd()) || isatty.IsCygwinTerminal(os.Stderr.Fd())
}

func currentOutputMode() outputMode {
	in := outputModeInput{
		tty:         stderrIsTTY(),
		noColor:     noColor,
		plain:       plain,
		quiet:       quiet,
		verbose:     verbose,
		errorFormat: errorFormat,
	}
	applyNoColorEnv(&in)
	return resolveOutputMode(in)
}

// applyNoColorEnv honors https://no-color.org: if NO_COLOR is
// set and non-empty in the environment, treat it as if
// --no-color were passed. An empty value is ignored per the
// spec. Reads from os.Getenv so callers don't have to.
func applyNoColorEnv(in *outputModeInput) {
	if v := os.Getenv("NO_COLOR"); v != "" {
		in.noColor = true
	}
}

func newOutputForWriter(w io.Writer) *output.Output {
	mode := currentOutputMode()
	// mode.verbose is stored but not yet forwarded; see TODO on outputMode.verbose.
	out := output.NewWithOptions(w, output.Options{
		Color: mode.color,
		Steps: mode.steps,
		Quiet: mode.quiet,
	})
	if w == os.Stderr {
		configureSubsystemOutput(out, mode)
	}
	return out
}

func newOutput() *output.Output {
	return newOutputForWriter(os.Stderr)
}

func newCmdOutput(_ *cobra.Command) *output.Output {
	return newOutput()
}

func configureSubsystemOutput(out *output.Output, mode outputMode) {
	build.SetOutput(out)
	download.ProgressPrefix = out.StepPrefix()
	download.SetProgressEnabled(mode.progress)
}
