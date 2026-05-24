---
severity: medium
confidence: confirmed
commands: [info]
area: scope-behaviour
---
## Summary
`gale info <pkg>` checks the project gale.toml first and returns
on the first hit, so when the same package is in both project and
global config the global entry is unreachable. With no
`-g`/`--global` flag (see finding 0001), there is no way to ask
"what version is the *global* jq?" from inside a project.

## Reproducer
```sh
export HOME=/tmp/gale-ro-audit-scope-behaviour
mkdir -p "$HOME/.gale" "$HOME/proj"
printf '[packages]\njq = "1.7.1"\n' > "$HOME/.gale/gale.toml"
printf '[packages]\njq = "1.6"\n' > "$HOME/proj/gale.toml"

cd "$HOME/proj" && /home/tcole/code/gale/gale info jq
# Name:    jq
# Version: 1.6
# Scope:   project
# Config:  /tmp/gale-ro-audit-scope-behaviour/proj/gale.toml
```

`cmd/gale/info.go:27-47` — project lookup short-circuits; the
global branch only runs if the package is missing from the
project config.

## Expected vs actual
`info` is the natural diagnostic when a tool resolves to an
unexpected version. The current output tells the user about the
project scope only — fine when both versions match, misleading
when they don't. A user wondering "but I have a newer jq globally,
why is `which jq` pointing at 1.6?" is not helped by `info`.

Mutation siblings (`install`, `remove`, `add`) honour `-g` to
target the global config explicitly; `info` should too.

## Suggested investigation
Two options worth weighing:
1. Always print both scopes when the package appears in each;
   keep the "first hit" optimisation only when one side misses.
2. Wire `-g`/`-p` into `info` (per finding 0001) so users can
   force the scope. Cleaner, matches the mutation-command model.
