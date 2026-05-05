#!/usr/bin/env sh
set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
GO="${GO:-go}"
ZIG="${ZIG:-zig}"

cd "$ROOT"
if command -v cc >/dev/null 2>&1 || command -v gcc >/dev/null 2>&1; then
  CGO_ENABLED=1 "$GO" test ./...
elif command -v "$ZIG" >/dev/null 2>&1; then
  CGO_ENABLED=1 CC="$ZIG cc" CXX="$ZIG c++" "$GO" test ./...
else
  echo "No C compiler found. Install gcc/clang or zig 0.13+ for CGO tree-sitter tests." >&2
  exit 1
fi
