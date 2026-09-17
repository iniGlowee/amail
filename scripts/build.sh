#!/usr/bin/env sh
# Build AMail for every supported platform into dist/.
# Usage: scripts/build.sh [version]   (version defaults to git describe or "dev")
set -eu
cd "$(dirname "$0")/.."
VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
mkdir -p dist
build() {
  os="$1"; arch="$2"; ext="$3"
  out="dist/amail-${VERSION}-${os}-${arch}${ext}"
  echo "  $out"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o "$out" ./cmd/amail
}
echo "building amail ${VERSION}"
build windows amd64 .exe
# Same program built for the Windows GUI subsystem: no console window when
# run by a scheduled task. Logs go to files anyway. CLI use stays on amail.exe.
out="dist/amailw-${VERSION}-windows-amd64.exe"; echo "  $out (no console, for background tasks)"
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -H windowsgui -X main.version=${VERSION}" -o "$out" ./cmd/amail
build linux   amd64 ""
build linux   arm64 ""
# Checksums so a downloaded binary can be verified against the release page.
( cd dist && (sha256sum amail-"${VERSION}"-* amailw-"${VERSION}"-* 2>/dev/null || shasum -a 256 amail-"${VERSION}"-* amailw-"${VERSION}"-*) > SHA256SUMS )
echo "  dist/SHA256SUMS"
echo "done"
