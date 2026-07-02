// Command refresh-trusted-root regenerates
// internal/attestation/embedded_trusted_root.json from the Sigstore
// TUF CDN. Run it before each release so the offline fallback used
// when the TUF cache is unreachable (internal/attestation/trustroot.go)
// stays current. See `just refresh-trusted-root`.
package main

import (
	"fmt"
	"os"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
)

// embeddedRootPath is the checked-in snapshot refreshed by this tool.
const embeddedRootPath = "internal/attestation/embedded_trusted_root.json"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "refresh-trusted-root:", err)
		os.Exit(1)
	}
}

func run() error {
	client, err := tuf.New(tuf.DefaultOptions())
	if err != nil {
		return fmt.Errorf("create TUF client: %w", err)
	}

	data, err := client.GetTarget("trusted_root.json")
	if err != nil {
		return fmt.Errorf("fetch trusted_root.json from TUF: %w", err)
	}

	if _, err := root.NewTrustedRootFromJSON(data); err != nil {
		return fmt.Errorf("parse fetched trusted root: %w", err)
	}

	if err := os.WriteFile(embeddedRootPath, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", embeddedRootPath, err)
	}

	fmt.Println("refreshed", embeddedRootPath)
	return nil
}
