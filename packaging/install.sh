#!/bin/sh
# sys1 installer for Linux, macOS and the BSDs.
#
#   curl -fsSL https://a-hackers-guide.github.io/sys1/install.sh | sh
#   wget -qO-  https://a-hackers-guide.github.io/sys1/install.sh | sh
#
# Installs sys0 (client) and sys1 (node) into /usr/local/bin (or
# ~/.local/bin without root), after verifying the SHA-256 checksum.
# Read it before you run it; it is short on purpose.
set -eu

REPO="A-Hackers-Guide/sys1"
VERSION="${SYS1_VERSION:-latest}"

say() { printf '  %s\n' "$*"; }
die() { printf '  error: %s\n' "$*" >&2; exit 1; }

fetch() {
  if command -v curl >/dev/null 2>&1; then curl -fsSL "$1" -o "$2"
  elif command -v wget >/dev/null 2>&1; then wget -q "$1" -O "$2"
  else die "need curl or wget"; fi
}

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  linux|darwin|freebsd|openbsd|netbsd|dragonfly) ;;
  sunos) os=solaris ;;
  *) die "unsupported OS: $os (see https://github.com/$REPO/blob/main/docs/PLATFORMS.md)" ;;
esac
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  armv7*|armv6*) arch=armv7 ;;
  i386|i686) arch=386 ;;
  riscv64) arch=riscv64 ;;
  ppc64le) arch=ppc64le ;;
  s390x) arch=s390x ;;
  *) die "unsupported CPU: $arch" ;;
esac

if [ "$VERSION" = latest ]; then
  tmpv=$(mktemp)
  fetch "https://api.github.com/repos/$REPO/releases/latest" "$tmpv" || die "no release found yet"
  VERSION=$(sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' "$tmpv" | head -1)
  rm -f "$tmpv"
  [ -n "$VERSION" ] || die "could not find the latest version"
fi
num=${VERSION#v}
file="sys1_${num}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$VERSION"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM
say "Downloading sys1 $VERSION for $os/$arch…"
fetch "$base/$file" "$tmp/$file" || die "no download for $os/$arch in $VERSION"
fetch "$base/checksums.txt" "$tmp/checksums.txt"

want=$(grep " $file\$" "$tmp/checksums.txt" | cut -d' ' -f1)
if command -v sha256sum >/dev/null 2>&1; then got=$(sha256sum "$tmp/$file" | cut -d' ' -f1)
elif command -v shasum >/dev/null 2>&1; then got=$(shasum -a 256 "$tmp/$file" | cut -d' ' -f1)
else got=$(sha256 -q "$tmp/$file"); fi
[ -n "$want" ] && [ "$want" = "$got" ] || die "checksum mismatch; download is corrupt or tampered with"
say "Checksum verified."

tar -xzf "$tmp/$file" -C "$tmp" sys0 sys1
if [ "$(id -u)" = 0 ]; then dest=/usr/local/bin; else dest="$HOME/.local/bin"; fi
mkdir -p "$dest"
install -m 0755 "$tmp/sys0" "$tmp/sys1" "$dest/" 2>/dev/null || { cp "$tmp/sys0" "$tmp/sys1" "$dest/"; chmod 0755 "$dest/sys0" "$dest/sys1"; }

say "Installed sys0 and sys1 to $dest"
case ":$PATH:" in *":$dest:"*) ;; *) say "Add $dest to your PATH to use them." ;; esac
echo
say "Lend this machine to a pool:  sys0"
say "Run a node here:              sys1"
echo
