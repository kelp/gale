# Style Guide

## Writing

Follow Strunk & White. Omit needless words. Use active
voice. Make definite assertions.

Short sentences. One idea per sentence. If a sentence
has a semicolon, consider splitting it.

## Documentation

### Man pages

Write in OpenBSD mandoc style:

- Terse. One fact per sentence.
- Imperative mood for commands: "Install a package"
  not "This command installs a package."
- No marketing language. Describe behavior.
- Consistent grammar across all entries.
- Use mdoc macros: `.Nm`, `.Ar`, `.Fl`, `.Ev`, `.Pa`.
- Include an EXAMPLES section with practical,
  copy-paste-ready commands. Enough to get started,
  not a tutorial.

### README

The README serves two audiences: someone deciding
whether to use gale, and someone who just installed it.

- Opening paragraph: what it does, in plain language.
  Not a sales pitch. State facts.
- Get Started section: install command, PATH setup,
  first package install. Three minutes to working.
- Commands section: one-line descriptions, aligned.
  Reference `man gale` for details.
- How It Works: brief architecture for the curious.
  Store, generations, recipes. Three paragraphs max.
- Development section: clone, bootstrap, common tasks.
  No explanation of why — just the commands.

### Code comments

Comment why, not what. The code shows what.

Exception: complex algorithms, non-obvious platform
workarounds, and suppressed lint warnings (`//nolint`)
get explanatory comments.

### Commit messages

First line: what changed (50 chars max). Imperative
mood ("Add", "Fix", "Remove", not "Added", "Fixed").

Body (if needed): why it changed. Not how — the diff
shows how.

No emojis.

## Code

Idiomatic Go unless noted below. Run gofumpt, not
gofmt. Lint with golangci-lint (`just lint`).

The rules below draw from the Uber Go Style Guide,
the Google Go Style Guide, and Go Code Review
Comments. Most are enforced mechanically by the
linters in `.golangci.yml`; the rest are review
discipline. Where a linter enforces a rule, it is
named in parentheses.

Adoption is incremental. `.golangci.yml` grandfathers
violations that predate the strict config
(`new-from-merge-base`). New and changed code must
pass the full set; existing hits are fixed as files
are next touched.

### Error handling

Wrap with context:

```go
return fmt.Errorf("installing %s: %w", name, err)
```

Context is a noun phrase describing the operation,
not the failure. "installing jq" not "failed to
install jq" — the error already says what failed.

More rules:

- Use `%w` to let callers match the wrapped error;
  use `%v` only to deliberately hide the underlying
  error (`errorlint`).
- Match errors with `errors.Is`/`errors.As`, never
  `==` or a type switch on the concrete type
  (`errorlint`).
- Choose the error form by how callers use it: a
  static, unmatched message → inline `errors.New`;
  a dynamic message → inline `fmt.Errorf`; a matched
  condition → exported sentinel `var ErrFoo` or a
  custom `FooError` type.
- Error strings are lowercase and unpunctuated: "no
  such recipe" not "No such recipe." (`revive`
  error-strings).
- Handle each error once. Either wrap-and-return or
  log-and-stop — never both, which double-reports.
- Never return `(nil, nil)` (`nilnil`). Never return
  a nil error when one occurred (`nilerr`).
- Use comma-ok on every type assertion; a bare
  `x.(T)` panics on mismatch (`forcetypeassert`).
- No panics in library code. `cmd/gale` may exit
  with a non-zero status; `internal/` returns errors.

### Control flow

- Return early; keep the happy path at the leftmost
  indentation (`revive` early-return,
  indent-error-flow).
- No `else` after a `return`/`break`/`continue`
  (`revive` superfluous-else).
- No unreachable or dead code (`revive`
  unreachable-code, `unused`).

### Concurrency and memory

- A zero-value `sync.Mutex` is ready to use. Do not
  use a pointer to a mutex, and do not embed one in
  an exported struct (it leaks `Lock`/`Unlock`).
- Copy slices and maps at API boundaries. Storing a
  caller's slice, or returning an internal one, lets
  the other side mutate your state.
- Channels are unbuffered or buffered to 1. A larger
  buffer needs a comment justifying the size.
- Every goroutine has a clear exit. Wait for it
  (`sync.WaitGroup`) or cancel it (`context`). No
  fire-and-forget.
- `context.Context` is the first parameter, named
  `ctx`, and is threaded through, never stored in a
  struct or dropped (`contextcheck`).

### Function shape

Keep functions short and single-purpose. Extract a
helper when a function outgrows a screen or mixes
concerns (`funlen` ≤ 80 lines, `gocognit` < 30).

Keep signatures narrow: at most five parameters
(`revive` argument-limit) and three results
(`revive` function-result-limit). Past that, pass a
config struct or use functional options.

### Abstraction

Find the balance between too much and too little.

- Accept interfaces, return concrete types.
- Do not add an interface until there are two
  implementations or a real need to mock in tests.
  No speculative abstraction (YAGNI).
- Never take or return a pointer to an interface.
- Assert interface conformance at compile time:
  `var _ Resolver = (*registryResolver)(nil)`.
- Receivers are consistent: if any method needs a
  pointer receiver, all do (`revive`
  receiver-naming).
- Prefer composition over embedding in exported
  structs; embedding leaks the inner type's API.
- Use functional options for extensible
  constructors.

### Duplication and dead code

- No copy-pasted logic. Extract a shared helper, or
  reuse an existing one (see Shared helpers and
  CLAUDE.md "Code Reuse"). `dupl` flags blocks over
  ~150 tokens, in tests as well as production code.
- No unused functions, variables, parameters, or
  consts (`unused`, `unparam`).
- Delete commented-out code. Git remembers it.

### Structs and naming hygiene

- Initialize structs with field names; never
  positionally.
- Omit fields left at their zero value.
- A nil slice is valid and idiomatic. Prefer
  `var s []T` over `[]T{}`.
- Do not shadow builtins (`error`, `len`, `string`)
  (`predeclared`).
- No redundant type conversions (`unconvert`); use
  stdlib constants over magic values
  (`usestdlibvars`).

### Testing

TDD. Write the failing test before the fix, and
confirm it fails first.

- Table-driven for multiple inputs. Subtests with
  `t.Run` for named cases.
- Factor shared setup into helpers; do not copy a
  fixture block between tests. `dupl` is enforced on
  `_test.go` for this reason — duplicated test setup
  should become a helper or a table case, not be
  silenced. Use `t.Helper()` in test helpers.
- Assert behavior, not implementation. Mock only
  external boundaries (network, filesystem when
  unavoidable); do not over-mock. `internal/`
  interfaces like `Verifier` take nil in tests
  rather than a mock.
- Failure messages read "got X, want Y".
- Tests are deterministic: no wall-clock, randomness,
  or shared global state. Temp dirs (`t.TempDir()`)
  for filesystem work.
- Use `filepath.EvalSymlinks` when comparing paths
  on macOS (`/var` → `/private/var`).
- CI runs `go test -race`; keep tests race-clean.

### Comments and docs

Comment why, not what (see Documentation → Code
comments above).

- Every exported symbol has a doc comment that
  begins with its name and is a full sentence.
- No comment that restates the code. No stale
  comment: update it with the code it describes.

### Packages

One responsibility per package. No circular imports.
No panics in library code.

`cmd/gale/` is flat — one file per command, shared
helpers in `context.go` and `paths.go`.

`internal/` packages export only what other packages
need. Keep the API surface small.

### Shared helpers

Before writing new code, check `cmd/gale/context.go`
for CLI helpers and the relevant `internal/` package
for domain helpers.

Key reuse points:

- `newCmdContext` — config, store, resolver setup
- `finalizeInstall` — config + lock + generation
- `writeConfigAndLock` — config + lock (no generation)
- `resolveVersionedRecipe` — @version resolution
- `reportResult` — install/update output
- `lockfilePath` — derive .lock path from .toml path
- `build.TmpDir` — scratch space in `~/.gale/tmp/`
- `download.HashFile` — SHA256 of a file

### Naming

Use industry-standard terms. "Store" not "cellar."
"Generation" not "profile." "Recipe" not "formula."

Short variable names in small scopes (`r` for recipe,
`cfg` for config). Descriptive names in larger scopes
or exported APIs.

### Recipe format

Recipes are TOML. Required fields: `[package]` name
and version, `[source]` url and sha256, `[build]`
steps. Build steps reference `${PREFIX}`, `${VERSION}`,
`${JOBS}`, `${OS}`, `${ARCH}`, `${PLATFORM}`.

Binary metadata lives in separate `.binaries.toml`
files, not in the recipe. Version history lives in
`.versions` files.

## LLM Guardrails

Standards this project enforces because AI coding
assistants regress on them. They restate rules from
above as imperatives, plus a few that no linter
catches. Follow them when generating or editing code
here. For tier 2–3 changes, read
[`change-discipline.md`](change-discipline.md) first.

- **Trace before edit (tier ≥2).** Pick a change tier
  (see change-discipline). For tier 2–3, write the
  six-point pre-change trace (invariant, pipeline,
  caller grep, commands, test anchors, blast radius)
  before editing code. Grep at change time — interfaces
  change; invariants do not.
- **Reuse before writing.** Search
  `cmd/gale/context.go` and the relevant `internal/`
  package before adding a helper. Re-implementing
  something that already exists is a defect, not a
  style nit. See CLAUDE.md "Code Reuse."
- **No stubs or fakes.** No `return nil // TODO`, no
  placeholder values, no implementation that pretends
  to work. Implement it fully or stop and ask.
- **Stay in scope.** Change what the task needs and
  no more. No drive-by refactors, no reformatting
  untouched code. Match the surrounding file's style.
- **No hallucinated APIs.** Every call resolves to a
  real signature. Run `just build` and `go vet`
  before claiming the work compiles.
- **Verify before claiming done.** Run the relevant
  tests and report the result, including failures and
  their output. "Done" means observed-passing, not
  assumed. Separate what you confirmed from what you
  expect.
- **TDD is mandatory.** Write the failing test first
  and confirm it fails before writing the fix.
- **Pipeline bugs need pipeline tests.** A fix in
  `internal/foo` alone is insufficient when the failure
  is config ↔ generation skew, staleness loops, or gc
  retention. Put the repro in `cmd/gale/` or
  `integration/` at the tier change-discipline
  recommends.
- **Do not swallow errors or drop edge cases.** An
  ignored error or an unhandled nil/empty/zero input
  is a bug, even when the happy path works.
