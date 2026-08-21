# CLAUDE.md

Gale is a macOS-first package manager for developer CLI
tools. Written in Go. It fetches verified artifacts from
the gale-recipes index, pins them in a v2 lock, and
activates them through generation snapshots. Design
rationale: [`docs/dev/design.md`](docs/dev/design.md).

Two repos: **gale** (this one — the CLI) and
**gale-recipes** (`../gale-recipes` — index documents
and leftover recipe TOML). `gale install jq` resolves
the index, stages `pkg/fetch/`, writes the lock, and
swaps `current` last. Not-in-index is an error. A v1
lock migrates with `gale fetch-adopt`.

`just` runs test + lint + fmt-check; `just --list` has
the rest.

## Vocabulary

**Store** (`~/.gale/pkg/`): immutable package storage.
Fetch trees live under `pkg/fetch/<name>/<version>-<sha12>/`.
Append-only.

**Generation** (`~/.gale/gen/<N>/`): a snapshot of
symlinks into the store. Rebuilt declaratively from
gale.toml on every install/remove/sync, swapped
atomically with `os.Rename`. "gen" is short for
generation.

**current** (`~/.gale/current`): symlink to the active
gen. Users put `~/.gale/current/bin` on PATH, so one
symlink swap updates bin, lib, and man together.

**Registry**: recipes fetched on demand from GitHub raw
URLs, letter-bucketed (`recipes/j/jq.toml`). No clone.

**Revision**: Debian-style `[package] revision = N`,
default 1. Store identity is
`<name>/<version>-<revision>/`; the user-facing form is
bare when revision = 1, and a bare `@version` resolves
to the highest revision known. A shared dylib farm at
`~/.gale/lib/` lets binaries absorb SONAME-compatible
dep upgrades without rebuilding.
[`docs/revisions.md`](docs/revisions.md).

## Agent Sandbox

Agent containers (Claude Code on the web and similar)
ship no toolchain. A `SessionStart` hook runs
`scripts/agent-bootstrap.sh` in the background to
install one. Full reference:
[`docs/dev/agent-environment.md`](docs/dev/agent-environment.md).

- **The bootstrap is async.** To wait for it, run it
  again — `just agent-bootstrap` takes an flock and
  blocks until the in-flight run finishes.
  `just agent-status` shows what landed.
- **`gale install`, `gale build` and `gale sync` cannot
  work here against the real index.** The egress proxy
  blocks artifact hosts, and those commands fail slowly.
  A PreToolUse hook blocks them. `gale lint` is
  offline-clean and unaffected.
- **`just preflight` is the pre-push gate.** It runs
  every reproducible CI step, fails fast, and names the
  gate. Local green without it is not CI green: darwin
  files never compile on Linux (`just check-darwin`),
  and the change-discipline guard runs on
  `pull_request` only (`just pipeline-check`). A gate that
  exits 75 prints `BLOCKED`, not `FAILED` — the
  environment got in the way and nothing was learned
  about your change; re-run rather than hunting a defect
  (gh#237).
- **The container runs as root**, so tests asserting a
  permission error skip themselves
  (`os.Geteuid() == 0`). They still run on CI, and
  `just test-unprivileged` runs them here.
- **Signed commits work here.** The container provisions
  its own ssh signing key, so `git commit -S` succeeds
  unattended. The Secretive caveat under Conventions is
  Mac-only; a signing failure here is a real failure.
- `gh` and `api.github.com` are unavailable; GitHub work
  goes through the GitHub MCP tools.

`tmp/` at the repo root is scratch space (tracked via
`tmp/.gitignore`, contents ignored). Prefer it over
`/tmp` so artifacts survive a reboot mid-task; clean up
what you create.

## Where to Look

- Editing version identity, the finalize path,
  generation/farm, or gc/sync staleness →
  [`docs/dev/change-discipline.md`](docs/dev/change-discipline.md)
  first. These are tier 2–3: trace the pipeline and grep
  callers before editing, don't patch from memory.
- Code standards and the LLM guardrails →
  [`docs/dev/style-guide.md`](docs/dev/style-guide.md).
  `.golangci.yml` enforces the mechanical half on new
  and changed code (`new-from-merge-base`; existing
  violations are fixed when a file is next touched).
- Cutting a release → [`docs/dev/releasing.md`](docs/dev/releasing.md).
  Releases are immutable once published, and the
  post-release step bumps **two** `gale.toml` files —
  skipping the second one is what caused the gen/308
  regression.
- Testing the Homebrew formula →
  [`docs/dev/homebrew-tap.md`](docs/dev/homebrew-tap.md).

## Code Reuse

New commands reuse existing helpers rather than
re-implementing them. Read
[`cmd/gale/context.go`](cmd/gale/context.go) before
adding a command — it holds every shared CLI helper
(config resolution, registry, resolver, generation
rebuild, result reporting, install finalization).

The usual shape is `newCmdContext` → `ctx.Resolver` →
`ctx.Installer.Install`, with `resolveVersionedRecipe`
for `@version` support and `finalizeInstall` for the
config + generation write. A new build mode delegates to
`build.BuildLocal` once it has a source directory.

`rebuildGenerationWith` registers the project in
`internal/projects/` immediately before the generation
rebuild so gc retains every project's active generation
(gh#115). Registration failure aborts the swap. Read-only
commands, including `gale env`, do not register.

## Conventions

- TDD: write the failing test first.
- Errors: `fmt.Errorf("context: %w", err)`.
- Format with gofumpt. Run `just hooks` once per clone
  for the pre-commit gate that mirrors CI's format
  check.
- Commits MUST be signed. Never use `--no-gpg-sign` or
  `commit.gpgsign=false`.
- **When `git commit -S` fails on the Mac** (`agent
  refused operation`, `Couldn't get agent socket`,
  signing timeout) it is always the same cause: the user
  is away from their machine, so Secretive can't
  authorize the key. Don't diagnose it, retry it, probe
  ssh-agent sockets, or hunt for a working
  `SSH_AUTH_SOCK` — none of that has ever helped. Stop,
  leave the change staged without piling on more edits,
  and tell the user signing needs them back at their
  machine. **This does not apply in an agent container**,
  which signs with its own provisioned key —
  [`docs/dev/agent-environment.md`](docs/dev/agent-environment.md).

## Gotchas

- Build PATH isolates individual tools via symlinks into
  a temp dir, keeping nix vibeutils (ls, mv) from
  leaking in and breaking autotools. See `buildPath()`
  in `internal/build/build.go`.
- Tar extraction handles PAX headers, hard links,
  symlinks, and validates paths against traversal.
  Shared `extractTar()` in `internal/download/`.
- Autotools builds need a timestamp reset (`touchAll`)
  after extraction to avoid clock-skew errors.
- Live installer verbs (`install`, `sync`, `update`,
  `remove`, `lock`) take `--index <dir>`, not
  `--recipes`. The checkout must be a git repo;
  `index.Open` reads `git show` of HEAD.
- `--recipes <dir>` remains on leftover commands
  (`outdated`, `gc`, `migrate`) until those
  packages die. `gale lint` accepts index
  documents only.
- macOS `/var` is a symlink to `/private/var`. Tests
  comparing paths must `filepath.EvalSymlinks` both
  sides. `just check-darwin` cannot catch a violation —
  it compiles darwin code, it never runs it.
  `just test-symlinked-tmp` reproduces the spelling on
  Linux; its baseline is empty and `just preflight`
  runs it, so a failure there is yours
  ([`docs/dev/agent-environment.md`](docs/dev/agent-environment.md)).
- Prefer static linking for CLI tools to avoid dylib
  path issues — `--disable-shared --enable-all-static`
  for autotools projects like jq.
- gale-recipes CI pushes binary sections after builds.
  Expect push rejections; `git pull --rebase` first.
- gosec G306 flags `os.WriteFile` with 0644. Use
  `//nolint:gosec` for world-readable files.
- `internal/attestation/` verifies in-process via
  sigstore-go — no external tool. A non-nil `Verifier`
  always verifies and fails closed; nil is a test-only
  seam that production wiring never passes.
  `GALE_SIGSTORE_TRUSTED_ROOT` overrides the trusted
  root with a local file, and `GALE_SIGSTORE_TEST_NO_SCT`
  drops the SCT requirement but only takes effect
  together with the root override.
  `internal/attestation/sigstoretest/` mints synthetic
  trust material for offline tests.

## Regression-Prone Areas

Lessons paid for in past regressions.

- Staleness checks must compare against the canonical
  version-revision a reinstall would write. On-disk
  metadata state carries meaning: a missing
  `.gale-deps.toml` and an empty one are different
  cases. Getting this wrong causes infinite
  reinstall/rebuild loops that stall direnv (013b4a4,
  688ce7d, af4c3f6).
- `internal/build/build.go` and the darwin fixup path
  are the most regression-prone code in the repo. Gate
  patchelf on `DT_NEEDED` — running `--set-rpath` on a
  static Go ELF corrupts it (75440bb). Skip `.dSYM` and
  `.o` files. Always re-sign after any Mach-O mutation,
  preserving entitlements.
- Any change to sync, gc, or remove must be exercised
  across all three scopes (global, project, `--host`).
  Cross-scope deletion bugs recur (ad4e685, 289d13b).
- In tests, never let a random port, map order, or
  temp-dir name feed a cache key or expected output; it
  produces coin-flip flakes (940a67a).

## Principles

- Fetch from the index. Source install is gone.
- Declarative over imperative (gale.toml + v2 lock →
  generation). `gale sync` does not write the lock.
