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

No emojis. No Co-Authored-By lines.

## Code

Idiomatic Go unless noted below. Run gofumpt, not
gofmt. Lint with golangci-lint.

### Error handling

Wrap with context:

```go
return fmt.Errorf("installing %s: %w", name, err)
```

Context is a noun phrase describing the operation,
not the failure. "installing jq" not "failed to
install jq" — the error already says what failed.

### Testing

TDD. Write the failing test before the fix.

Table-driven for multiple inputs. Subtests with
`t.Run` for named cases. Temp dirs (`t.TempDir()`)
for filesystem operations.

Use `filepath.EvalSymlinks` when comparing paths
on macOS (`/var` → `/private/var`).

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
