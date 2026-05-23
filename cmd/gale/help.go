package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var (
	bold   = color.New(color.Bold)
	cyan   = color.New(color.FgCyan)
	yellow = color.New(color.Bold, color.FgYellow)
)

func colorHelp(cmd *cobra.Command, args []string) {
	// Help text is not an error; route it to stdout so users
	// can pipe it through less/grep. Cobra's own default help
	// uses stdout for the same reason.
	w := cmd.OutOrStdout()

	// Description.
	if cmd.Long != "" {
		fmt.Fprintln(w, cmd.Long)
	} else if cmd.Short != "" {
		fmt.Fprintln(w, cmd.Short)
	}
	fmt.Fprintln(w)

	// Usage.
	yellow.Fprintln(w, "USAGE")
	useLine := cmd.UseLine()
	if cmd.HasAvailableSubCommands() {
		useLine = cmd.CommandPath() + " [command]"
	}
	fmt.Fprintf(w, "  %s\n\n", bold.Sprint(useLine))

	// Subcommands.
	cmds := cmd.Commands()
	var visible []*cobra.Command
	for _, c := range cmds {
		if c.IsAvailableCommand() {
			visible = append(visible, c)
		}
	}
	if len(visible) > 0 {
		yellow.Fprintln(w, "COMMANDS")
		// Find max command name length for alignment.
		maxLen := 0
		for _, c := range visible {
			if len(c.Name()) > maxLen {
				maxLen = len(c.Name())
			}
		}
		for _, c := range visible {
			name := cyan.Sprintf("  %-*s", maxLen+2, c.Name())
			fmt.Fprintf(w, "%s%s\n", name, c.Short)
		}
		fmt.Fprintln(w)
	}

	// Flags. Print local (command-specific) and inherited
	// (persistent / global) flags in separate sections so users
	// see every flag that affects the command, not just the
	// command's own additions.
	if local := cmd.LocalFlags(); local.HasFlags() {
		yellow.Fprintln(w, "FLAGS")
		printFlagSet(w, local)
		fmt.Fprintln(w)
	}
	if inherited := cmd.InheritedFlags(); inherited.HasFlags() {
		yellow.Fprintln(w, "GLOBAL FLAGS")
		printFlagSet(w, inherited)
		fmt.Fprintln(w)
	}

	// Footer.
	if cmd.HasAvailableSubCommands() {
		fmt.Fprintf(w,
			"Use %s for more information about a command.\n",
			bold.Sprintf("%s [command] --help", cmd.CommandPath()))
	}
}

// printFlagSet renders a pflag.FlagSet block matching the rest
// of the colored help layout.
func printFlagSet(w io.Writer, fs *pflag.FlagSet) {
	fs.VisitAll(func(f *pflag.Flag) {
		var parts []string
		if f.Shorthand != "" {
			parts = append(parts,
				cyan.Sprintf("-%s", f.Shorthand))
		}
		name := cyan.Sprintf("--%s", f.Name)
		if f.Value.Type() == "string" {
			name += " " + f.Value.Type()
		}
		parts = append(parts, name)
		fmt.Fprintf(w, "  %s\n", strings.Join(parts, ", "))
		if f.Usage != "" {
			fmt.Fprintf(w, "      %s\n", f.Usage)
		}
	})
}

// Persistent flags bound in root.go init().
var (
	noColor     bool
	verbose     bool
	dryRun      bool
	plain       bool
	quiet       bool
	errorFormat string
)

// applyColorMode syncs fatih/color with the current output mode.
func applyColorMode() {
	color.NoColor = !currentOutputMode().color
}
