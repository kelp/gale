# Gale

macOS-first package manager for developer CLI tools.
Written in Go.

## Build & Test

```
go build ./cmd/gale/          # build
go test ./...                  # all tests
go test ./internal/recipe/...  # single package
go vet ./...                   # lint
```

## Project Layout

```
cmd/gale/          CLI entry point (cobra commands)
internal/recipe/   TOML recipe parsing
internal/config/   gale.toml and config.toml parsing
internal/store/    package store directory management
internal/output/   colored terminal output
internal/download/ HTTP fetch, SHA256, extraction
internal/profile/  symlink management (~/.gale/bin/)
internal/lockfile/ gale.lock read/write
internal/env/      PATH building, shell hooks
internal/repo/     recipe repository management
internal/trust/    ed25519 signing and verification
internal/ai/       Anthropic SDK integration
```

## Conventions

- Error handling: return errors, wrap with
  `fmt.Errorf("context: %w", err)`
- Testing: table-driven tests, temp directories for
  filesystem operations
- No panics in library code
- Keep packages focused — one responsibility each
