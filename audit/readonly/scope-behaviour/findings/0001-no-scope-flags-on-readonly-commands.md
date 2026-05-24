---
severity: high
confidence: confirmed
commands: [list, info, sbom, outdated, env, which, verify, audit, generations, doctor, hook]
area: scope-behaviour
---
## Summary
None of the read-only commands accept `-g`/`--global` or
`-p`/`--project`; from a project directory there is no way to ask
gale about the global scope (and vice versa). Mutation commands
(`install`, `add`, `remove`, `sync`, `update`, `switch`) have all
been wired up; the read-only siblings have been left behind.

## Reproducer
```sh
export HOME=/tmp/gale-ro-audit-scope-behaviour
mkdir -p "$HOME/.gale" "$HOME/proj"
printf '[packages]\njq = "1.7.1"\nripgrep = "14.0.0"\n' \
    > "$HOME/.gale/gale.toml"
printf '[packages]\nfd = "10.0.0"\njq = "1.6"\n' \
    > "$HOME/proj/gale.toml"

cd "$HOME/proj" && /home/tcole/code/gale/gale list -g
# Error: unknown shorthand flag: 'g' in -g

cd "$HOME/proj" && /home/tcole/code/gale/gale list --global
# Error: unknown flag: --global

# Same for info/sbom/outdated/env/which/verify/audit/generations
# /doctor/hook.
```

Static check: `cmd/gale/{list,info,sbom,outdated,env,which,
verify,audit,generations,doctor,hook}.go` declare no `-g`/`-p`
flags. `outdated.go:38`, `verify.go:30`, `audit.go:27` call
`newCmdContext("", false, false)` — scope is hard-coded to
auto-detect.

## Expected vs actual
Mutation commands (per CLAUDE.md and the cluster 0001-0003 fix)
support explicit scope. Users discovering they can `gale install
-g jq` reasonably expect `gale list -g`, `gale outdated -g`, and
`gale info -g jq` to work too. They don't — the only way to query
the global scope while sitting in a project is to `cd` out.

## Suggested investigation
Decide which read-only commands semantically *have* a scope
(list, info, sbom, outdated, env, which, verify, audit,
generations) versus which are scope-less (doctor, hook, lint,
search). Add `-g`/`-p` and route through `newCmdContext(..., g, p)`
on the first group; explicitly reject the flags on the second.
See `cmd/gale/install.go:177` (`resolveScope`) for the existing
helper.
