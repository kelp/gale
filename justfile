# Default: run tests and lint
default: test lint

# Build the binary
build:
    go build -ldflags "-X main.version=$(git rev-parse --short HEAD)" -o gale ./cmd/gale/

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
    gofumpt -l .

# Fix formatting
fmt:
    gofumpt -w .

# Show test coverage per package
cover:
    go test -cover ./...

# Run tests with race detector
test-race:
    go test -race ./...

# Run all checks (test + lint + format)
check: test lint fmt-check

# Install gale from local source using gale itself
install:
    gale install --source . gale

# Bootstrap gale (first-time: build with go, then self-install)
bootstrap: build
    ./gale install --source . gale

# Tag a release (runs checks first)
tag version: check
    #!/usr/bin/env bash
    set -euo pipefail
    if git tag --list | grep -q "^v{{version}}$"; then
      echo "Tag v{{version}} already exists"
      exit 1
    fi
    # Update CHANGELOG header.
    sed -i '' "s/^## v.*-dev.*/## v{{version}} — $(date +%Y-%m-%d)/" CHANGELOG.md
    git add CHANGELOG.md
    git commit -m "Release v{{version}}"
    git tag "v{{version}}"
    echo "Tagged v{{version}} — run 'just release {{version}}' to publish"

# Push tag and create GitHub release
release version:
    #!/usr/bin/env bash
    set -euo pipefail
    if ! git tag --list | grep -q "^v{{version}}$"; then
      echo "Tag v{{version}} does not exist — run 'just tag {{version}}' first"
      exit 1
    fi
    git push origin main "v{{version}}"
    gh release create "v{{version}}" \
      --title "v{{version}}" \
      --notes-file RELEASENOTES.md
    echo "Published https://github.com/kelp/gale/releases/tag/v{{version}}"

# Clean build artifacts
clean:
    rm -f gale
