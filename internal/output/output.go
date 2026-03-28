package output

import (
	"fmt"
	"io"

	"github.com/fatih/color"
)

// Output handles colored terminal output.
// The caller decides whether to enable color (by checking
// NO_COLOR, TTY status, etc.) and passes the decision in.
type Output struct {
	w      io.Writer
	color  bool
	cyan   *color.Color
	green  *color.Color
	yellow *color.Color
	red    *color.Color
}

// New creates an Output that writes to w.
// color controls whether ANSI codes are emitted.
func New(w io.Writer, useColor bool) *Output {
	o := &Output{w: w, color: useColor}
	if useColor {
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
	o.writeMsg(o.cyan, "--> ", msg)
}

// Success writes a success message in green.
func (o *Output) Success(msg string) {
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

// Step writes a sub-step message with a dimmed arrow.
func (o *Output) Step(msg string) {
	o.writeMsg(o.cyan, "    ", msg)
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
	if o.color {
		return o.cyan.Sprint("    ")
	}
	return "    "
}
