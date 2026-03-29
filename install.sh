#!/bin/sh
# Install gale — https://github.com/kelp/gale
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/kelp/gale/main/scripts/install.sh | sh
#
# Options (via environment variables):
#   GALE_VERSION  — version to install (default: latest)
#   GALE_DIR      — install directory (default: ~/.gale)

set -eu

GALE_VERSION="${GALE_VERSION:-latest}"
GALE_DIR="${GALE_DIR:-$HOME/.gale}"
REPO="kelp/gale"

main() {
    detect_platform
    resolve_version

    echo "Installing gale ${VERSION} (${OS}-${ARCH})..."

    BINARY="gale-v${VERSION}-${OS}-${ARCH}"
    URL="https://github.com/${REPO}/releases/download/v${VERSION}/${BINARY}.tar.gz"

    # Download and extract.
    TMPDIR=$(mktemp -d)
    trap 'rm -rf "$TMPDIR"' EXIT

    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$URL" -o "$TMPDIR/gale.tar.gz"
    elif command -v wget >/dev/null 2>&1; then
        wget -qO "$TMPDIR/gale.tar.gz" "$URL"
    else
        echo "Error: curl or wget required" >&2
        exit 1
    fi

    tar xzf "$TMPDIR/gale.tar.gz" -C "$TMPDIR"

    # Install binary.
    mkdir -p "$GALE_DIR/bin"
    mv "$TMPDIR/$BINARY" "$GALE_DIR/bin/gale"
    chmod +x "$GALE_DIR/bin/gale"

    echo "Installed gale to $GALE_DIR/bin/gale"

    # Check if gale is on PATH.
    if ! command -v gale >/dev/null 2>&1; then
        echo ""
        echo "Add gale to your PATH:"
        echo ""
        echo "  export PATH=\"$GALE_DIR/bin:\$PATH\""
        echo ""
        echo "Then add ~/.gale/current/bin for managed packages:"
        echo ""
        echo "  export PATH=\"$GALE_DIR/current/bin:\$PATH\""
    else
        echo ""
        echo "Run 'gale --version' to verify."
    fi
}

detect_platform() {
    OS=$(uname -s | tr '[:upper:]' '[:lower:]')
    ARCH=$(uname -m)

    case "$ARCH" in
        x86_64)  ARCH="amd64" ;;
        aarch64) ARCH="arm64" ;;
        arm64)   ARCH="arm64" ;;
        *)
            echo "Error: unsupported architecture: $ARCH" >&2
            exit 1
            ;;
    esac

    case "$OS" in
        linux)  ;;
        darwin) ;;
        *)
            echo "Error: unsupported OS: $OS" >&2
            exit 1
            ;;
    esac
}

resolve_version() {
    if [ "$GALE_VERSION" = "latest" ]; then
        if command -v curl >/dev/null 2>&1; then
            VERSION=$(curl -fsSL \
                "https://api.github.com/repos/${REPO}/releases/latest" |
                grep '"tag_name"' | head -1 |
                sed 's/.*"v\([^"]*\)".*/\1/')
        elif command -v wget >/dev/null 2>&1; then
            VERSION=$(wget -qO- \
                "https://api.github.com/repos/${REPO}/releases/latest" |
                grep '"tag_name"' | head -1 |
                sed 's/.*"v\([^"]*\)".*/\1/')
        fi

        if [ -z "$VERSION" ]; then
            echo "Error: could not determine latest version" >&2
            exit 1
        fi
    else
        VERSION="$GALE_VERSION"
    fi
}

main
