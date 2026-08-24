package main

import (
	"fmt"

	"github.com/kelp/gale/internal/output"
)

// runMigrate is a tombstone. Fetch-adopt replaced pouring bottles
// without fixup (fetch-dont-build.md §11).
func runMigrate(_ *cmdContext, _ *output.Output) error {
	return fmt.Errorf(
		"gale migrate: pouring bottles without fixup is gone; use gale install or gale fetch-adopt",
	)
}
