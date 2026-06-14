#!/bin/bash
# Build the tunneldir binary. Produces a static, dependency-free executable.
#
#   ./build.sh                build ./tunneldir for the host platform
#   ./build.sh all            also cross-compile for common linux arches
set -e

cd "$(dirname "$0")"

# CGO is not needed; disabling it guarantees a fully static binary.
export CGO_ENABLED=0

# Version stamped into the binary. Override with VERSION=... ; otherwise derive
# it from git, falling back to "dev" outside a tagged checkout.
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"

build() {
  local goos="$1" goarch="$2" out="$3"
  echo "building $out ($goos/$goarch) $VERSION"
  GOOS="$goos" GOARCH="$goarch" go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" -o "$out" ./cmd/tunneldir
}

if [ "$1" = "all" ]; then
  mkdir -p dist
  build linux  amd64 dist/tunneldir-linux-amd64
  build linux  arm64 dist/tunneldir-linux-arm64
  build darwin amd64 dist/tunneldir-darwin-amd64
  build darwin arm64 dist/tunneldir-darwin-arm64
  echo "binaries in ./dist"
else
  build "$(go env GOOS)" "$(go env GOARCH)" tunneldir
  echo "built ./tunneldir"
fi
