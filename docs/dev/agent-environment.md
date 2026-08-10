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
just test              # 21s, hermetic
just lint              # golangci-lint + go vet, 15s
just fmt-check         # gofumpt
just integration       # hermetic Tier A, fake GHCR
```

Three things to internalize before your first command:

1. **The bootstrap is asynchronous.** A `SessionStart` hook starts it in the
   background, so a fresh session may reach you before `golangci-lint` is
   built. To wait, just run the bootstrap again — it takes an flock, so a
   second invocation blocks until the first finishes and then no-ops.
2. **`gale install`, `gale build` and `gale sync` cannot work here.** They
   fail slowly. See [Blocked egress](#blocked-egress).
3. **You are root.** Tests that assert a permission error skip themselves
   rather than fail. See [Running as root](#running-as-root).

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

It also warms the Go module cache, unshallows the clone (see
[new-from-merge-base](#golangci-lint)), and points `core.hooksPath` at
`.githooks`.

## What is already in the container

`go`, `git`, `python3` (3.11+), `jq`, `curl`, `flock`, `gcc`/`g++`, `make`,
`cmake`, `cargo`, `node`, `ruby`.

Not present, and not installed by the bootstrap: `gh`, `direnv`, `zstd`,
`patchelf`, `shellcheck`. `direnv` in particular means `.envrc`'s `use gale`
never fires — the bootstrap is what puts the pinned toolchain on `PATH`
instead.

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

What to use instead:

- `gale lint <recipe.toml>` — fully offline, and the real gate for recipe edits.
- `go build -o ~/.local/bin/gale ./cmd/gale/` — rebuild gale from source.
- `just integration` — exercises the whole install path against a hermetic
  fake GHCR server, which is the closest thing to a real install you can run
  here.

### GitHub API

`api.github.com` is blocked and `gh` is not installed. All GitHub work — PRs,
issues, CI status, comments — goes through the session's GitHub MCP tools,
scoped to `kelp/gale` and `kelp/gale-recipes`.

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

They still run, and still pass, as an unprivileged user on CI. If you add a
negative test that depends on file modes, add the same guard — and check it by
running the compiled test binary under `setpriv --reuid=65534`, not by
reasoning about it.

## CI parity

`.github/workflows/ci.yml` runs on `macos-26` and `ubuntu-latest`.

Reproducible locally, and worth running before you push:

| CI step | Local |
| --- | --- |
| `go test ./...` | `just test` |
| `go test -race ./...` | `just test-race` |
| `go vet ./...` | part of `just lint` |
| golangci-lint | `just lint` |
| `gofumpt -l .` | `just fmt-check` — **note CI checks `.`, the justfile only checks `cmd internal integration`**; run `gofumpt -l .` directly before pushing |
| `scripts/check-pipeline-tests.sh origin/main` | same command |
| integration Tier A | `just integration` |

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
