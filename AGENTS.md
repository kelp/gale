# AGENTS.md

Guidance for AI coding agents working in `gale`.
`CLAUDE.md` is the single source of truth; read it first.

Then, before your first command:

1. [`docs/dev/agent-environment.md`](docs/dev/agent-environment.md) — what
   the sandbox has, what the bootstrap installs, and what cannot work here.
   In particular: the toolchain arrives via an **async** `SessionStart`
   bootstrap (`just agent-bootstrap` blocks until it finishes), and
   `gale install`/`build`/`sync` cannot succeed in the sandbox.
2. [`docs/dev/change-discipline.md`](docs/dev/change-discipline.md) — pick a
   change tier before editing. Tier 2–3 needs a written pre-change trace.
3. [`docs/dev/style-guide.md`](docs/dev/style-guide.md) — the "LLM Guardrails"
   section is aimed at you.

Three rules are non-negotiable:

1. **TDD.** Write the failing test first, at the layer the change lives at.
   A green `internal/` test does not prove a pipeline fix.
2. **Reuse before writing.** `cmd/gale/context.go` holds the shared CLI
   helpers. Re-implementing one is a defect, not a nit.
3. **Commits must be signed.** If `git commit -S` fails, stop and report it.
   Never pass `--no-gpg-sign`.
