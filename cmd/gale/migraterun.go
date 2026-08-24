package main

import "fmt"

// runMigrate is a tombstone. Fetch-adopt replaced pouring bottles
// without fixup (fetch-dont-build.md §11). It takes no context:
// constructing one can fail for reasons unrelated to migrate.
func runMigrate() error {
	return fmt.Errorf(
		"gale migrate: pouring bottles without fixup is gone; use gale install or gale fetch-adopt",
	)
}
