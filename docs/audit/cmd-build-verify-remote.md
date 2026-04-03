# Audit Report: cmd-build-verify-remote

## Summary

Seven bugs in the build, audit, verify, lint, remote, and repo
commands. Three are critical security issues in `remote.go`
(SSH/SCP option injection and remote code execution via
curl-pipe). Two high-severity bugs in `repo.go` (add/remove
do not persist to config.toml).

## Bugs Found

### BUG-1: SSH option injection via unvalidated host argument

- **File:** `cmd/gale/remote.go:211`
- **Severity:** Critical
- **Category:** security
- **Description:** `sshCmd` prepends `host` as the first
  positional argument to `exec.Command("ssh", ...)`. SSH
  parses arguments starting with `-` as options. A host value
  like `-oProxyCommand=malicious_cmd` causes SSH to execute an
  arbitrary command on the local machine. The argument is
  passed directly from user CLI input with no validation.
- **Code path:** `gale remote sync <host>` -> `sshCmd(host, ...)`
  -> `exec.Command("ssh", host, ...)`.
- **Impact:** Local arbitrary command execution. Same vector in
  `runRemoteDiff` and `uploadAndSync`.

### BUG-2: SCP option injection via unvalidated host in destination

- **File:** `cmd/gale/remote.go:100`
- **Severity:** Critical
- **Category:** security
- **Description:** The SCP destination is `host+":~/.gale/gale.toml"`.
  If `host` contains `-oProxyCommand=cmd`, SCP forwards the
  option to SSH.
- **Code path:** `uploadAndSync(out, configPath, host)` ->
  `exec.Command("scp", configPath, host+":...")`.
- **Impact:** Local arbitrary command execution via ProxyCommand.

### BUG-3: Unverified remote code execution via curl-pipe bootstrap

- **File:** `cmd/gale/remote.go:122`
- **Severity:** Critical
- **Category:** security
- **Description:** `bootstrapRemote` fetches and executes a shell
  script from GitHub without integrity check:
  `curl -fsSL https://...install.sh | sh`. No SHA256
  verification, no pinned commit.
- **Code path:** `runRemoteSync` -> host lacks gale ->
  `bootstrapRemote(host)`.
- **Impact:** If the GitHub URL is tampered with, malicious code
  runs on every remote host.

### BUG-4: repo add does not persist to config.toml

- **File:** `cmd/gale/repo.go:36-46`
- **Severity:** High
- **Category:** logic
- **Description:** `repoAddCmd` calls `mgr.AddRepo(...)` (memory
  only) and `mgr.Clone(name)` (disk only). Neither writes to
  `~/.gale/config.toml`. When the command exits, the
  registration is gone. The cloned directory remains on disk but
  is never referenced.
- **Code path:** `gale repo add <name> <url>` -> memory + clone
  -> command exits, memory discarded.
- **Impact:** `gale repo add` always appears to succeed but has
  no lasting effect.

### BUG-5: repo remove does not update config.toml

- **File:** `cmd/gale/repo.go:63-68`
- **Severity:** High
- **Category:** logic
- **Description:** `repoRemoveCmd` deletes the cache directory
  but does not remove the entry from `~/.gale/config.toml`.
  Future consumers see the repo listed but its cache is gone.
- **Code path:** `gale repo remove foo` -> `os.RemoveAll` ->
  config.toml untouched.
- **Impact:** Subsequent operations fail trying to read the
  deleted cache directory.

### BUG-6: audit and verify resolve lockfile from wrong scope

- **File:** `cmd/gale/audit.go:27`, `cmd/gale/verify.go:34`
- **Severity:** Medium
- **Category:** logic
- **Description:** Both call `resolveConfigPath(false)` for the
  lockfile, then `newCmdContext("")` independently for the
  installer. Outside a project directory,
  `resolveConfigPath(false)` returns `<cwd>/gale.toml`
  (nonexistent) while `newCmdContext` falls back to global
  config. The lockfile read fails.
- **Code path:** `gale audit <pkg>` or `gale verify <pkg>` from
  any directory without a local `gale.toml`.
- **Impact:** Both commands fail outside project directories,
  unusable for global packages.

### BUG-7: lint.go reports errors using out.Warn instead of out.Error

- **File:** `cmd/gale/lint.go:41`
- **Severity:** Medium
- **Category:** logic
- **Description:** When a lint issue has level `"error"`, the
  code calls `out.Warn(...)` (yellow) instead of `out.Error(...)`
  (red). Exit code is correct but visual output misrepresents
  severity.
- **Code path:** `lint.go:39-43: case "error": out.Warn(...)`.
- **Impact:** Errors appear as warnings visually.

## Test Coverage Gaps

- `remote.go` has zero tests.
- `repo.go` has zero tests.
- `audit.go` has zero tests.
- `verify.go` has zero tests.
- `create_recipe_test.go` tests only `parseMissingDep` and
  `buildRecipeChecker`, providing no coverage for these files.

## Files Reviewed

- `cmd/gale/build.go`
- `cmd/gale/audit.go`
- `cmd/gale/verify.go`
- `cmd/gale/lint.go`
- `cmd/gale/remote.go`
- `cmd/gale/repo.go`
- `cmd/gale/create_recipe_test.go`
- `cmd/gale/context.go`
- `cmd/gale/paths.go`
- `cmd/gale/recipes.go`
- `internal/build/build.go`
- `internal/repo/repo.go`
- `internal/config/config.go`
- `internal/trust/trust.go`
- `internal/output/output.go`
