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
just test-symlinked-tmp  # the macOS /var path-spelling class; baseline is empty
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

### Proving a darwin test actually ran

A green `macos-26` leg does **not** prove a darwin-only test executed. CI runs
`go test ./...` without `-v`, and a skipped test is indistinguishable from a
passing one in that output. #216 shipped on exactly that assumption.

Two things do prove it, both by making the test *fail* on the macOS leg — a
skipped test cannot fail:

- **A red phase that reaches macOS.** If the test fails by assertion and CI
  names it, it ran. It must be an assertion failure: a compile error names the
  package, not the test, and proves nothing about the body executing.
- **A mutation probe.** Append an unconditional `t.Fatal("mutation probe")` to
  the **end** of the test body, push, confirm the macOS leg goes red naming it,
  then revert. Putting it last matters: a `t.Skip` still short-circuits ahead of
  it, so a genuinely-skipped test stays green and the probe correctly reports
  "did not run".

The matrix sets `fail-fast: false` so a red push reports both platforms. It did
not always: whichever leg failed first cancelled the other mid-step, so a red
push that failed on Linux produced *no* macOS evidence at all, and the probe was
the only usable route (gh#215).

## Path spelling: the macOS-only test failure

`check-darwin` compiles darwin code; it never *runs* anything. So the most
common macOS-only failure in this repo slips past it entirely: a test that
compares a raw `t.TempDir()` path against one production canonicalized.

On macOS `$TMPDIR` sits under `/var`, and `/var` is a symlink to
`private/var`, so `t.TempDir()` returns `/var/folders/…` while
`filepath.EvalSymlinks` returns `/private/var/folders/…`. On Linux `/tmp` is
a real directory, both spellings are identical, and the comparison passes.
That is what cost PR #227 a round trip.

```sh
just test-symlinked-tmp   # the suite under a macOS-shaped $TMPDIR
```

It points `$TMPDIR` at `/<name>`, a symlink to `/private/<name>`, runs
`go test -count=1 ./...`, and removes both on exit including on failure. On
macOS it is a no-op wrapper around `go test` — the real thing already runs
that way.

The shape is deliberate. macOS's resolved spelling *contains* its raw one
(`/private` + `/var/folders/…`), so assertions like
`strings.Contains(err.Error(), rawDir)` pass there. A sibling symlink
(`tmp -> tmp.real`) breaks that property and reports 5 extra `cmd/gale`
failures macOS is perfectly happy with. Keeping the raw path a substring of
the resolved one is what makes the root-level symlink necessary, which is
why the target needs `/` writable — free in this container, `sudo` on a
Linux dev box.

**The baseline is empty**, and `just preflight` runs it as its last gate. A
failure here is a path-spelling bug your branch introduced — read it as yours
rather than comparing against `origin/main`.

It was not always. Until gh#230 the target had one known failure,
`internal/farm` `TestFarmPredicateIgnoresPathSpelling` (both subtests): the
fixture hardcoded the soname `libspell.4.dylib`, and `farm.IsVersionedDylib`
switches on `runtime.GOOS`, so nothing was farmed on Linux and `StaleLinks`
came back empty. The test guards itself with a skip when the temp prefix is
not symlinked, so a symlinked `$TMPDIR` was the only thing that un-skipped it
here — and it then failed for a platform reason rather than a spelling one.
Giving the fixture the `runtime.GOOS == "linux"` switch its three siblings in
the file already had cleared it, which is what let the target join
`preflight`.

The gate costs roughly 9s on a warm cache, taking `preflight` from ~11s to
~20s. It is a full `go test -count=1 ./...` and cannot reuse the `test`
gate's cached results, because running under a different `$TMPDIR` is
exactly the point.

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
| the `macos-26` leg's path spellings | `just test-symlinked-tmp` | yes |
| integration Tier A | `just integration` | yes |
| govulncheck | not reproducible, see below | no |

`integration/` is behind `//go:build integration`, so the `test` gate never
compiles it and a green `go test ./...` says nothing about the txtar suite.
It used to be left out of `preflight` as "run it separately", and the cost
landed on gh#247: a gc-retention change passed every local gate and failed
`gc_reaps_old_revision.txtar` on **both** CI legs. Any change to gc, install,
or generation semantics can only be seen here. It roughly doubles preflight's
wall time, which is still two orders of magnitude cheaper than a CI round
trip.

Three of those exist only because the sandbox needs them.
`just pipeline-check` wraps a script CI runs on `pull_request` only, so
without it the change-discipline guard stays invisible until the PR is already
open. `just check-darwin` substitutes for a macOS runner nobody has locally,
and `just test-symlinked-tmp` covers the one macOS failure class typechecking
cannot see.

**A gate can fail because it could not run.** `preflight` distinguishes the
two, because conflating them costs a session (gh#237): a gate exiting 75
(`EX_TEMPFAIL`) prints `BLOCKED at '<name>'` rather than `FAILED`, and
`preflight` itself exits 75. `BLOCKED` means the environment got in the way and
nothing was learned about your change — re-run it, do not go hunting for a
defect. Today only the lint gate raises it, when another golangci-lint holds
the lock for the entire wait (below).

### pipeline-check and untracked files

`scripts/check-pipeline-tests.sh` scans `git diff` **plus**
`git ls-files --others --exclude-standard`, so uncommitted and untracked files
count. Before gh#237 it read the diff alone, which cannot see untracked files:
writing the new `cmd/gale/*_test.go` first — the repo's own TDD rule — made the
guard report that you had shipped a sensitive change with only `internal/`
tests, while the test it was asking for sat unstaged in the tree.

Two consequences worth knowing:

- Only `.gitignore`d files are invisible to it now. A test parked under `tmp/`
  does not count, and should not.
- The local verdict now matches what CI will say after you commit, in both
  directions. An untracked `internal/`-only test used to slip through locally
  by being invisible; it fails now, as it will on the PR.

`just pipeline-check` runs `scripts/check-pipeline-tests-selftest.sh` first —
six cases over throwaway git repos, ~0.5s, no network. It exists because the
guard decides what may land, so widening it must not launder the rule: two of
its cases assert that a sensitive change carrying only `internal/` tests still
fails. CI runs the guard alone; the cases are a local-only gate.

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

#### One lock per machine

golangci-lint takes an exclusive lock (`$TMPDIR/golangci-lint.lock`) for the
whole run, so a second run exits 3 with `Error: parallel golangci-lint is
running`. That collides more often than it sounds: several worktrees is the
normal shape here, and `preflight` invokes golangci-lint twice itself — once
for `lint`, once for `check-darwin` — so one agent can collide with its own
still-draining run.

`just lint`, `just check-darwin` and `preflight` therefore call
`scripts/golangci-lint.sh` rather than the binary. It runs golangci-lint
plainly, and only when the run dies on the lock does it do anything: name the
collision as a collision, print the process holding the lock, and queue behind
it with golangci-lint's own `--allow-serial-runners`. If the lock is still held
after `GALE_LINT_LOCK_WAIT` seconds (default 600) it gives up with exit 75, and
`preflight` reports `BLOCKED`. Real findings are untouched — they print and
exit 1 exactly as before.

The uncontended path is a plain `golangci-lint run` on the shared cache, so it
costs nothing. Giving each invocation its own `GOLANGCI_LINT_CACHE` would dodge
the lock instead, but that was measured at ~105s cold against ~1s warm on this
repo, per invocation — far more expensive than waiting out an occasional
collision.

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
