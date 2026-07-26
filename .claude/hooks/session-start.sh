#!/usr/bin/env bash
# SessionStart hook: install the dev toolchain this container lacks.
#
# Async so the session starts immediately. The agent may therefore land
# before golangci-lint (~90s) or a cold module cache (minutes) are ready —
# scripts/agent-bootstrap.sh takes an flock, so re-running it blocks until
# the background run finishes. That, not a polling loop, is the wait
# contract. See docs/dev/agent-environment.md.

set -uo pipefail

echo '{"async": true, "asyncTimeout": 600000}'

exec "${CLAUDE_PROJECT_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}/scripts/agent-bootstrap.sh"
