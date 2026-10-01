#!/bin/sh
# Builds the desktop app: the same server with the UI in a native window (WebView2 / WKWebView / WebKitGTK).
#
#   scripts/build-desktop.sh [output]        needs a C/C++ compiler (cgo); on Linux also libgtk-3-dev and
#                                            libwebkit2gtk-4.1-dev (or -4.0-dev); on Windows MinGW-w64 (gcc, g++)
set -eu
cd "$(dirname "$0")/.."
VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
LDFLAGS="-s -w -X github.com/demogest/medialib/internal/version.Version=$VERSION"
OUT=${1:-dist/medialib-desktop}
mkdir -p "$(dirname "$OUT")"
case "$(go env GOOS)" in
  windows) LDFLAGS="$LDFLAGS -H windowsgui"; case "$OUT" in *.exe) ;; *) OUT="$OUT.exe" ;; esac ;;
  linux)
    # The web view library asks pkg-config for webkit2gtk-4.0, which newer distributions no longer ship; the headers
    # it needs are the same in 4.1. Point it there with a throwaway .pc file.
    if ! pkg-config --exists webkit2gtk-4.0 2>/dev/null && pkg-config --exists webkit2gtk-4.1 2>/dev/null; then
      shim=$(mktemp -d)
      printf 'Name: webkit2gtk-4.0\nDescription: forwards to 4.1\nVersion: %s\nRequires: webkit2gtk-4.1\n' "$(pkg-config --modversion webkit2gtk-4.1)" > "$shim/webkit2gtk-4.0.pc"
      PKG_CONFIG_PATH="$shim${PKG_CONFIG_PATH:+:$PKG_CONFIG_PATH}"
      export PKG_CONFIG_PATH
    fi ;;
esac
CGO_ENABLED=1 go build -tags desktop -trimpath -ldflags "$LDFLAGS" -o "$OUT" ./cmd/medialib
echo "built $OUT"
