#!/bin/sh
# tunneldir installer.
#
#   curl -fsSL https://raw.githubusercontent.com/tallbiscuits/tunneldir/main/install.sh | sh
#
# Downloads the right release binary for this OS/arch, verifies its SHA256, and
# installs it to ~/.local/bin (no sudo needed). Pin a version with VERSION=vX.Y.Z.
set -eu

REPO="tallbiscuits/tunneldir"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${VERSION:-latest}"

err() { echo "error: $*" >&2; exit 1; }

command -v curl >/dev/null 2>&1 || err "curl is required"

# Detect platform and map to the asset naming used by build.sh.
os="$(uname -s)"
arch="$(uname -m)"
case "$os" in
  Linux)  os="linux" ;;
  Darwin) os="darwin" ;;
  *) err "unsupported OS: $os" ;;
esac
case "$arch" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) err "unsupported architecture: $arch" ;;
esac
asset="tunneldir-${os}-${arch}"

# Resolve the release tag.
if [ "$VERSION" = "latest" ]; then
  VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name":' | head -n1 | cut -d'"' -f4)"
  [ -n "$VERSION" ] || err "could not determine latest version"
fi
echo "installing tunneldir ${VERSION} (${os}/${arch})"

base="https://github.com/${REPO}/releases/download/${VERSION}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

curl -fsSL "${base}/${asset}" -o "${tmp}/${asset}" || err "download failed for ${asset}"

# Verify the checksum when SHA256SUMS is published with the release.
if curl -fsSL "${base}/SHA256SUMS" -o "${tmp}/SHA256SUMS" 2>/dev/null; then
  expected="$(grep " ${asset}\$" "${tmp}/SHA256SUMS" | awk '{print $1}')"
  if [ -n "$expected" ]; then
    if command -v sha256sum >/dev/null 2>&1; then
      actual="$(sha256sum "${tmp}/${asset}" | awk '{print $1}')"
    else
      actual="$(shasum -a 256 "${tmp}/${asset}" | awk '{print $1}')"
    fi
    [ "$actual" = "$expected" ] || err "checksum mismatch for ${asset}"
    echo "checksum verified"
  fi
else
  echo "warning: no SHA256SUMS published; skipping checksum verification" >&2
fi

mkdir -p "$INSTALL_DIR"
install -m 0755 "${tmp}/${asset}" "${INSTALL_DIR}/tunneldir"
echo "installed ${INSTALL_DIR}/tunneldir"

# Nudge the user if the install dir is not on PATH.
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) echo "note: $INSTALL_DIR is not on your PATH; add it with:" >&2
     echo "  export PATH=\"$INSTALL_DIR:\$PATH\"" >&2 ;;
esac

echo "run 'tunneldir --version' to confirm."
