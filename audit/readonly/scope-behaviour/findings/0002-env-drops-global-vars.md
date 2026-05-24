---
severity: high
confidence: confirmed
commands: [env]
area: scope-behaviour
---
## Summary
`gale env` only ever reads `[vars]` from the *project* gale.toml.
When run from any directory without a project gale.toml in the
walk-up chain, every `[vars]` entry in the global
`~/.gale/gale.toml` is silently dropped from the output, even
though the printed `PATH=` line still points at the global
generation.

## Reproducer
```sh
export HOME=/tmp/gale-ro-audit-scope-behaviour
mkdir -p "$HOME/.gale"
cat > "$HOME/.gale/gale.toml" <<'EOF'
[packages]
jq = "1.7.1"
[vars]
GLOBAL_VAR = "world"
EOF

cd "$HOME" && /home/tcole/code/gale/gale env
# export PATH="/tmp/gale-ro-audit-scope-behaviour/.gale/current/bin:$PATH"
# (no GLOBAL_VAR — silently dropped)

cd "$HOME" && /home/tcole/code/gale/gale env --vars-only
# (empty output)
```

## Expected vs actual
Either (a) `[vars]` should be exported from the active scope's
config — global vars when env resolves to the global scope, or
(b) the README/docs should declare `[vars]` project-only and the
parser should refuse `[vars]` in the global config. The current
behaviour is to accept the section, print PATH from global, and
swallow the variables. No warning, no error.

Code path: `cmd/gale/env.go:37` calls
`config.FindGaleConfig(cwd)` which only walks up looking for a
project gale.toml; on `ErrGaleConfigNotFound` the handler returns
nil and the global config is never opened.

## Suggested investigation
Decide policy on global `[vars]`. If supported: when
`FindGaleConfig` fails, fall back to reading
`$(galeConfigDir)/gale.toml` and exporting its `[vars]`. Cross-
check with empty-state finding 0003 (malformed gale.toml silently
drops vars) — both point at the same lenient error handling in
`env.go`.
