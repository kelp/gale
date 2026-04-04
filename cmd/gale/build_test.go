package main

import "testing"

func TestBuildCmdHasRecipesFlag(t *testing.T) {
	f := buildCmd.Flags().Lookup("recipes")
	if f == nil {
		t.Fatal("build command should have --recipes flag")
	}
	if f.NoOptDefVal != "auto" {
		t.Errorf("recipes flag NoOptDefVal = %q, want %q",
			f.NoOptDefVal, "auto")
	}
}
