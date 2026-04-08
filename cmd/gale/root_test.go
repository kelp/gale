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

func TestPlainPersistentFlag(t *testing.T) {
	f := rootCmd.PersistentFlags().Lookup("plain")
	if f == nil {
		t.Fatal("--plain persistent flag not found")
	}
}

func TestQuietPersistentFlag(t *testing.T) {
	f := rootCmd.PersistentFlags().Lookup("quiet")
	if f == nil {
		t.Fatal("--quiet persistent flag not found")
	}
	if f.Shorthand != "q" {
		t.Errorf("quiet shorthand = %q, want %q", f.Shorthand, "q")
	}
}

func TestErrorFormatPersistentFlag(t *testing.T) {
	f := rootCmd.PersistentFlags().Lookup("error-format")
	if f == nil {
		t.Fatal("--error-format persistent flag not found")
	}
	if f.DefValue != "text" {
		t.Errorf("error-format default = %q, want %q", f.DefValue, "text")
	}
}
