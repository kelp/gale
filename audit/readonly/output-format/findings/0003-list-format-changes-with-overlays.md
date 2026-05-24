---
severity: medium
confidence: confirmed
commands: [list]
area: output-format
---
## Summary
`gale list` silently switches output schemas based on whether
any `[hosts.*]` overlay applies. With no overlays it prints
flat `name@version` lines. With even one matching overlay it
prints grouped sections with `Shared:` / `Host (X):` headers,
two-space indents, and an inline `(overridden by host)`
annotation. Scripts that grep `gale list` break the moment a
user adds a host overlay.

## Reproducer
```sh
export HOME=/tmp/gale-ro-audit-output-format
mkdir -p "$HOME/.gale"
cat > "$HOME/.gale/gale.toml" <<'EOF'
[packages]
jq = "1.7"
ripgrep = "14.1.0"
EOF
gale list
# jq@1.7
# ripgrep@14.1.0

H=$(hostname -s)
cat > "$HOME/.gale/gale.toml" <<EOF
[packages]
jq = "1.7"
ripgrep = "14.1.0"
[hosts.$H]
[hosts.$H.packages]
bat = "0.24.0"
ripgrep = "13.0.0"
EOF
gale list
# Shared:
#   jq@1.7
#   ripgrep@14.1.0  (overridden by host)
#
# Host (one):
#   bat@0.24.0
#   ripgrep@13.0.0
```

`cmd/gale/list.go:71-80` deliberately keeps a "backward-
compatible flat output" branch only when `hostOverlayPackages`
is empty. `--scope shared` *always* uses the grouped form
(verified — it prints `Shared:\n  jq@1.7` even with no
overlays).

## Expected vs actual
Expected: a single deterministic schema (or a documented
`--format` flag for both). Users who script `gale list` can
parse one shape, not "either flat or grouped depending on
config."
Actual: schema flip is invisible from the CLI surface.
`--scope shared` and the no-flag default also disagree.

## Suggested investigation
`cmd/gale/list.go:71-113`. Decide on a single canonical
human format, and consider adding `--json` (the only
read-only command that lists installed packages and has no
machine-readable mode). `sbom --json` is *not* a substitute —
its schema is about source provenance, not pkg@ver.
