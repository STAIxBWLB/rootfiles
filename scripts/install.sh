#!/bin/bash
# rootfiles installer
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/STAIxBWLB/rootfiles/main/scripts/install.sh | sudo bash
#   curl -fsSL ... | sudo bash -s -- --version v0.1.0
#   curl -fsSL ... | sudo bash -s -- --channel dev
set -euo pipefail

REPO="STAIxBWLB/rootfiles"
INSTALL_DIR="/usr/local/bin"
BINARY="rootfiles"
VERSION=""
CHANNEL="stable"  # stable or dev

# Parse arguments
while [[ $# -gt 0 ]]; do
    case "$1" in
        --version|-v)
            VERSION="$2"
            shift 2
            ;;
        --channel|-c)
            CHANNEL="$2"
            shift 2
            ;;
        --help|-h)
            echo "Usage: install.sh [--version v0.1.0] [--channel stable|dev]"
            echo ""
            echo "Options:"
            echo "  --version, -v    Install a specific version (e.g., v0.1.0)"
            echo "  --channel, -c    Release channel: stable (default) or dev"
            echo ""
            echo "Channels:"
            echo "  stable    Latest tagged release (recommended)"
            echo "  dev       Latest commit on main (build from source)"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

# Detect architecture
ARCH=$(uname -m)
case "$ARCH" in
    x86_64)  ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *)
        echo "Error: unsupported architecture: $ARCH"
        exit 1
        ;;
esac

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
if [ "$OS" != "linux" ] && [ "$OS" != "darwin" ]; then
    echo "Error: rootfiles only supports Linux and macOS operator binaries (got: $OS)"
    exit 1
fi

echo "rootfiles installer"
echo "  OS:   $OS"
echo "  Arch: $ARCH"

# Resolve regular paths and symlink chains using portable readlink plus the
# physical parent directory. This works on macOS/BSD where readlink -f is not
# available, and also handles absolute and relative compatibility links.
canonical_path() {
    local path="$1" target dir base hops=0
    while [ -L "$path" ]; do
        hops=$((hops + 1))
        if [ "$hops" -gt 40 ]; then
            return 1
        fi
        target=$(readlink "$path") || return 1
        case "$target" in
            /*) path="$target" ;;
            *) path="$(dirname "$path")/$target" ;;
        esac
    done
    dir=$(cd -P -- "$(dirname "$path")" 2>/dev/null && pwd -P) || return 1
    base=$(basename "$path")
    printf '%s/%s\n' "$dir" "$base"
}

# Never replace a command or symlink the installer does not own. Comparing
# resolved targets also accepts an already-correct relative symlink.
ensure_compat_symlink() {
    local target="$1" link="$2" resolved_target resolved_link
    resolved_target=$(canonical_path "$target" 2>/dev/null || true)
    if [ -L "$link" ]; then
        resolved_link=$(canonical_path "$link" 2>/dev/null || true)
        if [ -n "$resolved_target" ] && [ "$resolved_link" = "$resolved_target" ]; then
            echo "Verified symlink: $link -> $target"
            return 0
        fi
        echo "Error: refusing to replace unrelated symlink: $link -> $(readlink "$link" 2>/dev/null || true)" >&2
        return 1
    fi
    if [ -e "$link" ]; then
        if [ "$link" -ef "$target" ]; then
            echo "Verified command path: $link"
            return 0
        fi
        echo "Error: refusing to replace unrelated file: $link" >&2
        return 1
    fi
    ln -s "$target" "$link"
    resolved_link=$(canonical_path "$link" 2>/dev/null || true)
    if [ -z "$resolved_target" ] || [ "$resolved_link" != "$resolved_target" ]; then
        echo "Error: symlink verification failed: $link -> $target" >&2
        return 1
    fi
    echo "Created symlink: $link -> $target"
}

# On Linux, sudo's secure_path may omit /usr/local/bin (notably on Rocky).
# Verify command resolution as the invoking account; if it does not find this
# installation, add only a previously absent /usr/bin/rootfiles link.
verify_linux_sudo_path() {
    [ "$OS" = "linux" ] || return 0
    command -v sudo >/dev/null 2>&1 || return 0

    local verify_user resolved expected actual
    verify_user="${SUDO_USER:-root}"
    if ! sudo -n -u "$verify_user" true >/dev/null 2>&1; then
        echo "Error: cannot verify sudo command resolution non-interactively for $verify_user" >&2
        return 1
    fi

    expected=$("$INSTALL_DIR/$BINARY" --version)
    resolved=$(sudo -n -u "$verify_user" sh -c 'command -v rootfiles' 2>/dev/null || true)
    if [ -n "$resolved" ] && [ "$(canonical_path "$resolved" 2>/dev/null || true)" = "$(canonical_path "$INSTALL_DIR/$BINARY")" ]; then
        actual=$(sudo -n -u "$verify_user" rootfiles --version)
        if [ "$actual" != "$expected" ]; then
            echo "Error: sudo rootfiles version does not match the installed binary" >&2
            return 1
        fi
        echo "Verified privileged command path: sudo rootfiles"
        return 0
    fi

    ensure_compat_symlink "$INSTALL_DIR/$BINARY" /usr/bin/rootfiles || return 1
    resolved=$(sudo -n -u "$verify_user" sh -c 'command -v rootfiles' 2>/dev/null || true)
    if [ -z "$resolved" ] || [ "$(canonical_path "$resolved" 2>/dev/null || true)" != "$(canonical_path "$INSTALL_DIR/$BINARY")" ]; then
        echo "Error: sudo secure_path still cannot resolve the installed rootfiles binary" >&2
        return 1
    fi
    actual=$(sudo -n -u "$verify_user" rootfiles --version)
    if [ "$actual" != "$expected" ]; then
        echo "Error: sudo rootfiles version does not match the installed binary" >&2
        return 1
    fi
    echo "Verified privileged command path: sudo rootfiles"
}

finish_install() {
    if [ "$OS" = "linux" ]; then
        ensure_compat_symlink "$INSTALL_DIR/$BINARY" "$INSTALL_DIR/root"
        verify_linux_sudo_path
        echo "Symlinked: $INSTALL_DIR/root -> $BINARY"
    fi
    echo "Installed: $INSTALL_DIR/$BINARY"
    "$INSTALL_DIR/$BINARY" --version
}

# --- Dev channel: build from source ---
if [ "$CHANNEL" = "dev" ]; then
    echo "  Channel: dev (building from source)"

    # Check Go is installed
    if ! command -v go &>/dev/null; then
        echo "Error: Go is required for dev channel. Install from https://go.dev/dl/"
        exit 1
    fi

    TMPDIR=$(mktemp -d)
    trap "rm -rf $TMPDIR" EXIT

    echo "Cloning repository..."
    git clone --depth 1 "https://github.com/${REPO}.git" "$TMPDIR/rootfiles"

    echo "Building..."
    cd "$TMPDIR/rootfiles"
    COMMIT=$(git rev-parse --short HEAD)
    go build -ldflags "-s -w -X main.version=dev-${COMMIT} -X main.commit=${COMMIT}" \
        -o "$INSTALL_DIR/$BINARY" ./cmd/rootfiles/

    finish_install
    echo "Channel: dev (${COMMIT})"
    exit 0
fi

# --- Stable channel: download release binary ---
echo "  Channel: stable"

# Resolve version
if [ -z "$VERSION" ]; then
    echo "Fetching latest release..."
    VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
        | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')

    if [ -z "$VERSION" ]; then
        echo "Error: could not determine latest version."
        echo "Try: install.sh --channel dev"
        exit 1
    fi
fi

echo "  Version: $VERSION"

# Strip leading 'v' for archive name
VERSION_NUM="${VERSION#v}"
ARCHIVE="rootfiles_${VERSION_NUM}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${ARCHIVE}"
CHECKSUM_URL="https://github.com/${REPO}/releases/download/${VERSION}/checksums.txt"

# Download
TMPDIR=$(mktemp -d)
trap "rm -rf $TMPDIR" EXIT

echo "Downloading ${URL}..."
if ! curl -fsSL -o "$TMPDIR/$ARCHIVE" "$URL"; then
    echo "Error: download failed. Check that version $VERSION exists."
    echo "Available releases: https://github.com/${REPO}/releases"
    exit 1
fi

# Verify checksum
echo "Verifying checksum..."
curl -fsSL -o "$TMPDIR/checksums.txt" "$CHECKSUM_URL"
cd "$TMPDIR"
if command -v sha256sum &>/dev/null; then
    grep "$ARCHIVE" checksums.txt | sha256sum -c --quiet
elif command -v shasum &>/dev/null; then
    grep "$ARCHIVE" checksums.txt | shasum -a 256 -c --quiet
else
    echo "Warning: no checksum tool found, skipping verification"
fi

# Extract and install
echo "Installing to $INSTALL_DIR/$BINARY..."
tar xzf "$ARCHIVE"
install -m 755 "$BINARY" "$INSTALL_DIR/$BINARY"

finish_install

echo ""
echo "Next steps:"
if [ "$OS" = "darwin" ]; then
    echo "  rootfiles --help                 # operator-side commands"
    echo "  Use sudo rootfiles on managed Linux hosts where required"
else
    echo "  sudo rootfiles apply              # or: sudo root apply"
    echo "  sudo rootfiles apply --profile dgx --yes"
fi
