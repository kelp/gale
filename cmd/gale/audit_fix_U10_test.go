package main

import "testing"

// TestUpdateNoInstallRevisionBumpWritesVersionedPin: the
// --no-install pin-only path is gone. Update fetches from
// the index and writes the resolved version.
func TestUpdateNoInstallRevisionBumpWritesVersionedPin(t *testing.T) {
	if updateCmd.Flags().Lookup("no-install") != nil {
		t.Fatal("update --no-install must be gone")
	}
}

// TestUpdateNoInstallVersionBumpKeepsBarePin: same flag is gone.
func TestUpdateNoInstallVersionBumpKeepsBarePin(t *testing.T) {
	if updateCmd.Flags().Lookup("no-install") != nil {
		t.Fatal("update --no-install must be gone")
	}
}
