package main

import (
	"slices"
	"testing"

	"github.com/spf13/cobra"
)

func TestHostFlagCommandsFrozen(t *testing.T) {
	got := commandsWithHostFlag(rootCmd)
	if len(got) != 0 {
		t.Fatalf("commands with --host = %v, want none", got)
	}
}

func commandsWithHostFlag(root *cobra.Command) []string {
	var names []string
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		for _, child := range c.Commands() {
			if child.Flags().Lookup("host") != nil {
				names = append(names, child.Name())
			}
			walk(child)
		}
	}
	walk(root)
	slices.Sort(names)
	return names
}
