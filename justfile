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

# Scope is the whole tree, matching ci.yml's `gofumpt -l .`.
# Anything narrower lets a file outside cmd/internal/integration
# (tools/, future top-level packages) pass here and fail CI.

# Check formatting (fails if any file needs formatting)
fmt-check:
    #!/usr/bin/env bash
    set -euo pipefail
    unformatted=$(gofumpt -l .)
    if [ -n "$unformatted" ]; then
      echo "Files need formatting (run 'just fmt'):" >&2
      echo "$unformatted" >&2
      exit 1
    fi

# Fix formatting
fmt:
    gofumpt -w .

# Install the agent-sandbox toolchain. Blocks until the background
# SessionStart bootstrap finishes, so it doubles as "wait for it".
# See docs/dev/agent-environment.md.
agent-bootstrap:
    scripts/agent-bootstrap.sh --force

# Show what the agent bootstrap installed, and what failed.
agent-status:
    @cat ~/.cache/gale-agent-bootstrap/status 2>/dev/null || echo "agent bootstrap has not run — try 'just agent-bootstrap'"

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

# ~22 tests assert an EACCES-style failure and skip themselves
# when euid is 0, which is every run in an agent container. This
# compiles as root, then runs each test binary under setpriv via
# `go test -exec`, so the go build and module caches under a 0700
# $HOME stay reachable. Linux only.
# See docs/dev/agent-environment.md.

# Run the suite as an unprivileged user (unskips the root-gated tests)
test-unprivileged:
    #!/usr/bin/env bash
    set -euo pipefail
    if ! command -v setpriv >/dev/null 2>&1; then
      echo "setpriv not found (util-linux); Linux only" >&2
      exit 1
    fi
    # A HOME the test binaries can write. setpriv keeps the
    # caller's env, and root's $HOME is 0700.
    home="${TMPDIR:-/tmp}/gale-unprivileged-home"
    mkdir -p "$home"
    chmod 0777 "$home"
    go test -count=1 \
      -exec "env HOME=$home setpriv --reuid=65534 --regid=65534 --clear-groups" \
      ./...

# internal/build's fixup_darwin.go, fixup_uuid.go and
# internal/inspect's binary_darwin.go sit behind //go:build
# darwin, so on Linux `go build`, `go vet` and golangci-lint never
# compile them: an undefined symbol there passes every other local
# gate and fails on CI's macos runner. go vet covers _test.go too.

# Typecheck and vet the darwin-only sources (GOOS=darwin)
check-darwin:
    GOOS=darwin GOARCH=arm64 go build ./...
    GOOS=darwin GOARCH=arm64 go vet ./...

# ci.yml runs this on pull_request only, so without a local target
# it is invisible until the PR is already open.

# Guard change-discipline test layers (scripts/check-pipeline-tests.sh)
pipeline-check:
    scripts/check-pipeline-tests.sh origin/main

# Runs CI's gates in CI's order, stops at the first red one and
# names it. Not covered: govulncheck (its database host is blocked
# in agent containers) and integration Tier A (`just integration`).

# Reproduce the CI gate locally — run before every push
preflight:
    #!/usr/bin/env bash
    set -uo pipefail
    gate() {
      name="$1"; shift
      echo "==> preflight: $name"
      if ! "$@"; then
        echo "preflight: FAILED at '$name' — fix it before pushing" >&2
        exit 1
      fi
    }
    gate test go test ./...
    gate test-race go test -race ./...
    gate vet go vet ./...
    gate lint golangci-lint run ./...
    gate fmt-check {{ just_executable() }} fmt-check
    gate pipeline-check {{ just_executable() }} pipeline-check
    gate check-darwin {{ just_executable() }} check-darwin
    echo "preflight: all gates passed"

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

# Regenerate the embedded Sigstore trusted root from the TUF CDN.
# Run before each release; see docs/dev/releasing.md.
refresh-trusted-root:
    go run ./tools/refresh-trusted-root

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
