# Audit Report: cmd-env-shell

## Summary

Four bugs in the environment and shell commands. The most
critical is `prependPATH`, which silently fails to inject the
gale bin directory into child-process environments, meaning
`gale shell` and `gale run` never actually activate the project
environment. No test files exist for any of the five commands.

## Bugs Found

### BUG-1: prependPATH appends duplicate PATH -- gale bin dir is ignored

- **File:** `cmd/gale/shell.go:88-94`
- **Severity:** Critical
- **Category:** logic
- **Description:** `prependPATH` calls `os.Environ()`, which
  returns a slice that already contains a `PATH=...` entry, then
  appends a second `PATH=binDir:...` entry at the end. On Linux
  and macOS, `execve(2)` passes the full env slice to the child,
  and `getenv(3)` returns the first matching key. The appended
  entry at the end is silently ignored. The gale bin directory
  is never placed on the child's PATH.
- **Code path:** `shellCmd.RunE` -> `prependPATH(binDir)` ->
  `exec.Command(shell).Run()` (also `runCmd.RunE`).
- **Impact:** `gale shell` and `gale run` appear to succeed but
  the spawned shell does not have project binaries on PATH.
  This defeats the primary purpose of both commands.

### BUG-2: syncIfNeeded silently swallows all errors

- **File:** `cmd/gale/shell.go:61-84`
- **Severity:** High
- **Category:** error-handling
- **Description:** Every error path in `syncIfNeeded` is
  discarded with a bare `return` or `_ = runSync(...)`. When
  sync fails for any reason, the function returns silently.
- **Code path:** `shellCmd.RunE` -> `syncIfNeeded()` ->
  `runSync("", false, false)` (return value discarded).
- **Impact:** Users launch shells with incomplete environments
  and receive no warning.

### BUG-3: syncIfNeeded ignores --project flag

- **File:** `cmd/gale/shell.go:21,61-84`
- **Severity:** High
- **Category:** logic
- **Description:** `syncIfNeeded` always calls `os.Getwd()` to
  locate the config file. `shellCmd` accepts `--project <dir>`
  (stored in `shellProject`) but `syncIfNeeded` is called
  before that logic and has no access to `shellProject`. When
  a caller runs `gale shell --project /other/path` from an
  unrelated directory, `syncIfNeeded` syncs the wrong project.
- **Code path:** `shellCmd.RunE` -> `syncIfNeeded()` which
  reads `os.Getwd()`, not `shellProject`.
- **Impact:** `gale shell --project /other/path` may have an
  incomplete environment for the target project.

### BUG-4: env.go uses Go %q format for shell exports

- **File:** `cmd/gale/env.go:56`
- **Severity:** Medium
- **Category:** logic
- **Description:** `fmt.Fprintf(out, "export %s=%q\n", k,
  cfg.Vars[k])` uses Go's `%q` verb, which produces Go-syntax
  escape sequences (`\n`, `\t`, `\xNN`, `\uNNNN`). POSIX sh
  does not interpret these inside double quotes. A value
  containing a newline is emitted as `"foo\nbar"` but the shell
  stores the literal string `foo\nbar`, not `foo<LF>bar`.
- **Code path:** `envCmd.RunE` -> sort vars -> `fmt.Fprintf`.
  Also reached via `eval "$(gale env --vars-only)"`.
- **Impact:** `[vars]` entries with whitespace, newlines, or
  non-ASCII characters are exported with wrong values.

## Test Coverage Gaps

- `shell.go`, `run.go`, `env.go`, `hook.go`, and `doctor.go`
  have zero test files.
- `prependPATH` would be caught by a simple unit test.
- `syncIfNeeded` error suppression is untested.
- `env.go` var export has no test for special characters.
- `doctor.go` PATH check doesn't use `filepath.EvalSymlinks`.

## Files Reviewed

- `cmd/gale/shell.go`
- `cmd/gale/run.go`
- `cmd/gale/env.go`
- `cmd/gale/hook.go`
- `cmd/gale/doctor.go`
- `cmd/gale/context.go`
- `cmd/gale/paths.go`
- `internal/env/env.go`
- `internal/env/env_test.go`
