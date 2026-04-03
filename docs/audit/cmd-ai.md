# Audit Report: cmd-ai

## Summary

Five bugs in the AI recipe creation command and internal/ai
package. The most severe are an index panic on empty package
names (guaranteed crash), an infinite recursion when two missing
dependencies require each other (no cycle detection), and a
path traversal in the lint tool that lets the agent read
arbitrary files.

## Bugs Found

### BUG-1: Index panic on empty package name

- **File:** `cmd/gale/create_recipe.go:352`, `:310`;
  `internal/ai/tools.go:361`
- **Severity:** High
- **Category:** nil-deref / edge-case
- **Description:** Three call sites index `name[0]` to extract
  the letter bucket without checking whether `name` is empty.
  If the AI agent calls `write_recipe` with an empty `name`,
  or if `moveRecipe` is handed a file whose base name (after
  stripping `.toml`) is empty, or if `buildRecipeChecker` is
  called with an empty dep name, the program panics.
- **Code path:** Agent-supplied name, or a file named `.toml`.
- **Impact:** Process crash. Because the agent drives all three
  paths, a misbehaving model can reliably trigger this.

### BUG-2: Infinite recursion when two missing deps require each other

- **File:** `cmd/gale/create_recipe.go:204-213`
- **Severity:** High
- **Category:** logic
- **Description:** `runCreateRecipe` detects a `MISSING_DEP`
  signal, recursively creates the dependency (incrementing
  `depth`), then retries the original at the same `depth`. If
  the retry encounters the same missing dep, the depth does not
  increment, producing an infinite loop. No visited/seen set
  exists for cycle detection.
- **Code path:** `runCreateRecipe(A, depth=0)` -> MISSING_DEP B
  -> `runCreateRecipe(B, depth=1)` -> success -> retry A at
  depth=0 -> MISSING_DEP B again -> infinite loop.
- **Impact:** Infinite API calls and runaway spend against the
  Anthropic API.

### BUG-3: Download filename collision produces wrong SHA256

- **File:** `internal/ai/tools.go:94-95`
- **Severity:** Medium
- **Category:** logic
- **Description:** `downloadAndHashTool` derives the local
  filename from `filepath.Base(args.URL)`. Two different
  packages with the same archive filename (e.g., both using
  `v1.0.0.tar.gz`) silently clobber each other in the shared
  temp dir.
- **Code path:** Download package A -> stores as
  `downloadDir/v1.0.0.tar.gz`. Download package B (same tag) ->
  overwrites. Hash call for A reads B's bytes.
- **Impact:** Generated recipe contains wrong SHA256, causing
  install failures.

### BUG-4: Path traversal in lintRecipeTool

- **File:** `internal/ai/tools.go:403`
- **Severity:** Medium
- **Category:** security
- **Description:** `lintRecipeTool` accepts a `path` field from
  the agent and passes it directly to `os.ReadFile` with no
  validation that the path is inside `tmpDir`. An adversarial
  model can read arbitrary files.
- **Code path:** Agent calls `lint_recipe({"path":
  "/Users/user/.ssh/id_rsa"})` -> `os.ReadFile(...)`.
- **Impact:** Sensitive local files exfiltrated into the agent's
  context window.

### BUG-5: Unvalidated agent-supplied depRepo drives recursive recipe creation

- **File:** `cmd/gale/create_recipe.go:186-213`
- **Severity:** Medium
- **Category:** security / edge-case
- **Description:** `parseMissingDep` extracts `depRepo` from raw
  agent text with no validation. The value becomes the `repo`
  argument to a recursive `runCreateRecipe` call and is used in
  GitHub API URLs. A hallucinating model can supply arbitrary
  strings.
- **Code path:** `parseMissingDep(result)` -> `depRepo =
  parts[2]` -> `runCreateRecipe(depRepo, ...)`.
- **Impact:** Uncontrolled recursive API requests and potential
  SSRF against GitHub API.

## Test Coverage Gaps

- Empty `name` in `writeRecipeTool`: no test.
- `moveRecipe` with `.toml`-only filename: no test.
- `runCreateRecipe` retry loop termination: no tests at all.
- `downloadAndHashTool` filename collision: no test.
- `lintRecipeTool` path validation: no test.
- `parseMissingDep` with adversarial `depRepo`: only happy-path
  tested.

## Files Reviewed

- `cmd/gale/create_recipe.go`
- `cmd/gale/create_recipe_test.go`
- `cmd/gale/context.go`
- `internal/ai/ai.go`
- `internal/ai/tools.go`
- `internal/ai/prompt.go`
- `internal/ai/ai_test.go`
- `internal/ai/tools_test.go`
- `internal/ai/prompts/create-recipe.md`
