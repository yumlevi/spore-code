#!/usr/bin/env sh
set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
BIN="${BIN:-spore}"
VERSION="$("$ROOT/scripts/version.sh")"
GO="${GO:-go}"
ZIG="${ZIG:-zig}"
LDFLAGS="-s -w -X main.version=${VERSION}"

if command -v cc >/dev/null 2>&1 || command -v gcc >/dev/null 2>&1; then
  CC_CMD="${CC:-}"
  CXX_CMD="${CXX:-}"
elif command -v "$ZIG" >/dev/null 2>&1; then
  CC_CMD="${CC:-$ZIG cc}"
  CXX_CMD="${CXX:-$ZIG c++}"
else
  echo "No C compiler found. Install gcc/clang or zig 0.13+ for CGO tree-sitter builds." >&2
  exit 1
fi

cd "$ROOT"
if [ -n "$CC_CMD" ]; then
  CGO_ENABLED=1 CC="$CC_CMD" CXX="$CXX_CMD" "$GO" build -ldflags "$LDFLAGS" -o "$BIN" ./cmd/spore
else
  CGO_ENABLED=1 "$GO" build -ldflags "$LDFLAGS" -o "$BIN" ./cmd/spore
fi

if [ "${SPORE_CODE_STAGE_UPDATE:-1}" != "0" ]; then
  goos="$("$GO" env GOOS)"
  goarch="$("$GO" env GOARCH)"
  ext=""
  if [ "$goos" = "windows" ]; then
    ext=".exe"
  fi
  update_dir="${SPORE_CODE_UPDATE_DIR:-${HOME:-}/.spore-code/updates}"
  if [ "$update_dir" != "/.spore-code/updates" ]; then
    mkdir -p "$update_dir"
    cp "$BIN" "$update_dir/spore-$goos-$goarch$ext"
    echo "staged local update: $update_dir/spore-$goos-$goarch$ext"
  fi
fi
