---
severity: medium
confidence: confirmed
commands: [env]
area: empty-state
---
## Summary
`gale env` swallows every gale.toml read/parse error
with `return nil //nolint:nilerr` and prints the PATH
export anyway. `gale env --vars-only` against a
malformed gale.toml produces an empty stdout, no
stderr, and exit 0 — a silent failure mode that
breaks `eval "$(gale env --vars-only)"` consumers
without any signal.

## Reproducer
```sh
mkdir -p $HOME/.gale
cat > $HOME/.gale/gale.toml <<'EOF'
[packages
jq = not_quoted
EOF
gale env --vars-only
echo "exit=$?"
# (empty stdout, empty stderr, exit=0)

gale env
echo "exit=$?"
# export PATH="/.../.gale/current/bin:$PATH"
# (no warning that [vars] could not be read)
```

The harness recorded `env_--vars-only.txt` for the
`malformed-toml` state as 0 bytes stdout, 0 bytes
stderr, exit 0.

## Expected vs actual
Expected: at minimum, a stderr warning that the
config could not be parsed and `[vars]` were dropped.
Better: exit non-zero so the calling shell knows
the eval was incomplete.

Actual: `cmd/gale/env.go:32-48`:

```go
cwd, err := os.Getwd()
if err != nil {
    return nil //nolint:nilerr // best-effort
}
configPath, err := config.FindGaleConfig(cwd)
if err != nil {
    return nil //nolint:nilerr // no project config
}
data, err := os.ReadFile(configPath)
if err != nil {
    return nil //nolint:nilerr // best-effort
}
cfg, err := config.ParseGaleConfig(string(data))
if err != nil {
    return nil //nolint:nilerr // best-effort
}
```

Four consecutive silent fall-throughs. The
ParseGaleConfig one is the dangerous case — `data`
is non-empty and parseable as a file, but the user
intended for it to define vars. The hook script in
`cmd/gale/hook.go` literally calls
`eval "$(gale env --vars-only 2>/dev/null)" || true`,
which means a malformed gale.toml will leave a direnv
shell with stale or missing project vars and no user-
visible signal beyond `gale doctor` (which the user
has no reason to run).

The "best-effort" comment makes sense for the cwd/find
cases (no project config is normal). It is wrong for
the parse-error case: that is not a "missing config",
that is a "broken config", and silently emitting an
empty result is worse than failing.

## Suggested investigation
- Split the four error paths: only `getwd` and
  "no config found" should return nil. `ReadFile`
  failure on a path we just resolved, and
  `ParseGaleConfig` failure, deserve a stderr
  warning at minimum, and an exit code in
  vars-only mode where stdout is otherwise empty.
- The same nilerr pattern likely appears in other
  read-only commands — grep for `nilerr.*best-effort`
  in `cmd/gale/`.
