#!/usr/bin/env sh
set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"

if [ "${VERSION:-}" != "" ]; then
  printf '%s\n' "$VERSION"
  exit 0
fi

if command -v git >/dev/null 2>&1; then
  if v="$(git -C "$ROOT" describe --tags --dirty --always 2>/dev/null)" && [ "$v" != "" ]; then
    printf '%s\n' "$v"
    exit 0
  fi
fi

sed -n 's/^var version = "\(.*\)"/\1/p' "$ROOT/cmd/spore/main.go" | head -1
