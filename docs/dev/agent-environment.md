# Agent Sandbox Environment

Reference for AI coding agents (Claude Code on the web, GitHub Actions
runners, any container) working on gale. It answers, up front, the questions
an agent would otherwise burn a session rediscovering: which tools exist,
which commands work, and which ones cannot work here no matter how long you
wait.

`gale-recipes` has a companion document at
`../gale-recipes/docs/dev/agent-environment.md`; the environment facts below
apply to both repos.

## The short version

```sh
just agent-bootstrap   # install the toolchain; blocks if it is already running
just agent-status      # what landed, and what failed
just preflight         # every CI gate, in CI's order — run before you push
just test              # 21s, hermetic
just lint              # golangci-lint + go vet, 15s
just fmt-check         # gofumpt, whole tree
just check-darwin      # the darwin-only files Linux never compiles
just integration       # hermetic Tier A, fake GHCR
```

Four things to internalize before your first command:

1. **The bootstrap is asynchronous.** A `SessionStart` hook starts it in the
   background, so a fresh session may reach you before `golangci-lint` is
   built. To wait, just run the bootstrap again — it takes an flock, so a
   second invocation blocks until the first finishes and then no-ops.
2. **`gale install`, `gale build` and `gale sync` cannot work here.** They
   fail slowly. See [Blocked egress](#blocked-egress).
3. **You are root.** Tests that assert a permission error skip themselves
   rather than fail. See [Running as root](#running-as-root).
4. **Green locally is not green on CI.** Half of `internal/build` and all of
   the Mach-O code is `//go:build darwin` and is never compiled here. See
   [Darwin code is invisible on Linux](#darwin-code-is-invisible-on-linux).
   `just preflight` closes that gap and the other CI-only gates.

## Bootstrap

`scripts/agent-bootstrap.sh` installs everything the container lacks into
`~/.local/bin`, which is already first on `PATH`. It is registered as a
`SessionStart` hook in `.claude/settings.json`.

| Property | Behavior |
| --- | --- |
| Idempotent | Every step is skipped when already satisfied. |
| Serialized | An `flock` makes a second invocation block until the first finishes. **This is the wait primitive** — there is no polling protocol to learn. |
| Best-effort | One failed tool never aborts the rest; failures are recorded in the status file instead. |
| Inert off-container | No-ops unless `CLAUDE_CODE_REMOTE=true`, so it never fights a real dev machine's direnv + gale toolchain. `--force` overrides. |

Status lands in `~/.cache/gale-agent-bootstrap/status` (`just agent-status`).

What it installs, and where the pin comes from:

| Tool | Source | Pin |
| --- | --- | --- |
| `gale` | `go build ./cmd/gale/` | the worktree |
| `gofumpt` | `go install mvdan.cc/gofumpt` | `gale.toml` |
| `golangci-lint` | `go install`, see the trap below | `gale.toml` |
| `govulncheck` | `go install golang.org/x/vuln/...@latest` | latest, as CI does |
| `actionlint` | `go install github.com/rhysd/actionlint/...@latest` | latest |
| `just` | GitHub release tarball | `gale.toml` |
| `patchelf` | GitHub release tarball, Linux only | literal in the script |

`patchelf` is a test dependency, not a dev tool: six rpath tests in
`internal/build/fixup_linux_test.go` call `exec.LookPath("patchelf")` and skip
without it, which quietly strips Linux coverage from the most
regression-prone file in the repo. Its pin lives in the script rather than
`gale.toml` because `gale.toml` drives a real dev machine's direnv toolchain,
and gale is macOS-first, where patchelf means nothing.

It also warms the Go module cache, unshallows the clone (see
[new-from-merge-base](#golangci-lint)), and points `core.hooksPath` at
`.githooks`.

### The `gale` on PATH is shared, and may not be yours

The bootstrap builds `~/.local/bin/gale` from whatever repo root invoked it.
`~/.local/bin` is one directory for the whole container, so with several
worktrees checked out, the `gale` on `PATH` is whichever worktree bootstrapped
last — not necessarily the one you are editing. Nothing warns you; the binary
just behaves like someone else's branch.

Do not try to make it per-worktree. To exercise *your* build, build it
somewhere private and call it by path:

```sh
go build -o /tmp/gale-$$ ./cmd/gale/ && /tmp/gale-$$ lint recipes/j/jq.toml
```

`just agent-status` prints the repo root the shared binary came from
(`gale  ok (built from /path)`), which is the fastest way to tell whose it is.

## What is already in the container

`go`, `git`, `python3` (3.11+), `jq`, `curl`, `flock`, `gcc`/`g++`, `make`,
`cmake`, `cargo`, `node`, `ruby`, `readelf`/`objdump` (binutils), `setpriv`.

Not present, and not installed by the bootstrap: `gh`, `direnv`, `zstd`,
`shellcheck`. `direnv` in particular means `.envrc`'s `use gale` never fires —
the bootstrap is what puts the pinned toolchain on `PATH` instead.

### Go toolchain

`go` on `PATH` reports an older version than `go.mod` requires. That is fine:
`GOTOOLCHAIN=auto` transparently downloads and uses the version in the `go`
directive. `go version` inside the repo reports the real one.

## Blocked egress

Outbound HTTPS goes through an agent proxy with an allowlist. This is the
single most expensive thing to rediscover.

| Reachable | Blocked |
| --- | --- |
| `github.com/*/releases/download/*` | `pkg-containers.githubusercontent.com` (**GHCR blob host**) |
| `raw.githubusercontent.com` | `codeload.github.com` |
| `objects.githubusercontent.com` | `go.dev` |
| `ghcr.io/v2/*` (token + manifest only) | `ftp.gnu.org`, `www.zlib.net`, most upstream CDNs |
| `proxy.golang.org` | `ci-artifacts.rust-lang.org` |
| `pypi.org`, `files.pythonhosted.org` | `api.github.com` |
| `static.crates.io`, `registry.npmjs.org` | `vuln.go.dev` |

### Why `gale install` cannot work

GHCR's token and manifest endpoints resolve, so gale finds the prebuilt
binary — then the blob fetch 403s. gale falls back to a source build, and the
source hosts are blocked too. A measured `gale install just` spent 3m11s
compiling rustc before dying.

A `PreToolUse` hook (`.claude/hooks/block-gale-install.sh`) blocks
`gale install|build|sync` and `just install|bootstrap` for exactly this
reason. Prefix a command with `GALE_ALLOW_NETWORK_INSTALL=1` to override.

`just install` and `just bootstrap` are blocked for the same reason: both end
in `./gale install --path . -g gale`. There is no sandbox equivalent and you
do not need one — nothing here requires gale to be *installed into a
generation*. What `just bootstrap` would have given you, the bootstrap script
already did:

| `just bootstrap` step | Sandbox equivalent |
| --- | --- |
| `just build` | `go build -o ~/.local/bin/gale ./cmd/gale/`, or `just agent-bootstrap` |
| `just hooks` | `just agent-bootstrap` (sets `core.hooksPath`) |
| `./gale install -g gale` | nothing — no generation is needed to run gale here |

What to use instead, generally:

- `gale lint <recipe.toml>` — fully offline, and the real gate for recipe edits.
- `go build -o ~/.local/bin/gale ./cmd/gale/` — rebuild gale from source.
- `just integration` — exercises the whole install path against a hermetic
  fake GHCR server, which is the closest thing to a real install you can run
  here.

### GitHub API

`api.github.com` is blocked and `gh` is not installed. All GitHub work — PRs,
issues, CI status, comments — goes through the session's GitHub MCP tools,
scoped to `kelp/gale` and `kelp/gale-recipes`.

## Darwin code is invisible on Linux

gale is macOS-first, but this container is Linux. Every `//go:build darwin`
file is therefore excluded from the build: it is not compiled, not
typechecked, not vetted, and not linted. Six files, found with
`grep -rl '^//go:build darwin' --include='*.go' .` — `fixup_darwin.go`,
`fixup_uuid.go` and `binary_darwin.go` plus their tests, the Mach-O rpath,
codesign and UUID paths that CLAUDE.md names as the most regression-prone
code in the repo — get **zero** local signal.

The failure mode is silent. Append `func x() { doesNotExist() }` to
`fixup_darwin.go` and `go build ./...`, `go vet ./...`, `golangci-lint run`,
`go test ./...` and the pre-commit hook all stay green; CI's `macos-26` job is
the first thing that notices.

The remedy:

```sh
just check-darwin   # GOOS=darwin go build ./... && go vet ./... && golangci-lint run ./...
```

`go build` covers the non-test files; `go vet` also typechecks `_test.go`,
which `go build` never sees. The `GOOS=darwin` golangci-lint pass adds the
linters: darwin `_test.go` files are linted on the macos leg like any other
source, so a `dupl` or `funlen` hit in darwin-only test code is otherwise
invisible until the PR is open. It cannot *run* darwin tests — no execution,
no cgo, no `install_name_tool` — but it catches every undefined symbol, wrong
signature, hallucinated API and lint regression, which is the class of error
an agent introduces. Cold it takes about 75s for build+vet (a separate build
cache per GOOS) plus ~30s for the lint pass; warm, a few seconds each.
`just preflight` includes it.

The same blind spot runs the other way on CI's macos runner for
`//go:build linux` files, but nothing here is Linux-only in the same way.

## Running as root

The container runs as root, and root bypasses file permission checks. Tests
that assert an `EACCES`-style failure by `chmod`-ing a file or directory would
therefore observe success and fail.

Those tests guard themselves:

```go
if os.Geteuid() == 0 {
    t.Skip("running as root: read-only dirs are still writable")
}
```

22 tests skip this way, across `cmd/gale`, `internal/build`,
`internal/generation`, `internal/installer`, `internal/lockfile`,
`internal/projects`, `internal/provenance` and `internal/atomicfile`. They
still run, and still pass, as an unprivileged user on CI. `go test ./... -v`
plus `grep SKIP` prints the current list.

To actually run them here:

```sh
just test-unprivileged
```

It compiles as root and runs each test binary under
`setpriv --reuid=65534 --regid=65534 --clear-groups` via `go test -exec`, so
the Go build and module caches under root's 0700 `$HOME` stay reachable — the
reason a plain `setpriv go test` does not work. It also points `HOME` at a
world-writable temp dir, since `setpriv` keeps the caller's environment.

**It complements `just test`; it does not replace it.** The two runs cover
different tests, so run both:

| Run | Gains | Loses |
| --- | --- | --- |
| `just test` (root) | the 10 tests that shell out to `patchelf`, `cc` or `go build` | 22 permission tests |
| `just test-unprivileged` | those 22 | the 10 — `~/.local/bin` and the Go toolchain live under root's 0700 `$HOME`, so `exec.LookPath("patchelf")` and a nested `go build` fail for uid 65534 and those tests skip themselves |

Known failure: `TestBuildEnvHomeIsBuildScoped` fails under it once any root
`go test` has run in the container. That test sets `HOME=/host/home/value`,
and `build.TmpDir()` does `MkdirAll($HOME/.gale/tmp)` — which root happily
creates at the filesystem root, leaving a root-owned `/host/home/value/.gale/`
behind. Unprivileged runs then find that directory present but unwritable
instead of absent, so `TmpDir()` returns it rather than falling back to
`/tmp`. Removing `/host/home` clears it. The real fix belongs in the test: a
`t.TempDir()`, not a literal path outside the test's own tree.

### chmod 000 does not work as root

Do not write a new negative test around file modes and expect it to run here;
it will skip and you will have proved nothing. Where the failure you need is
structural rather than permission-based, provoke it with the filesystem
instead, and it works for every uid:

| Want | Arrange | errno |
| --- | --- | --- |
| open/read fails | a directory where a file is expected | `EISDIR` |
| any path resolution fails | a symlink pointing at itself | `ELOOP` |
| traversal fails | a regular file used as a path component | `ENOTDIR` |
| rename/create fails | a target path on a different filesystem, or a name over `NAME_MAX` | `EXDEV`, `ENAMETOOLONG` |

Prefer those. Keep the `Geteuid` guard only when the behavior under test
genuinely is permission bits.

## CI parity

`.github/workflows/ci.yml` runs on `macos-26` and `ubuntu-latest`.

**`just preflight` is the pre-push gate.** It runs every reproducible CI step
in CI's order, stops at the first failure, and names the gate that failed. Run
it before every push; a red CI on a branch several agents are about to build
on costs more than the two minutes it takes.

| CI step | Local | In `preflight` |
| --- | --- | --- |
| `go test ./...` | `just test` | yes |
| `go test -race ./...` | `just test-race` | yes |
| `go vet ./...` | part of `just lint` | yes |
| golangci-lint | `just lint` | yes |
| `gofumpt -l .` | `just fmt-check` | yes |
| `scripts/check-pipeline-tests.sh origin/main` | `just pipeline-check` | yes |
| the `macos-26` matrix leg | `just check-darwin` (typecheck + lint, no execution) | yes |
| integration Tier A | `just integration` | no — run it separately |
| govulncheck | not reproducible, see below | no |

Two of those exist only because the sandbox needs them.
`just pipeline-check` wraps a script CI runs on `pull_request` only, so
without it the change-discipline guard stays invisible until the PR is already
open. `just check-darwin` substitutes for a macOS runner nobody has locally.

CI-only, do not attempt locally:

- **govulncheck.** The bootstrap installs it and it loads packages fine, but
  its vulnerability database at `vuln.go.dev` is blocked by the egress proxy,
  so it dies with `fetching vulnerabilities: ... Forbidden`. CI filters the
  one known-unfixable advisory, `GO-2026-5932`.
- `attestation-parity.yml` — needs real GHCR and a gh token.
- `release.yml`.
- `just refresh-trusted-root` — fetches the Sigstore TUF CDN.

### golangci-lint

golangci-lint refuses to run when the Go version it was **built with** is
older than the target module's `go` directive:

```
can't load config: the Go language version (go1.25) used to build
golangci-lint is lower than the targeted Go version (1.26.4)
```

The container's preinstalled binary hits this, and so does a plain
`go install ...@latest` — golangci-lint's own `go.mod` pins an older
toolchain. The only build that works forces the toolchain:

```sh
GOTOOLCHAIN=go$(awk '/^go [0-9]/{print $2}' go.mod) \
  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v<pin>
```

The bootstrap does this. If you ever install golangci-lint by hand, do it this
way or the binary is useless.

`.golangci.yml` also sets `issues: new-from-merge-base: origin/main`, so only
lines changed relative to `origin/main` are reported. A shallow clone silently
changes which findings appear; the bootstrap unshallows for this reason.

## Working in this repo

The environment is only half the story. Before editing, read:

- [`change-discipline.md`](change-discipline.md) — pick a change tier first.
  Tier 0–1 is a skim; **tier 2–3 requires a written pre-change trace** before
  you touch code (version identity, the finalize path, generation/farm, gc and
  sync staleness). Round up when unsure.
- [`style-guide.md`](style-guide.md) — in particular its "LLM Guardrails"
  section: reuse before writing, no stubs, stay in scope, no hallucinated
  APIs, verify by running tests before claiming done.
- [`../../CLAUDE.md`](../../CLAUDE.md) — layout, shared helpers in
  `cmd/gale/context.go`, and the regression-prone areas.

Two rules that bite agents specifically:

- **TDD is mandatory.** Write the failing test first, at the right layer.
  A passing `internal/foo` test alone does not prove a tier-3 fix.
- **Commits must be signed, and signing works in this container.** The
  environment provisions its own ssh signing key and signing helper, so
  `git commit -S` succeeds unattended:

  ```
  gpg.format=ssh
  gpg.ssh.program=/tmp/code-sign        -> /opt/env-runner/environment-manager
  user.signingkey=/home/claude/.ssh/commit_signing_key.pub
  commit.gpgsign=true
  ```

  There is no Secretive here and `SSH_AUTH_SOCK` is empty — neither is
  needed. Confirm a signature landed by looking for a `gpgsig
  -----BEGIN SSH SIGNATURE-----` block in `git cat-file -p HEAD`, not with
  `git log --show-signature`: verification reports `N` / "needs to be
  configured" because `gpg.ssh.allowedSignersFile` is unset, which is a
  verification-side gap and not a signing failure. Don't try to "fix" it.

  CLAUDE.md's "stop, the user is away from their machine" rule is about
  Secretive on the Mac and does **not** apply here. A `git commit -S`
  failure in this container is a genuine problem — report it rather than
  attributing it to an absent user, and still never pass `--no-gpg-sign`.

Use `tmp/` at the repo root for scratch files, not `/tmp`.
