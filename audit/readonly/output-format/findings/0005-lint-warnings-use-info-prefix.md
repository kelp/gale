---
severity: low
confidence: confirmed
commands: [lint]
area: output-format
---
## Summary
`gale lint` renders warning-level issues using the cyan
informational prefix `-->` instead of the yellow warning
prefix `!!!`. Errors use the red `xxx` prefix correctly. The
visual severity hierarchy (info → warn → error) collapses to
two tiers and a user scanning a lint run cannot tell at a
glance which lines are warnings versus benign chatter.

## Reproducer
```sh
cat > /tmp/r.toml <<'EOF'
[package]
name = "test"
version = "1.0.0"

[source]
url = "https://example.com/test-1.0.0.tar.gz"
sha256 = "0000000000000000000000000000000000000000000000000000000000000000"
EOF
gale lint /tmp/r.toml
# xxx /tmp/r.toml: missing required field: build steps
# xxx /tmp/r.toml: file path "/tmp/r.toml" does not match package name "test"
# --> /tmp/r.toml: missing license          <-- "warning" issued as Info
# --> /tmp/r.toml: missing homepage         <-- "warning" issued as Info
# --> /tmp/r.toml: missing source.repo (no auto-update)
```

`cmd/gale/lint.go:38-50`:
```go
for _, issue := range issues {
    switch issue.Level {
    case "error":
        lintIssueOutput(out, ...)         // out.Error → "xxx "
    case "warning":
        out.Info(fmt.Sprintf(...))        // <-- should be out.Warn
    }
}
```

`internal/output/output.go:74-77` defines `Warn` with the
`!!!` yellow prefix expressly for this purpose. `lint` is the
only consumer that uses `out.Info` for non-info content.

## Expected vs actual
Expected: warnings rendered with `out.Warn` (`!!!` yellow,
also escalated to stderr-tier semantics if `Warn` ever grows
that). Errors rendered with `out.Error`. Info reserved for
status.
Actual: warnings disguised as info; user can't visually
distinguish them from progress prints in mixed output.

## Suggested investigation
`cmd/gale/lint.go:47` — change `out.Info` to `out.Warn`. While
there, audit the other commands for `out.Info` calls that
should be `out.Warn` (e.g. `outdated.go:57` "Skipping" is a
true skip/warning).
