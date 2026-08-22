package main

import "testing"

// TestUpdateNoInstallWritesPinSkipsBuild: --no-install is gone.
// Update always fetches and publishes. The old split flow
// (bump pin, then sync) is not part of the live installer.
func TestUpdateNoInstallWritesPinSkipsBuild(t *testing.T) {
	if updateCmd.Flags().Lookup("no-install") != nil {
		t.Fatal("update --no-install must be gone")
	}
}

// TestUpdateNoInstallSkipsUpToDate: same flag is gone.
func TestUpdateNoInstallSkipsUpToDate(t *testing.T) {
	if updateCmd.Flags().Lookup("no-install") != nil {
		t.Fatal("update --no-install must be gone")
	}
}
