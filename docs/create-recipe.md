# Creating Recipes with AI

`gale create-recipe` generates a recipe TOML file
from a GitHub repository. It uses the Anthropic API
to analyze the repo, detect the build system, and
produce a working recipe.

## Setup

Add your Anthropic API key to `~/.gale/config.toml`:

```toml
[anthropic]
api_key = "sk-ant-..."
```

## Usage

```sh
gale create-recipe jqlang/jq
```

Accepts `owner/repo`, `github.com/owner/repo`, or
a full HTTPS URL.

## Output

When run inside a gale-recipes directory, the recipe
is written directly to `recipes/<letter>/<name>.toml`.
Otherwise the recipe is printed to stdout.

Use `-o <dir>` to specify an output directory:

```sh
gale create-recipe jqlang/jq -o ~/code/gale-recipes/recipes
```

## What it does

The agent calls tools in a loop:

1. Fetches repo metadata from the GitHub API
   (description, license, homepage, latest release).
2. Reads build system files from the repo
   (configure.ac, Cargo.toml, go.mod, CMakeLists.txt)
   to detect how to build.
3. Downloads the source tarball and computes the
   real SHA256 hash.
4. Writes the recipe TOML file.
5. Runs `gale lint` on the recipe to validate.
6. Fixes any lint errors and rewrites.

## Example

From inside gale-recipes:

```
$ cd ~/code/gale-recipes
$ gale create-recipe casey/just
--> Creating recipe for casey/just...
  > Downloaded - 1.48.1.tar.gz 735.1 KB in 0.6s
==> Recipe written to recipes/j/just.toml
```

From anywhere else, the recipe prints to stdout:

```
$ gale create-recipe casey/just > just.toml
```

The generated recipe:

```toml
[package]
name = "just"
version = "1.48.1"
description = "A command runner"
license = "CC0-1.0"
homepage = "https://just.systems"

[source]
repo = "casey/just"
url = "https://github.com/casey/just/archive/refs/tags/1.48.1.tar.gz"
sha256 = "290bb320..."
released_at = "2025-01-27"

[build]
system = "cargo"
steps = [
  "cargo install --path . --root ${PREFIX}",
]

[dependencies]
build = ["rust"]
```

## Customizing the prompt

Add a `prompt_file` to your config to extend the
agent's system prompt with your own instructions:

```toml
[anthropic]
api_key = "sk-ant-..."
prompt_file = "~/.gale/recipe-prompt.md"
```

The file contents are appended to the base prompt.
Use this to encode project-specific conventions,
gotchas, or build patterns you've learned:

```markdown
## My conventions

- Always use --disable-shared for autotools projects
- For Go projects with multiple binaries, install
  all of them, not just the main one
- Include source.repo for auto-update support
```

The prompt file is read on every invocation, so
changes take effect immediately without rebuilding
gale.

## Limitations

- Requires an Anthropic API key (costs per use).
- Generates recipes but does not build them. Run
  `gale build <recipe>` to verify the recipe works.
- May need manual adjustments for complex build
  systems or unusual project layouts.
- Capped at 10 agent iterations. If the recipe
  doesn't converge, the last attempt is printed.
