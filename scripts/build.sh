#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION="${VERSION:-dev}"
mkdir -p dist
build() {
  local os="$1" arch="$2" arm="${3:-}" suffix=""
  [ -n "$arm" ] && suffix="v${arm}"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" GOARM="$arm" go build -trimpath \
    -ldflags "-s -w -X github.com/t0mer/cic/internal/version.Version=${VERSION}" \
    -o "dist/cic_${VERSION}_${os}_${arch}${suffix}" ./cmd/cic
}
build linux amd64
build linux arm64
build linux arm 7
