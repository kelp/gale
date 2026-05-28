#!/usr/bin/env bash
# perf-baseline.sh — capture gale vs Homebrew install timings.
#
# Authored for the performance-and-distribution loop (Tier 0,
# baseline measurement). Produces a Markdown block suitable for
# pasting into docs/dev/perf-baseline.md.
#
# This script is destructive: it removes and reinstalls each
# package multiple times via both gale and brew. It refuses to
# run without --yes; use --dry-run to preview the commands it
# would execute.
#
# Usage:
#   scripts/perf-baseline.sh --dry-run
#   scripts/perf-baseline.sh --yes
#
# Optional environment:
#   PACKAGES   — space-separated list (default: "jq fd ripgrep bat eza")
#   RUNS       — runs per scenario for median (default: 3)
#   GALE       — path to gale binary (default: gale on PATH)
#   BREW       — path to brew binary (default: brew on PATH)

set -euo pipefail

PACKAGES="${PACKAGES:-jq fd ripgrep bat eza}"
RUNS="${RUNS:-3}"
GALE="${GALE:-gale}"
BREW="${BREW:-brew}"

DRY_RUN=0
YES=0

usage() {
    cat <<EOF
Usage: $0 [--dry-run | --yes]

Captures cold and warm install times for each package via both
gale and brew, plus a multi-package sync comparison. Prints a
Markdown table to stdout.

Flags:
  --dry-run   Print commands without running anything.
  --yes       Acknowledge the destructive nature and run.
  -h, --help  Show this message.

Env:
  PACKAGES    Override package list (default: "jq fd ripgrep bat eza").
  RUNS        Runs per scenario (default: 3).
  GALE        Path to gale binary.
  BREW        Path to brew binary.
EOF
}

while [ $# -gt 0 ]; do
    case "$1" in
        --dry-run) DRY_RUN=1; shift ;;
        --yes)     YES=1; shift ;;
        -h|--help) usage; exit 0 ;;
        *)         echo "unknown flag: $1" >&2; usage >&2; exit 2 ;;
    esac
done

if [ "$DRY_RUN" -eq 0 ] && [ "$YES" -eq 0 ]; then
    echo "Refusing to mutate package state without --yes." >&2
    echo "Use --dry-run to preview the commands first." >&2
    exit 2
fi

# OS detection — Homebrew bottles differ between platforms; warn
# when running on Linux so readers know the brew numbers aren't
# directly comparable to macOS numbers.
OS=$(uname -s)
if [ "$OS" != "Darwin" ]; then
    echo "NOTE: not on macOS ($OS). Homebrew bottle behaviour may differ;" >&2
    echo "      gale numbers are still meaningful, brew numbers less so." >&2
fi

if [ "$DRY_RUN" -eq 0 ]; then
    if ! command -v "$GALE" >/dev/null 2>&1; then
        echo "gale not found on PATH (set GALE= to override)" >&2
        exit 1
    fi
    if ! command -v "$BREW" >/dev/null 2>&1; then
        echo "brew not found on PATH (set BREW= to override)" >&2
        exit 1
    fi
fi

# announce: print `+ cmd args` to stderr so the user can see
# what's running. Stderr because stdout is reserved for the
# final Markdown report — keeping previews and status off
# stdout lets the user pipe to a file. Used by both dry-run
# (preview only) and real-run (announce then execute).
announce() {
    {
        printf '+ '
        printf '%q ' "$@"
        printf '\n'
    } >&2
}

# time_cmd: prints elapsed seconds (integer) to stdout. In
# real-run mode the command itself is announced to stderr,
# then executed with its own stdout/stderr suppressed (the
# user wants to see what we ran, not pages of install spam).
# Whole-second precision is enough — installs are 5-60s and
# sub-second noise doesn't matter for baseline tracking.
time_cmd() {
    if [ "$DRY_RUN" -eq 1 ]; then
        announce "$@"
        echo 0
        return
    fi
    announce "$@"
    local start end
    start=$(date +%s)
    "$@" >/dev/null 2>&1 || true
    end=$(date +%s)
    echo $((end - start))
}

# silent_run: in dry-run mode, print the command preview. In
# real-run mode, announce + execute with stdout and stderr
# suppressed. Used for the wipe/uninstall steps where we
# don't care about the command's own output but the user
# still wants to see what ran.
silent_run() {
    announce "$@"
    if [ "$DRY_RUN" -eq 0 ]; then
        "$@" >/dev/null 2>&1 || true
    fi
}

# median of $RUNS integer numbers passed as args. Uses sort then
# picks the middle value. Assumes RUNS is odd; for even RUNS the
# lower middle is returned (good enough — we default to 3).
median() {
    printf '%s\n' "$@" | sort -n | awk -v n="$#" 'NR==int((n+1)/2) {print; exit}'
}

# Per-scenario timer: runs the wipe + install N times, returns
# median elapsed. Wipe is run once before each timed install so
# we measure cold installs.
bench_cold() {
    # Args come in as two whitespace-joined command strings so
    # callers can construct them inline without arrays. Split
    # back into argv via word-splitting — these are trusted
    # constants from the caller (package names + flags), not
    # user-supplied paths, so splitting is safe.
    # shellcheck disable=SC2206
    local wipe=($1) install=($2)
    local samples=() i elapsed
    for i in $(seq 1 "$RUNS"); do
        silent_run "${wipe[@]}"
        elapsed=$(time_cmd "${install[@]}")
        samples+=("$elapsed")
    done
    median "${samples[@]}"
}

# Warm install: assume the package is already present from the
# preceding cold run. Just time the install command N times.
bench_warm() {
    # shellcheck disable=SC2206
    local install=($1)
    local samples=() i elapsed
    for i in $(seq 1 "$RUNS"); do
        elapsed=$(time_cmd "${install[@]}")
        samples+=("$elapsed")
    done
    median "${samples[@]}"
}

# macOS ships bash 3.2 which has no associative arrays. Use four
# indexed arrays in lockstep, all sharing the same index space as
# PACKAGES_ARR. The script's shebang is `#!/usr/bin/env bash` so
# users can override via brew bash if they want, but we should
# work on whatever bash is on PATH.
# shellcheck disable=SC2206
PACKAGES_ARR=($PACKAGES)
GALE_COLD=()
BREW_COLD=()
GALE_WARM=()
BREW_WARM=()

for i in "${!PACKAGES_ARR[@]}"; do
    pkg="${PACKAGES_ARR[$i]}"
    echo "Benchmarking $pkg..." >&2

    GALE_COLD[$i]=$(bench_cold "$GALE remove -g $pkg"          "$GALE install -g $pkg")
    GALE_WARM[$i]=$(bench_warm                                  "$GALE install -g $pkg")
    BREW_COLD[$i]=$(bench_cold "$BREW uninstall --force $pkg"  "$BREW install $pkg")
    BREW_WARM[$i]=$(bench_warm                                  "$BREW reinstall $pkg")
done

# Multi-package sync: gale sync over a temp gale.toml vs brew
# install all 5 at once. Wipe both first so cold.
SYNC_DIR=$(mktemp -d)
trap 'rm -rf "$SYNC_DIR"' EXIT
{
    echo "[packages]"
    for pkg in $PACKAGES; do
        echo "$pkg = \"latest\""
    done
} > "$SYNC_DIR/gale.toml"

# Wipe everything once.
for pkg in $PACKAGES; do
    silent_run "$GALE" remove -g "$pkg"
    silent_run "$BREW" uninstall --force "$pkg"
done

GALE_SYNC=$(
    if [ "$DRY_RUN" -eq 1 ]; then
        announce "$GALE" sync "(in $SYNC_DIR)"
        echo 0
    else
        start=$(date +%s)
        ( cd "$SYNC_DIR" && "$GALE" sync >/dev/null 2>&1 ) || true
        echo $(( $(date +%s) - start ))
    fi
)

# Wipe brew side again before the multi-install timing.
for pkg in $PACKAGES; do
    silent_run "$BREW" uninstall --force "$pkg"
done

BREW_SYNC=$(time_cmd "$BREW" install $PACKAGES)

# Capture --verbose phase timing for one cold install. Use the
# first package as the sample. Strip everything except [timing]
# lines so the output is paste-ready.
echo "Capturing --verbose phase timing for $(echo "$PACKAGES" | awk '{print $1}')..." >&2
SAMPLE=$(echo "$PACKAGES" | awk '{print $1}')
PHASE_TIMING=""
if [ "$DRY_RUN" -eq 1 ]; then
    PHASE_TIMING="(dry-run: would capture from '$GALE --verbose install -g $SAMPLE')"
else
    "$GALE" remove -g "$SAMPLE" >/dev/null 2>&1 || true
    PHASE_TIMING=$("$GALE" --verbose install -g "$SAMPLE" 2>&1 | grep '^\[timing\]' || true)
    if [ -z "$PHASE_TIMING" ]; then
        PHASE_TIMING="(no [timing] lines captured — confirm --verbose is wired)"
    fi
fi

# Emit the Markdown block on stdout. Everything above this
# point goes to stderr (status), so a `> baseline.md` redirect
# captures just the report.
cat <<EOF
### Per-package install (seconds, median of $RUNS)

| package | gale cold | brew cold | gale warm | brew warm |
|---------|-----------|-----------|-----------|-----------|
EOF
for i in "${!PACKAGES_ARR[@]}"; do
    printf "| %-7s | %9s | %9s | %9s | %9s |\n" \
        "${PACKAGES_ARR[$i]}" "${GALE_COLD[$i]}" "${BREW_COLD[$i]}" \
        "${GALE_WARM[$i]}" "${BREW_WARM[$i]}"
done

cat <<EOF

### Multi-package install (seconds, single run, ${#PACKAGES_ARR[@]} packages)

| operation               | seconds |
|-------------------------|---------|
| gale sync               | $GALE_SYNC |
| brew install (all pkgs) | $BREW_SYNC |

### Phase timing breakdown ($SAMPLE cold install, --verbose)

\`\`\`
$PHASE_TIMING
\`\`\`
EOF
