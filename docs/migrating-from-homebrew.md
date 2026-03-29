# Migrating from Homebrew

Move your CLI tools from Homebrew to gale one package
at a time. No need to uninstall Homebrew first.

## Import a Formula

Gale can generate a recipe from a Homebrew formula:

```sh
gale import homebrew jq
```

This prints a TOML recipe to stdout. Review it, then
save it to the recipes directory if you maintain your
own recipes:

```sh
gale import homebrew jq > recipes/j/jq.toml
```

Most popular tools already have recipes in the gale
registry. Check first:

```sh
gale search jq
```

## Install via Gale

```sh
gale install jq
```

Gale fetches the recipe, downloads a prebuilt binary
(or builds from source), and adds `jq` to your
`gale.toml` manifest.

## Verify the Installation

```sh
gale which jq
jq --version
```

`gale which` shows the full path to the binary and
which package provides it.

## Remove from Homebrew

Once the gale-installed binary works:

```sh
brew uninstall jq
```

If `~/.gale/current/bin` is before `/opt/homebrew/bin`
in your PATH, the gale binary takes priority even
before you uninstall from Homebrew.

## Repeat for Each Tool

Work through your Homebrew packages one at a time:

```sh
brew list --formula
```

For each formula:

1. `gale search <name>` -- check for an existing recipe
2. `gale install <name>` -- install it
3. `gale which <name>` -- verify
4. `brew uninstall <name>` -- remove the Homebrew copy

## Coverage

Not all Homebrew formulas have gale recipes yet. Gale
focuses on developer CLI tools. GUI applications,
system libraries, and niche packages may not have
recipes.

If a recipe does not exist, you can import the formula
and contribute it:

```sh
gale import homebrew <name> > recipe.toml
gale lint recipe.toml
gale build recipe.toml
```

Review the generated recipe, fix any lint warnings,
and test the build before submitting.
