#!/bin/sh
# Installs the SuperCool CLI (supercool) from GitHub Releases.
#
#   curl -fsSL https://supercool.com/install.sh | sh
#   curl -fsSL https://supercool.com/install.sh | sh -s -- --prefix=$HOME/.local
#   curl -fsSL https://supercool.com/install.sh | sh -s -- --version=1.2.3
#
# Checks the archive against the release's checksums.txt before installing.
set -eu

REPO="Famous-Labs/supercool-cli"
PREFIX="/usr/local"
VERSION=""

for arg in "$@"; do
  case "$arg" in
    --prefix=*) PREFIX="${arg#*=}" ;;
    --version=*) VERSION="${arg#*=}"; VERSION="${VERSION#v}" ;;
    -h|--help) sed -n '2,8p' "$0" 2>/dev/null || true; exit 0 ;;
    *) echo "unknown option: $arg" >&2; exit 1 ;;
  esac
done

say() { printf '%s\n' "$*" >&2; }
fail() { say "supercool install: $*"; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "needs $1"; }

need uname; need tar
if command -v curl >/dev/null 2>&1; then
  get() { curl -fsSL "$1" -o "$2"; }
  get_stdout() { curl -fsSL "$1"; }
elif command -v wget >/dev/null 2>&1; then
  get() { wget -qO "$2" "$1"; }
  get_stdout() { wget -qO- "$1"; }
else
  fail "needs curl or wget"
fi

case "$(uname -s)" in
  Darwin) OS=darwin ;;
  Linux) OS=linux ;;
  *) fail "unsupported OS $(uname -s); on Windows use: npm i -g @famous-labs/supercool-cli" ;;
esac
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) fail "unsupported CPU $(uname -m)" ;;
esac

if [ -z "$VERSION" ]; then
  VERSION="$(get_stdout "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p' | head -n1)"
  [ -n "$VERSION" ] || fail "couldn't find the latest release"
fi

ARCHIVE="supercool_${VERSION}_${OS}_${ARCH}.tar.gz"
BASE="https://github.com/$REPO/releases/download/v$VERSION"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

say "Downloading supercool $VERSION for $OS/$ARCH…"
get "$BASE/$ARCHIVE" "$TMP/$ARCHIVE" || fail "download failed: $BASE/$ARCHIVE"
get "$BASE/checksums.txt" "$TMP/checksums.txt" || fail "couldn't download checksums.txt"

WANT="$(grep " $ARCHIVE\$" "$TMP/checksums.txt" | awk '{print $1}')"
[ -n "$WANT" ] || fail "no checksum for $ARCHIVE"
if command -v sha256sum >/dev/null 2>&1; then
  GOT="$(sha256sum "$TMP/$ARCHIVE" | awk '{print $1}')"
else
  GOT="$(shasum -a 256 "$TMP/$ARCHIVE" | awk '{print $1}')"
fi
[ "$WANT" = "$GOT" ] || fail "checksum mismatch; not installing"

tar -xzf "$TMP/$ARCHIVE" -C "$TMP" supercool
BIN="$PREFIX/bin"
if mkdir -p "$BIN" 2>/dev/null && [ -w "$BIN" ]; then
  install -m 0755 "$TMP/supercool" "$BIN/supercool"
else
  say "Installing to $BIN needs sudo (or rerun with --prefix=\$HOME/.local)."
  sudo mkdir -p "$BIN"
  sudo install -m 0755 "$TMP/supercool" "$BIN/supercool"
fi

say "Installed $("$BIN/supercool" version)"
case ":$PATH:" in
  *":$BIN:"*) ;;
  *) say "Add $BIN to your PATH to run supercool." ;;
esac
say "Next: supercool login"
