---
name: land-todo-slice
description: >-
  Land one TODO.md PR slice: choose the next slice, write a plan,
  review the plan with three models to consensus, implement with
  red-green TDD, open a draft pull request, and drain review comments
  until CI is green and no threads remain. Use when starting a TODO
  slice, landing the next milestone PR, planning a slice, or when
  asked to address all PR review comments until they are fixed.
---

# Land one TODO.md PR slice

Do this whole procedure for one slice. Do not skip the plan review.
Do not skip the comment drain. Do not start a second slice in the
same change.

`CLAUDE.md` is the architecture contract.
`docs/dev/change-discipline.md` is how to trace before a
tier 2–3 edit. `docs/dev/style-guide.md` is the code
standard and LLM guardrails. This skill is the operating
procedure. If this skill conflicts with those files, follow
those files.

## When to use

Use this skill when the user asks to land a slice, do the next
TODO item, plan a slice, or drain PR review comments to all-clear.

Do not use this skill for a docs-only chore that is not a TODO.md
slice, unless the user maps that chore onto a named slice.

## 1. Choose the work

1. Read `TODO.md` from the start of the current milestone.
2. Fetch `origin/main` when the local default branch may be stale.
3. Confirm the local `main` matches `origin/main` before you branch.
4. Take the first unchecked **PR slice** in listed order inside
   the current milestone, unless the user named a later slice whose
   predecessors are already checked and on `main`.
5. Stop if a predecessor slice is still open. Do not start this
   slice until that predecessor is on `main`.
6. Record the exact `TODO.md` heading. That heading is the slice
   name. One heading is one pull request.

Do not combine slices. Do not pull in the next heading "while you
are here."

## 2. Plan the slice

Write a plan before you change code. Keep the plan inside this
slice.

The plan must include:

- Slice name (the `TODO.md` heading)
- Change tier from `docs/dev/change-discipline.md`. Tier 2–3
  needs the written pre-change trace in the plan.
- In scope (behavior, files, packages)
- Out of scope (the next headings, named explicitly)
- Spec impact (the relevant `docs/` file: no change,
  spec-first edit, or design-note only)
- Tests (which failing tests you will add, and at which
  layer)
- Risks (sandbox, scope fan-out, package boundaries)

Do not implement during planning.

### Spec and design-note slices

A spec slice or a design-note slice still needs user approval
before any code against that document. Plan review is not that
approval. The fetch-don't-build control-plane cuts live in
`docs/dev/proposals/fetch-dont-build.md` and `TODO.md`.

An implementation slice may proceed after plan consensus unless
the user asked you to wait.

## 3. Review the plan to consensus

Do not implement until this step ends in consensus.

Launch three independent reviewers in parallel. Give each reviewer
the plan, the slice heading from `TODO.md`, `CLAUDE.md`,
`docs/dev/change-discipline.md`, `docs/dev/style-guide.md`, and
the relevant spec or package files. Tell each reviewer to find
blocking issues. Tell each reviewer not to write code.

In Cursor, use the Task tool with these models and no substitute:

- Grok 4.6: `cursor-grok-4.6-high-fast`
- GPT-5.6-Sol: `gpt-5.6-sol-high`
- Fable 5: `claude-fable-5-thinking-high`

If a requested model is not in the available list, do not pick a
replacement. Report the missing model. Continue only when at least
two reviews return.

Each reviewer must answer:

- Is this exactly one `TODO.md` PR slice?
- Does the plan violate `CLAUDE.md`, change-discipline, or
  the style guide?
- Does the plan conflict with the named spec document?
- Are package boundaries respected (`cmd/gale` helpers in
  `context.go`; no re-implemented shared CLI)?
- Are the tests enough for the behavior change, and at the
  layer the change lives at?
- Blocking issues, non-blocking notes, and a vote: approve or
  request changes

Resolve every blocking issue in a revised plan. If reviewers
disagree, decide with the repo rules, then record the decision in
the plan. Re-run the three reviewers on the delta when the revision
adds behavior, files, or a spec change. Nit-only dissent does not
need a second round.

Consensus means: no remaining blocking objection from a completed
review.

Do not skip this step unless the user explicitly says to skip plan
review.

## 4. Implement

Follow `CLAUDE.md`, change-discipline, and the style guide.

For a behavior change:

1. Write a failing test.
2. Run that test and confirm the failure.
3. Make the minimum change.
4. Run the same test and confirm the pass.
5. Run `just`. Do not commit a failing state.

You may skip red-green TDD only when the change does not change
program behavior. Still run `just` before each commit.

Also:

- Reuse helpers in `cmd/gale/context.go`. Do not re-implement
  config resolution, generation rebuild, or install
  finalization.
- Put the test at the layer the change lives at. A green
  `internal/` test does not prove a pipeline fix.
- Errors wrap with `fmt.Errorf("context: %w", err)`.
- Format with gofumpt. Do not commit a secret.
- Do not bypass `.golangci.yml` with `#[allow]`-style
  suppressions except the existing `//nolint:gosec` pattern
  for world-readable files.
- If code disagrees with a merged spec, correct the
  specification first, then the code.
- Update `TODO.md` and `CHANGELOG.md` in the same commit as
  the applicable change.
- `gale install`, `gale build`, and `gale sync` cannot
  succeed in the agent sandbox. Do not use them as proof.
  `just preflight` is the pre-push gate.

After `just` is green, run `just preflight`. Then commit
signed (`git commit -S`). Push the branch. Open a draft pull
request unless the user asked for a ready PR.

Do not merge. Do not enable auto-merge. Do not mark the PR ready
unless the user asks.

## 5. Drain review comments to all-clear

Treat inline review threads as work. Loop until the stop
condition.

On each loop:

1. List unresolved review threads and new review comments on the
   PR. Include bot reviews (Bugbot, Security Reviewer, and similar).
2. Ignore Codex **usage-limit** issue comments. Those are not code
   review.
3. For each remaining comment, either fix it or record a brief
   reason that the repo rules reject it.
4. For a behavior fix, use red-green TDD. Run `just`. Commit. Push.
5. Resolve only the threads that the new commit actually fixes.
   In Cursor Cloud, resolve through the pull-request tool, not a
   merge command.
6. Wait for every required check to finish. `just preflight`
   is the local stand-in: test, lint, fmt-check, check-darwin,
   pipeline-check, and the other CI-ordered gates.
7. Re-list unresolved threads and new comments after CI completes.
   Start this loop again when anything remains.

Do not post a "done" comment unless the user asks. Do not leave a
valid review thread open because the bot is slow. Wait, then look
again.

### Stop condition

Stop when all of these are true:

- `just` and `just preflight` passed on the last commit
- Required CI checks are green
- There are no unresolved review threads
- No new review comment arrived after the last push

Then report the PR URL, the slice heading, and that the comment
drain is all-clear. Do not merge unless the user explicitly asks
to merge.

## What not to do

- Do not combine two `TODO.md` headings in one PR.
- Do not implement before plan consensus.
- Do not write code against a new spec or design note before the
  user approves that document.
- Do not substitute a different plan-review model when a named
  model is unavailable.
- Do not skip the comment drain after the first green CI run.
- Do not merge, enable auto-merge, or mark the PR ready unless
  the user asks.
- Do not start the next slice in this change.
- Do not prove the slice with `gale install`, `gale build`, or
  `gale sync` in the agent sandbox.
