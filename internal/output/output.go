package output

import (
	"fmt"
	"io"

	"github.com/fatih/color"
)

// Options controls output behavior.
type Options struct {
	Color bool
	Steps bool
	Quiet bool
}

// Output handles colored terminal output.
// The caller decides whether to enable color (by checking
// NO_COLOR, TTY status, etc.) and passes the decision in.
type Output struct {
	w      io.Writer
	color  bool
	steps  bool
	quiet  bool
	cyan   *color.Color
	green  *color.Color
	yellow *color.Color
	red    *color.Color
}

// New creates an Output that writes to w.
// color controls whether ANSI codes are emitted.
func New(w io.Writer, useColor bool) *Output {
	return NewWithOptions(w, Options{Color: useColor, Steps: true})
}

// NewWithOptions creates an Output with explicit behavior.
func NewWithOptions(w io.Writer, opts Options) *Output {
	o := &Output{
		w:     w,
		color: opts.Color,
		steps: opts.Steps,
		quiet: opts.Quiet,
	}
	if opts.Color {
		o.cyan = color.New(color.FgCyan)
		o.cyan.EnableColor()
		o.green = color.New(color.FgGreen)
		o.green.EnableColor()
		o.yellow = color.New(color.FgYellow)
		o.yellow.EnableColor()
		o.red = color.New(color.FgRed)
		o.red.EnableColor()
	}
	return o
}

// Info writes an informational message in cyan.
func (o *Output) Info(msg string) {
	if o.quiet {
		return
	}
	o.writeMsg(o.cyan, "--> ", msg)
}

// Success writes a success message in green.
func (o *Output) Success(msg string) {
	if o.quiet {
		return
	}
	o.writeMsg(o.green, "==> ", msg)
}

// Warn writes a warning message in yellow.
func (o *Output) Warn(msg string) {
	o.writeMsg(o.yellow, "!!! ", msg)
}

// Error writes an error message in red.
func (o *Output) Error(msg string) {
	o.writeMsg(o.red, "xxx ", msg)
}

// Step writes a sub-step message indented under the
// main arrow.
func (o *Output) Step(msg string) {
	if !o.steps {
		return
	}
	o.writeMsg(o.cyan, "  > ", msg)
}

func (o *Output) writeMsg(c *color.Color, prefix, msg string) {
	if o.color {
		fmt.Fprintf(o.w, "%s%s\n", c.Sprint(prefix), msg)
	} else {
		fmt.Fprintf(o.w, "%s%s\n", prefix, msg)
	}
}

// StepPrefix returns the colored prefix string for use in
// progress lines that need \r overwriting.
func (o *Output) StepPrefix() string {
	if !o.steps {
		return ""
	}
	if o.color {
		return o.cyan.Sprint("  > ")
	}
	return "  > "
}
