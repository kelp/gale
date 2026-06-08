# Default: run tests, lint, and format check
default: test lint fmt-check

# Build the binary
build:
    go build -ldflags "-X main.version=$(just _dev-version)" -o gale ./cmd/gale/

# Run all tests
test:
    go test ./...

# Run tests with verbose output
test-v:
    go test -v ./...

# Run tests for a single package
test-pkg pkg:
    go test -v ./internal/{{pkg}}/...

# Lint with golangci-lint and go vet
lint:
    golangci-lint run ./...
    go vet ./...

# Check formatting (fails if any file needs formatting)
fmt-check:
    #!/usr/bin/env bash
    set -euo pipefail
    unformatted=$(gofumpt -l cmd internal integration)
    if [ -n "$unformatted" ]; then
      echo "Files need formatting (run 'just fmt'):" >&2
      echo "$unformatted" >&2
      exit 1
    fi

# Fix formatting
fmt:
    gofumpt -w cmd internal integration

# Install git hooks (pre-commit gofumpt check). Run once per clone.
hooks:
    git config core.hooksPath .githooks
    @echo "Installed git hooks (core.hooksPath=.githooks)"

# Show test coverage per package
cover:
    go test -cover ./...

# Run tests with race detector
test-race:
    go test -race ./...

# Run the integration suite (Tier A: fixture-driven, fast)
integration:
    go test -tags=integration -timeout 5m ./integration/...

# Run the slow integration tier (Tier B: real recipes, real GHCR)
integration-slow:
    GALE_INTEGRATION_TIER=B go test -tags=integration -timeout 15m ./integration/...

# Run all checks (test + lint + format + integration)
check: test lint fmt-check integration

# Install gale from local source using a freshly-built local
# binary. Always uses ./gale (just rebuilt) rather than whatever
# is on PATH — direnv's `use gale` activates this repo's pinned
# project gale, which may be older than current source and lack
# the resolver/install changes we're testing. See CLAUDE.md
# "Stale Local gale Binary".
install: build
    ./gale install --path . -g gale

# Bootstrap gale (first-time: build with go, self-install, install hooks)
bootstrap: build hooks
    ./gale install --path . -g gale

# Tag a release (formats, runs checks first)
tag version: fmt check
    #!/usr/bin/env bash
    set -euo pipefail
    if git tag --list | grep -q "^v{{version}}$"; then
      echo "Tag v{{version}} already exists"
      exit 1
    fi
    # Update CHANGELOG: replace "## Unreleased" with version.
    sed "s/^## Unreleased$/## v{{version}} — $(date +%Y-%m-%d)/" \
      CHANGELOG.md > CHANGELOG.tmp && mv CHANGELOG.tmp CHANGELOG.md
    git add CHANGELOG.md
    git commit -m "Release v{{version}}"
    git tag "v{{version}}"
    echo "Tagged v{{version}} — run 'just release {{version}}' to publish"
    echo "Reminder: after the release is published, bump:"
    echo "  - gale-recipes/recipes/g/gale.toml  (so users get v{{version}})"
    echo "  - gale/gale.toml                    (so this repo's dev env"
    echo "                                       activates v{{version}}; otherwise"
    echo "                                       'just install' runs a stale binary"
    echo "                                       — see CLAUDE.md 'Stale Local gale Binary')"

# Push tag — the release workflow builds, drafts, and publishes
release version:
    #!/usr/bin/env bash
    set -euo pipefail
    if ! git tag --list | grep -q "^v{{version}}$"; then
      echo "Tag v{{version}} does not exist — run 'just tag {{version}}' first"
      exit 1
    fi
    # Preflight: confirm CHANGELOG section exists; the workflow extracts it.
    if ! awk '/^## v{{version}} /{found=1; next} /^## v/{if(found) exit} found' CHANGELOG.md | grep -q .; then
      echo "No CHANGELOG section found for v{{version}}"
      exit 1
    fi
    git push origin main "v{{version}}"
    echo "Pushed v{{version}}. Release workflow will build, draft, and publish."
    echo "Watch: https://github.com/kelp/gale/actions/workflows/release.yml"
    echo "Reminder: once the release is live, bump:"
    echo "  - gale-recipes/recipes/g/gale.toml  (so users get v{{version}})"
    echo "  - gale/gale.toml                    (so this repo's dev env"
    echo "                                       activates v{{version}}; otherwise"
    echo "                                       'just install' runs a stale binary"
    echo "                                       — see CLAUDE.md 'Stale Local gale Binary')"

# Retry a failed release run (e.g. matrix flake) without re-tagging
release-retry version:
    gh workflow run release.yml --ref "v{{version}}" -f tag="v{{version}}"

# Format git describe as semver (used by build and install)
_dev-version:
    #!/usr/bin/env bash
    desc=$(git describe --tags --always)
    if [[ "$desc" =~ ^v?([0-9]+\.[0-9]+\.[0-9]+)$ ]]; then
      echo "${BASH_REMATCH[1]}"
    elif [[ "$desc" =~ ^v?([0-9]+\.[0-9]+\.[0-9]+)-([0-9]+)-g([0-9a-f]+)$ ]]; then
      echo "${BASH_REMATCH[1]}-dev.${BASH_REMATCH[2]}+${BASH_REMATCH[3]}"
    else
      echo "0.0.0-dev+${desc}"
    fi

# Clean build artifacts
clean:
    rm -f gale
