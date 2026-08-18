package main

import (
	"slices"
	"testing"

	"github.com/spf13/cobra"
)

func TestHostFlagCommandsFrozen(t *testing.T) {
	want := []string{"add", "install", "lock", "pin", "remove", "unpin"}
	got := commandsWithHostFlag(rootCmd)
	if !slices.Equal(got, want) {
		t.Fatalf("commands with --host = %v, want %v", got, want)
	}
	for _, name := range want {
		cmd := findCmd(name)
		if cmd == nil {
			t.Fatalf("command %q missing", name)
		}
		f := cmd.Flags().Lookup("host")
		if f == nil {
			t.Fatalf("%s: --host flag not found", name)
		}
		if f.DefValue != "" {
			t.Errorf("%s: --host default = %q, want empty", name, f.DefValue)
		}
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
