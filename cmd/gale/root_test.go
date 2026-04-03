package main

import "testing"

func TestVerbosePersistentFlag(t *testing.T) {
	f := rootCmd.PersistentFlags().Lookup("verbose")
	if f == nil {
		t.Fatal("--verbose persistent flag not found")
	}
	if f.Shorthand != "v" {
		t.Errorf("verbose shorthand = %q, want %q",
			f.Shorthand, "v")
	}
}

func TestDryRunPersistentFlag(t *testing.T) {
	f := rootCmd.PersistentFlags().Lookup("dry-run")
	if f == nil {
		t.Fatal("--dry-run persistent flag not found")
	}
	if f.Shorthand != "n" {
		t.Errorf("dry-run shorthand = %q, want %q",
			f.Shorthand, "n")
	}
}
