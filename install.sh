#!/usr/bin/env sh
# Spore Code — one-liner installer for Linux & macOS.
#
#   curl -fsSL https://raw.githubusercontent.com/yumlevi/spore-code/main/install.sh | sh
#
# Optional overrides (pass before the pipe):
#   SPORE_CODE_VERSION=v1.0.0    pin a specific release tag
#   SPORE_CODE_DIR=/usr/local/bin install to a different directory
#
# Re-running upgrades in place. Same script handles a fresh install
# and an upgrade — no extra `spore upgrade` command needed.

set -eu

REPO="yumlevi/spore-code"
VERSION="${SPORE_CODE_VERSION:-latest}"
BIN="spore"

# ── pretty output (best-effort; falls back to plain text without TTY) ──
if [ -t 1 ]; then
  C_BOLD="$(printf '\033[1m')"
  C_DIM="$(printf '\033[2m')"
  C_RED="$(printf '\033[31m')"
  C_GREEN="$(printf '\033[32m')"
  C_BLUE="$(printf '\033[34m')"
  C_RESET="$(printf '\033[0m')"
else
  C_BOLD="" C_DIM="" C_RED="" C_GREEN="" C_BLUE="" C_RESET=""
fi
say()  { printf "%s%s%s\n" "$C_BLUE" "→ $*" "$C_RESET"; }
ok()   { printf "%s%s%s\n" "$C_GREEN" "✓ $*" "$C_RESET"; }
warn() { printf "%s%s%s\n" "$C_DIM"   "  $*" "$C_RESET"; }
die()  { printf "%s%s%s\n" "$C_RED"   "✗ $*" "$C_RESET" >&2; exit 1; }

# ── platform detection ──
os="$(uname -s 2>/dev/null | tr '[:upper:]' '[:lower:]')"
case "$os" in
  linux)   ;;
  darwin)  ;;
  msys*|mingw*|cygwin*)
    die "Detected Git-Bash / WSL — use the PowerShell installer instead:
     irm https://raw.githubusercontent.com/yumlevi/spore-code/main/install.ps1 | iex" ;;
  *) die "Unsupported OS: $os" ;;
esac

raw_arch="$(uname -m 2>/dev/null)"
case "$raw_arch" in
  x86_64|amd64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) die "Unsupported architecture: $raw_arch" ;;
esac

# ── pick a download tool ──
if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL "$1" -o "$2"; }
  fetch_stdout() { curl -fsSL "$1"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -q -O "$2" "$1"; }
  fetch_stdout() { wget -q -O - "$1"; }
else
  die "Need curl or wget to download. Install one and retry."
fi

# ── resolve version → tag (the GitHub `latest` redirect handles this for
#    the asset URL, but we also want to surface the version we picked) ──
if [ "$VERSION" = "latest" ]; then
  resolved_tag="$(fetch_stdout "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null \
    | sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
  if [ -n "$resolved_tag" ]; then
    VERSION="$resolved_tag"
  fi
fi

asset_url="https://github.com/$REPO/releases/download/$VERSION/$BIN-$os-$arch"
[ "$VERSION" = "latest" ] && asset_url="https://github.com/$REPO/releases/latest/download/$BIN-$os-$arch"

# ── pick install dir ──
dest_dir="${SPORE_CODE_DIR:-$HOME/.local/bin}"
mkdir -p "$dest_dir" || die "Cannot create $dest_dir"
dest="$dest_dir/$BIN"

# ── download to a temp file, verify, then atomic rename so a half-
#    written binary can never replace a working one. ──
tmp="$(mktemp "${TMPDIR:-/tmp}/$BIN.XXXXXX")" || die "mktemp failed"
trap 'rm -f "$tmp"' EXIT INT TERM

say "Downloading spore ${C_BOLD}$VERSION${C_RESET} for $os/$arch"
warn "$asset_url"
if ! fetch "$asset_url" "$tmp"; then
  die "Download failed — check the URL above and your network."
fi

# Sanity-check it actually looks like a binary, not an HTML 404 page.
head_bytes="$(head -c 4 "$tmp" 2>/dev/null | od -An -c 2>/dev/null | tr -d ' \n')"
case "$head_bytes" in
  "177ELF"*)         ;; # Linux ELF
  "317372372376"*|"376372372317"*) ;; # Mach-O magic, both endianness
  *)
    case "$(file -b "$tmp" 2>/dev/null || echo unknown)" in
      *ELF*|*Mach-O*) ;;
      *) die "Downloaded file isn't a valid binary (asset missing for this platform?)" ;;
    esac
    ;;
esac

chmod +x "$tmp"

# Move into place. If $dest exists and is the running binary on macOS
# the rename is fine. On Linux too — rename(2) atomically swaps inode.
if [ -e "$dest" ]; then
  say "Replacing existing $dest"
fi
mv -f "$tmp" "$dest" || die "Could not write $dest (try: SPORE_CODE_DIR=/usr/local/bin sudo …)"
trap - EXIT

ok "Installed to $dest"

# ── PATH advice ──
case ":$PATH:" in
  *":$dest_dir:"*) ;;
  *)
    warn "$dest_dir is not in your PATH."
    case "${SHELL##*/}" in
      bash) rc="~/.bashrc" ;;
      zsh)  rc="~/.zshrc"  ;;
      fish) rc="~/.config/fish/config.fish" ;;
      *)    rc="your shell rc" ;;
    esac
    warn "Add this to $rc and reopen the shell:"
    warn "  export PATH=\"$dest_dir:\$PATH\""
    ;;
esac

printf "\n%sRun %sspore%s to start. First launch walks you through setup.%s\n" \
  "$C_DIM" "$C_BOLD" "$C_RESET$C_DIM" "$C_RESET"
