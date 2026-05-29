#!/usr/bin/env bash
# perf-baseline.sh — capture gale install/sync timings.
#
# Runs in an ISOLATED gale environment (HOME pointed at a tmpdir
# that's wiped on exit). Your real ~/.gale/, ~/.gale/gale.toml,
# and installed packages are NOT touched. Earlier versions of
# this script mutated the user's real global gale.toml; that
# was a design bug and is fixed.
#
# Optionally compares against Homebrew via --with-brew. We only
# use `brew reinstall` — never `brew uninstall` — so your real
# brew state is preserved (reinstall is roughly cold-install time
# for bottled packages anyway).
#
# Usage:
#   scripts/perf-baseline.sh --dry-run            # preview only
#   scripts/perf-baseline.sh --yes                # gale-only baseline
#   scripts/perf-baseline.sh --yes --with-brew    # also benchmark brew
#
# Optional environment:
#   PACKAGES   — space-separated list (default: "jq fd ripgrep bat eza")
#   RUNS       — runs per scenario for median (default: 3)
#   GALE       — path to gale binary. Unset (default): build gale from
#                HEAD and measure that. Set: use it as-is (skips the
#                build; for VMs or release-vs-HEAD comparisons).
#   BREW       — path to brew binary (default: brew on PATH)

set -euo pipefail

PACKAGES="${PACKAGES:-jq fd ripgrep bat eza}"
RUNS="${RUNS:-3}"
BREW="${BREW:-brew}"

# GALE explicitly set by the caller? If so, honour it as-is (used by
# VMs that pre-build and copy a binary in, or to compare a released
# gale against HEAD). If not, we build gale from HEAD below and point
# GALE at the freshly built binary — the baseline must never silently
# measure a stale installed release.
if [ -n "${GALE:-}" ]; then
    GALE_EXPLICIT=1
else
    GALE_EXPLICIT=0
fi

REPO_ROOT=$(cd "$(dirname "$0")/.." && pwd)

DRY_RUN=0
YES=0
WITH_BREW=0
ALLOW_SOURCE=0

usage() {
    cat <<EOF
Usage: $0 [--dry-run | --yes] [--with-brew]

Times cold and warm gale installs in an isolated gale HOME (your
real ~/.gale/ is untouched). Prints a Markdown table to stdout.

Flags:
  --dry-run     Print commands without running anything.
  --yes         Acknowledge the script will spin up a temp HOME
                and download recipes/binaries into it.
  --with-brew   Also benchmark 'brew reinstall <pkg>' for each
                package. NEVER uninstalls — your real brew state
                is preserved.
  --allow-source Skip the binary-install preflight. By default
                 the harness refuses to benchmark a package whose
                 install path would fall back to source build —
                 compile time (especially for Rust packages
                 pulling in the toolchain) drowns the signal we
                 care about. Pass this when you genuinely want
                 source-build numbers.
  -h, --help    Show this message.

Env:
  PACKAGES      Override package list (default: "jq fd ripgrep bat eza").
  RUNS          Runs per scenario (default: 3).
  GALE          Path to gale binary. Unset: build from HEAD and
                measure that. Set: use as-is (skips the build).
  BREW          Path to brew binary.
EOF
}

while [ $# -gt 0 ]; do
    case "$1" in
        --dry-run)      DRY_RUN=1; shift ;;
        --yes)          YES=1; shift ;;
        --with-brew)    WITH_BREW=1; shift ;;
        --allow-source) ALLOW_SOURCE=1; shift ;;
        -h|--help)      usage; exit 0 ;;
        *)              echo "unknown flag: $1" >&2; usage >&2; exit 2 ;;
    esac
done

if [ "$DRY_RUN" -eq 0 ] && [ "$YES" -eq 0 ]; then
    echo "Refusing to run without --yes (this script downloads recipes" >&2
    echo "and binaries into a temp dir; pass --dry-run to preview)." >&2
    exit 2
fi

OSNAME=$(uname -s)
ARCH=$(uname -m)
if [ "$OSNAME" != "Darwin" ]; then
    echo "NOTE: not on macOS ($OSNAME). Homebrew bottle behaviour may differ;" >&2
    echo "      gale numbers are still meaningful, brew numbers less so." >&2
fi

# expected_dev_version: the version string a HEAD build embeds.
# Mirrors the justfile — prefer `just _dev-version`, else fall back to
# raw `git describe` (which the go-build fallback below embeds).
expected_dev_version() {
    if command -v just >/dev/null 2>&1; then
        (cd "$REPO_ROOT" && just _dev-version 2>/dev/null)
    else
        (cd "$REPO_ROOT" && git describe --tags --always 2>/dev/null)
    fi
}

# Resolve the gale binary under test. Build from HEAD unless the
# caller pinned GALE explicitly. Runs even under --dry-run so the
# preview reflects the exact version that would be measured.
if [ "$GALE_EXPLICIT" -eq 1 ]; then
    if ! command -v "$GALE" >/dev/null 2>&1; then
        echo "GALE=$GALE not found or not executable" >&2
        exit 1
    fi
    GALE_VERSION=$("$GALE" --version 2>&1 | head -1)
    echo "Using caller-specified gale: $GALE ($GALE_VERSION)" >&2
    case "$GALE_VERSION" in
        *-dev.*) : ;; # looks like a HEAD build
        *)
            echo "WARNING: '$GALE_VERSION' has no -dev. marker — this may be" >&2
            echo "         a released binary, not a build from HEAD." >&2
            ;;
    esac
else
    echo "Building gale from HEAD ($REPO_ROOT)..." >&2
    if command -v just >/dev/null 2>&1 && command -v go >/dev/null 2>&1; then
        (cd "$REPO_ROOT" && just build >&2) || {
            echo "just build failed" >&2
            exit 1
        }
    elif command -v go >/dev/null 2>&1; then
        _ver=$(expected_dev_version)
        _ver=${_ver:-dev}
        (cd "$REPO_ROOT" && go build -ldflags "-X main.version=$_ver" -o gale ./cmd/gale/) || {
            echo "go build failed" >&2
            exit 1
        }
    else
        echo "Cannot build gale: neither 'just'+'go' nor 'go' is available." >&2
        echo "Install Go (and just), or set GALE= to a HEAD-built binary." >&2
        exit 1
    fi
    GALE="$REPO_ROOT/gale"
    GALE_VERSION=$("$GALE" --version 2>&1 | head -1)
    # Assert the freshly built binary reports the HEAD version, so a
    # botched build can't masquerade as a valid measurement.
    EXPECTED=$(expected_dev_version)
    if [ -n "$EXPECTED" ] && ! printf '%s' "$GALE_VERSION" | grep -qF "$EXPECTED"; then
        echo "ERROR: built gale reports '$GALE_VERSION' but HEAD is" >&2
        echo "       '$EXPECTED'. Refusing to measure a mismatched binary." >&2
        exit 1
    fi
    echo "Built gale from HEAD: $GALE ($GALE_VERSION)" >&2
fi

if [ "$DRY_RUN" -eq 0 ] && [ "$WITH_BREW" -eq 1 ] && ! command -v "$BREW" >/dev/null 2>&1; then
    echo "brew not found on PATH (set BREW= to override or drop --with-brew)" >&2
    exit 1
fi

# Isolated HOME for every gale invocation. gale uses
# os.UserHomeDir(), which honours $HOME on Unix, so this redirects
# its ~/.gale/ to $ISO_HOME/.gale/. Your real ~/.gale/ stays
# untouched. We DON'T copy your real gale.toml in — the point is
# a fresh measurement against the actual install pipeline (cold
# cache, no prior state).
ISO_HOME=$(mktemp -d -t gale-perf-baseline.XXXXXX)
trap 'rm -rf "$ISO_HOME"' EXIT
echo "Isolated gale HOME: $ISO_HOME (auto-cleaned on exit)" >&2

# Bridge gh CLI credentials from the real HOME into the isolated
# one. Without this, gale's attestation verifier (which shells
# out to `gh attestation verify`) sees no auth, every binary
# install fails attestation, and the harness measures source
# builds instead of binary installs.
#
# Symlinks rather than copies so the real config keeps working
# unchanged. ~/.config/gh holds hosts.yml + config.yml.
if [ -d "$HOME/.config/gh" ]; then
    mkdir -p "$ISO_HOME/.config"
    ln -s "$HOME/.config/gh" "$ISO_HOME/.config/gh"
fi

# gale_iso runs gale with HOME overridden. Use this for every
# gale invocation in this script — direct `gale` calls would hit
# the user's real ~/.gale/.
gale_iso() {
    HOME="$ISO_HOME" "$GALE" "$@"
}

# announce: print `+ cmd args` to stderr so the user can see
# what's running. Stderr because stdout is reserved for the
# final Markdown report.
announce() {
    {
        printf '+ '
        printf '%q ' "$@"
        printf '\n'
    } >&2
}

# time_cmd: print announcement, run command (output silenced),
# emit elapsed seconds to stdout. Whole-second precision is
# enough for install times of 5-60s.
time_cmd() {
    announce "$@"
    if [ "$DRY_RUN" -eq 1 ]; then
        echo 0
        return
    fi
    local start end
    start=$(date +%s)
    "$@" >/dev/null 2>&1 || true
    end=$(date +%s)
    echo $((end - start))
}

# silent_run: announce + run, ignoring output. Used for setup/
# teardown steps (wipe isolated HOME, set up fixtures) where
# the command's output isn't interesting.
silent_run() {
    announce "$@"
    if [ "$DRY_RUN" -eq 0 ]; then
        "$@" >/dev/null 2>&1 || true
    fi
}

# median of N integers passed as args. RUNS defaults to 3 →
# sorted middle is index 2 (1-indexed). For even RUNS this
# returns the lower middle.
median() {
    printf '%s\n' "$@" | sort -n | awk -v n="$#" 'NR==int((n+1)/2) {print; exit}'
}

# wipe_iso_home blows away $ISO_HOME/.gale so the next install
# starts from a cold isolated cache. Safe — it only touches the
# tempdir.
wipe_iso_home() {
    silent_run rm -rf "$ISO_HOME/.gale"
}

# preflight_binary_only: verify a package's install path resolves
# to a prebuilt binary, not a source build. If gale hits the
# "warning: binary install for X@Y failed: …; falling back to
# source build" path, the eventual benchmark would measure
# compile time (especially bad for Rust packages — the toolchain
# bootstrap alone is multiple minutes). Refuse to benchmark in
# that case unless ALLOW_SOURCE is set.
#
# Uses the isolated HOME and wipes between checks. Returns 0 on
# binary-install success, non-zero on source fallback or any
# other failure shape.
preflight_binary_only() {
    local pkg=$1
    local out
    if [ "$DRY_RUN" -eq 1 ]; then
        announce "preflight $pkg (dry-run, skipped)"
        return 0
    fi
    announce "preflight $pkg"
    wipe_iso_home
    if ! out=$(gale_iso install -g "$pkg" 2>&1); then
        echo "preflight FAILED: $pkg install errored" >&2
        echo "  Last 5 lines:" >&2
        printf '%s\n' "$out" | tail -5 >&2
        return 1
    fi
    if printf '%s\n' "$out" | grep -qF "falling back to source"; then
        echo "preflight FAILED: $pkg falls back to source build" >&2
        echo "  Reason:" >&2
        printf '%s\n' "$out" | grep -F "warning: binary install" | head -3 >&2
        return 1
    fi
    if ! printf '%s\n' "$out" | grep -qF "from binary"; then
        echo "preflight FAILED: $pkg did not install from binary" >&2
        echo "  Last 5 lines of gale output:" >&2
        printf '%s\n' "$out" | tail -5 >&2
        return 1
    fi
    return 0
}

# bench_cold_gale: $RUNS cold install measurements for $1.
# Wipes the isolated cache between each run.
bench_cold_gale() {
    local pkg=$1
    local samples=() i elapsed
    for i in $(seq 1 "$RUNS"); do
        wipe_iso_home
        elapsed=$(time_cmd gale_iso install -g "$pkg")
        samples+=("$elapsed")
    done
    median "${samples[@]}"
}

# bench_warm_gale: $RUNS warm install measurements for $1.
# Assumes the package is already installed from a preceding cold
# run — no wipe between runs.
bench_warm_gale() {
    local pkg=$1
    local samples=() i elapsed
    for i in $(seq 1 "$RUNS"); do
        elapsed=$(time_cmd gale_iso install -g "$pkg")
        samples+=("$elapsed")
    done
    median "${samples[@]}"
}

# bench_brew_reinstall: $RUNS `brew reinstall` measurements.
# Brew reinstall = uninstall + install internally but leaves
# the brew state at its starting point afterward, so this is
# safe to run on a real dev machine.
bench_brew_reinstall() {
    local pkg=$1
    local samples=() i elapsed
    for i in $(seq 1 "$RUNS"); do
        elapsed=$(time_cmd "$BREW" reinstall "$pkg")
        samples+=("$elapsed")
    done
    median "${samples[@]}"
}

# shellcheck disable=SC2206
PACKAGES_ARR=($PACKAGES)
GALE_COLD=()
GALE_WARM=()
BREW_REINST=()

# Preflight: refuse to benchmark anything that would source-build
# (would mix compile time into the binary-install signal). Runs
# before any timing so failures surface fast.
if [ "$DRY_RUN" -eq 0 ]; then
    echo "Preflighting packages (verifying binary-install path)..." >&2
    PREFLIGHT_FAILED=()
    for pkg in "${PACKAGES_ARR[@]}"; do
        if ! preflight_binary_only "$pkg"; then
            PREFLIGHT_FAILED+=("$pkg")
        fi
    done
    if [ "${#PREFLIGHT_FAILED[@]}" -gt 0 ]; then
        echo "" >&2
        if [ "$ALLOW_SOURCE" -eq 1 ]; then
            echo "WARNING: ${#PREFLIGHT_FAILED[@]} package(s) will source-build:" >&2
            echo "  ${PREFLIGHT_FAILED[*]}" >&2
            echo "Continuing because --allow-source is set." >&2
        else
            echo "ERROR: ${#PREFLIGHT_FAILED[@]} package(s) would source-build:" >&2
            echo "  ${PREFLIGHT_FAILED[*]}" >&2
            echo "" >&2
            echo "Remove them via PACKAGES env, fix the missing binary in" >&2
            echo "gale-recipes, or pass --allow-source to benchmark source" >&2
            echo "builds (numbers will then mix compile + install)." >&2
            exit 1
        fi
    fi
fi

for i in "${!PACKAGES_ARR[@]}"; do
    pkg="${PACKAGES_ARR[$i]}"
    echo "Benchmarking $pkg..." >&2
    GALE_COLD[$i]=$(bench_cold_gale "$pkg")
    GALE_WARM[$i]=$(bench_warm_gale "$pkg")
    if [ "$WITH_BREW" -eq 1 ]; then
        BREW_REINST[$i]=$(bench_brew_reinstall "$pkg")
    fi
done

# Multi-package sync: wipe the isolated cache, build a gale.toml with
# all packages, then time `gale sync -g`. Measures the parallel-install
# win (T1.2).
#
# Build the config with `gale add` (which resolves each package to its
# real registry version and writes the pin) rather than hand-writing
# `pkg = "latest"`. The registry pins exact versions — it has no
# "latest"/"*" alias — so a literal "latest" makes sync fail fast and
# `time_cmd` (which swallows errors via `|| true`) would record a
# bogus ~0s. `add` is setup, not timed; the `sync` below is the work.
echo "Benchmarking multi-package sync ($PACKAGES)..." >&2
wipe_iso_home
for pkg in "${PACKAGES_ARR[@]}"; do
    silent_run gale_iso add -g "$pkg"
done

# Time the sync ourselves (not via time_cmd) so a non-zero exit is
# surfaced as FAILED instead of masquerading as a fast run.
announce gale_iso sync -g
if [ "$DRY_RUN" -eq 1 ]; then
    GALE_SYNC=0
else
    _sync_start=$(date +%s)
    if gale_iso sync -g >/dev/null 2>&1; then
        GALE_SYNC=$(($(date +%s) - _sync_start))
    else
        GALE_SYNC="FAILED"
        echo "WARNING: 'gale sync -g' returned non-zero — recording the" >&2
        echo "         sync result as FAILED rather than a bogus time." >&2
    fi
fi

# Verbose phase timing capture for the first package's cold
# install. Strip everything except [timing] lines so the output
# is paste-ready.
SAMPLE="${PACKAGES_ARR[0]}"
echo "Capturing --verbose phase timing for $SAMPLE..." >&2
PHASE_TIMING=""
if [ "$DRY_RUN" -eq 1 ]; then
    PHASE_TIMING="(dry-run: would capture from 'gale --verbose install -g $SAMPLE')"
else
    wipe_iso_home
    PHASE_TIMING=$(gale_iso --verbose install -g "$SAMPLE" 2>&1 | grep '^\[timing\]' || true)
    if [ -z "$PHASE_TIMING" ]; then
        PHASE_TIMING="(no [timing] lines captured — confirm --verbose is wired)"
    fi
fi

# Emit the Markdown block on stdout. Everything above this
# point goes to stderr (status), so a `> baseline.md` redirect
# captures just the report.
cat <<EOF
- gale version: \`$GALE_VERSION\`
- platform: \`$OSNAME/$ARCH\`

### Per-package install (seconds, median of $RUNS)

EOF
if [ "$WITH_BREW" -eq 1 ]; then
    cat <<EOF
| package | gale cold | gale warm | brew reinstall |
|---------|-----------|-----------|----------------|
EOF
    for i in "${!PACKAGES_ARR[@]}"; do
        printf "| %-7s | %9s | %9s | %14s |\n" \
            "${PACKAGES_ARR[$i]}" "${GALE_COLD[$i]}" \
            "${GALE_WARM[$i]}" "${BREW_REINST[$i]}"
    done
else
    cat <<EOF
| package | gale cold | gale warm |
|---------|-----------|-----------|
EOF
    for i in "${!PACKAGES_ARR[@]}"; do
        printf "| %-7s | %9s | %9s |\n" \
            "${PACKAGES_ARR[$i]}" "${GALE_COLD[$i]}" "${GALE_WARM[$i]}"
    done
fi

cat <<EOF

### Multi-package gale sync (seconds, single run, ${#PACKAGES_ARR[@]} packages)

| operation               | seconds |
|-------------------------|---------|
| gale sync (cold)        | $GALE_SYNC |

### Phase timing breakdown ($SAMPLE cold install, --verbose)

\`\`\`
$PHASE_TIMING
\`\`\`
EOF
