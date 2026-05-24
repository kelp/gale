---
severity: low
confidence: confirmed
commands: [audit]
area: stream-discipline
---
## Summary
`gale audit` mostly uses the `output.Output` helper (stderr,
optionally colored) but then writes the SHA256 detail lines via
direct `fmt.Fprintf(os.Stderr, ...)`, bypassing the helper's color
/ no-color / quiet logic. The same command has no stdout output
at all: pass and fail both report entirely on stderr.

## Reproducer
`cmd/gale/audit.go:73-83`:

```go
if result.SHA256 == pkg.SHA256 {
    out.Success(fmt.Sprintf(
        "%s@%s: build is reproducible", name, pkg.Version))
    fmt.Fprintf(os.Stderr, "    sha256: %s\n", pkg.SHA256)
} else {
    out.Error(fmt.Sprintf(
        "%s@%s: build differs from installed binary",
        name, pkg.Version))
    fmt.Fprintf(os.Stderr, "    installed: %s\n", pkg.SHA256)
    fmt.Fprintf(os.Stderr, "    rebuilt:   %s\n", result.SHA256)
    return fmt.Errorf("audit failed: hashes do not match")
}
```

Compared with `--quiet` mode: `out.Success` would suppress the
banner, but the raw `fmt.Fprintf` SHA lines would still print.
That violates the implicit contract of `-q`.

## Expected vs actual
Expected: either route detail lines through `out.Step` / a new
helper that honours the same quiet/color modes, or emit the
machine-readable SHA pair to stdout (which is what a caller piping
to `awk` would actually want).

Actual: detail lines are unconditionally on stderr without color
handling, and there's no stdout output at all — so callers can't
capture "the rebuilt hash" without parsing stderr.

## Suggested investigation
Two small things in `cmd/gale/audit.go:76,81,82`:

1. Replace `fmt.Fprintf(os.Stderr, ...)` with calls through
   `out.*` so quiet/color modes are respected.
2. Consider printing the hash(es) to stdout (one per line, no
   prefix) so `gale audit foo | head -1` works in scripts. The
   human-friendly banner can stay on stderr.
