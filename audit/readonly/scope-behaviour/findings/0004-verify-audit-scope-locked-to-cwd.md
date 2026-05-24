---
severity: medium
confidence: confirmed
commands: [verify, audit]
area: scope-behaviour
---
## Summary
`gale verify <pkg>` and `gale audit <pkg>` resolve the lockfile
via `newCmdContext("", false, false)`, so they look at the
project lockfile when cwd is inside a project and the global
lockfile otherwise. There is no flag to invert this — verifying
a global install while inside a project is impossible without
`cd`'ing out (or vice versa).

## Reproducer
```sh
export HOME=/tmp/gale-ro-audit-scope-behaviour
mkdir -p "$HOME/.gale" "$HOME/proj"
printf '[packages]\njq = "1.7.1"\n' > "$HOME/.gale/gale.toml"
printf '[packages.jq]\nversion = "1.7.1"\nsha256 = "deadbeef"\n' \
    > "$HOME/.gale/gale.lock"
printf '[packages]\nfd = "10.0.0"\n' > "$HOME/proj/gale.toml"

# Project cwd: project lockfile has no jq, so verify fails even
# though jq is installed globally.
cd "$HOME/proj" && /home/tcole/code/gale/gale verify jq
# Error: jq not found in lockfile — install it first

# Same story for audit:
cd "$HOME/proj" && /home/tcole/code/gale/gale audit jq
# Error: jq not found in lockfile — install it first
```

Code: `cmd/gale/verify.go:30`, `cmd/gale/audit.go:27` both pass
`false, false` to `newCmdContext`, and `verify.go:36`/`audit.go:33`
build `lockfilePath(ctx.GalePath)` from the auto-detected scope.

## Expected vs actual
Both commands are inherently per-package operations; users want
to ask "is the *globally installed* jq authentic / reproducible?"
without leaving the project. Today the answer is "not via this
command from here." Combined with finding 0001, the only
workaround is to leave the project directory.

## Suggested investigation
Same fix as 0001: thread `-g`/`-p` into `verify` and `audit` so
`newCmdContext` resolves the right gale.toml/gale.lock pair.
