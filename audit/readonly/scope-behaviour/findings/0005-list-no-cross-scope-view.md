---
severity: medium
confidence: confirmed
commands: [list]
area: scope-behaviour
---
## Summary
`gale list` shows exactly one scope — the project's gale.toml if
one is found via walk-up, otherwise the global's. There is no
`--all`/`-a` and no `-g`/`-p` flag to widen or override. The
existing `--scope all|shared|host` only filters within the single
config that was selected; it does not bridge project↔global.

## Reproducer
```sh
export HOME=/tmp/gale-ro-audit-scope-behaviour
mkdir -p "$HOME/.gale" "$HOME/proj"
printf '[packages]\njq = "1.7.1"\nripgrep = "14.0.0"\n' \
    > "$HOME/.gale/gale.toml"
printf '[packages]\nfd = "10.0.0"\njq = "1.6"\n' \
    > "$HOME/proj/gale.toml"

cd "$HOME/proj" && /home/tcole/code/gale/gale list
# fd@10.0.0
# jq@1.6
# (no way to see ripgrep — it's global-only)

cd "$HOME/proj" && /home/tcole/code/gale/gale list --scope all
# same output as above; --scope is about shared/host overlays,
# not project/global

cd "$HOME/proj" && /home/tcole/code/gale/gale list -g
# Error: unknown shorthand flag: 'g' in -g
```

Code: `cmd/gale/list.go:42` — `config.FindGaleConfig(cwd)` walks
up; on miss falls back to `~/.gale/gale.toml`. No alternative
path is reachable from inside a project.

## Expected vs actual
The flag named `--scope` strongly implies project/global control
to anyone reading the help text; it actually means
shared/host-overlay filtering within a single scope. This is a
naming collision waiting to surprise users — separate from the
"can't see both at once" UX problem.

## Suggested investigation
Either rename the existing flag (e.g. `--section`) and reserve
`--scope` for project/global, or add `-g`/`-p` alongside it.
Consider an `--all`/`-a` that prints both project and global
sections side-by-side (the `Shared:` / `Host (<key>):` block
machinery in `list.go:82-108` is already suited to multi-section
output and could be reused for `Project:` / `Global:`).
