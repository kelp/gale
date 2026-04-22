# Default: run tests and lint
default: test lint

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

# Check formatting
fmt-check:
    gofumpt -l cmd internal

# Fix formatting
fmt:
    gofumpt -w cmd internal

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

# Install gale from local source using gale itself
install:
    gale install --path . -g gale

# Bootstrap gale (first-time: build with go, then self-install)
bootstrap: build
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
    echo "Reminder: bump gale-recipes/recipes/g/gale.toml to v{{version}} after the release is published."

# Push tag and create GitHub release
release version:
    #!/usr/bin/env bash
    set -euo pipefail
    if ! git tag --list | grep -q "^v{{version}}$"; then
      echo "Tag v{{version}} does not exist — run 'just tag {{version}}' first"
      exit 1
    fi
    # Extract release notes from CHANGELOG.md for this version.
    NOTES=$(awk '/^## v{{version}} /{found=1; next} /^## v/{if(found) exit} found' CHANGELOG.md)
    if [ -z "$NOTES" ]; then
      echo "No CHANGELOG section found for v{{version}}"
      exit 1
    fi
    git push origin main "v{{version}}"
    gh release create "v{{version}}" \
      --title "v{{version}}" \
      --notes "$NOTES"
    echo "Published https://github.com/kelp/gale/releases/tag/v{{version}}"
    echo "Release workflow will build and attach binaries."
    echo "Reminder: bump gale-recipes/recipes/g/gale.toml to v{{version}} once the release is live."

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
