---
severity: medium
confidence: confirmed
commands: [outdated]
area: stream-discipline
---
## Summary
`gale outdated` writes its non-empty result lines to stdout but
writes the "no packages installed" and "everything up to date"
states to stderr, so `gale outdated | wc -l` reports 0 for the
two empty states and a positive number for the populated state —
indistinguishable from `wc -l` on a hypothetical machine-parseable
empty list.

## Reproducer
Empty config (or no `gale.toml`):

```
$ gale outdated 2>/dev/null            # stdout only
                                       # (nothing)
$ gale outdated 1>/dev/null            # stderr only
--> No packages installed.
```

Populated config but everything current: same — message lands on
stderr via `out.Success(...)`, stdout is empty.

Populated config with a newer version available:

```
$ gale outdated 2>/dev/null            # stdout only
jq 1.7.1 → 1.8.1-4
```

## Expected vs actual
Expected: the empty / up-to-date states either emit a sentinel to
stdout (e.g. `gale list`'s "No packages installed." goes to stdout
— same wording, different stream) or, for scripting, emit nothing
to stdout. Pick one.

Actual: data on stdout, status on stderr — except for the two
"nothing to do" states, which silently go to stderr. A consumer
script can't tell "no packages declared" from "everything up to
date" from "list of outdated entries" using stdout alone.

## Suggested investigation
`cmd/gale/outdated.go:48-80`:

```go
if len(cfg.Packages) == 0 {
    out.Info("No packages installed.")     // stderr
    return nil
}
...
if len(items) == 0 {
    out.Success("Everything is up to date.") // stderr
    return nil
}
for _, line := range formatOutdated(items) {
    fmt.Println(line)                       // stdout
}
```

Compare with `cmd/gale/list.go:54,73` where the same "No packages
installed." string is written to stdout via `fmt.Fprintln(w, ...)`.
The two commands should agree on which stream carries the empty
state.
