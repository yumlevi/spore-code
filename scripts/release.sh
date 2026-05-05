#!/usr/bin/env sh
set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
BIN="${BIN:-spore}"
VERSION="$("$ROOT/scripts/version.sh")"
GO="${GO:-go}"
ZIG="${ZIG:-zig}"
LDFLAGS="-s -w -X main.version=${VERSION}"

if ! command -v "$ZIG" >/dev/null 2>&1; then
  echo "$ZIG not found in PATH. Install zig 0.13+ for CGO cross-compiles." >&2
  exit 1
fi

TARGETS="${TARGETS:-linux/amd64 linux/arm64 windows/amd64 windows/arm64}"
if [ "${INCLUDE_DARWIN:-0}" = "1" ]; then
  TARGETS="$TARGETS darwin/amd64 darwin/arm64"
fi

cd "$ROOT"
rm -rf dist
mkdir -p dist

for target in $TARGETS; do
  GOOS="${target%/*}"
  GOARCH="${target#*/}"
  EXT=""
  EXTFLAGS=""

  case "$target" in
    linux/amd64) ZIG_TARGET="x86_64-linux-musl"; EXTFLAGS="-extldflags '-static'" ;;
    linux/arm64) ZIG_TARGET="aarch64-linux-musl"; EXTFLAGS="-extldflags '-static'" ;;
    windows/amd64) ZIG_TARGET="x86_64-windows-gnu"; EXT=".exe" ;;
    windows/arm64) ZIG_TARGET="aarch64-windows-gnu"; EXT=".exe" ;;
    darwin/amd64) ZIG_TARGET="x86_64-macos-none" ;;
    darwin/arm64) ZIG_TARGET="aarch64-macos-none" ;;
    *) echo "unsupported target: $target" >&2; exit 1 ;;
  esac

  OUT="dist/${BIN}-${GOOS}-${GOARCH}${EXT}"
  echo "-> $OUT (zig cc -target $ZIG_TARGET)"
  GOOS="$GOOS" GOARCH="$GOARCH" CGO_ENABLED=1 \
    CC="$ZIG cc -target $ZIG_TARGET" \
    CXX="$ZIG c++ -target $ZIG_TARGET" \
    "$GO" build -ldflags "$LDFLAGS $EXTFLAGS" -o "$OUT" ./cmd/spore
done

if [ "${SPORE_CODE_STAGE_UPDATE:-1}" != "0" ]; then
  host_goos="$("$GO" env GOOS)"
  host_goarch="$("$GO" env GOARCH)"
  host_ext=""
  if [ "$host_goos" = "windows" ]; then
    host_ext=".exe"
  fi
  host_asset="dist/${BIN}-${host_goos}-${host_goarch}${host_ext}"
  update_dir="${SPORE_CODE_UPDATE_DIR:-${HOME:-}/.spore-code/updates}"
  if [ -f "$host_asset" ] && [ "$update_dir" != "/.spore-code/updates" ]; then
    mkdir -p "$update_dir"
    cp "$host_asset" "$update_dir/$(basename "$host_asset")"
    echo "staged local update: $update_dir/$(basename "$host_asset")"
  fi
fi

echo "done - $(find dist -maxdepth 1 -type f | wc -l) binaries in dist/"
