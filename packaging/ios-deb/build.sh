#!/bin/sh
# Builds the rootless jailbreak package: build.sh <version> <sys0 binary> <out.deb>
set -eu
v=$1 bin=$2 out=$3
here=$(cd "$(dirname "$0")" && pwd)
root=$(mktemp -d); chmod 755 "$root"
mkdir -p "$root/DEBIAN" "$root/var/jb/usr/bin" "$root/var/jb/usr/libexec" "$root/var/jb/Library/LaunchDaemons"
sed "s/@VERSION@/$v/" "$here/control.in" > "$root/DEBIAN/control"
install -m 755 "$here/postinst" "$here/prerm" "$root/DEBIAN/"
install -m 755 "$bin" "$root/var/jb/usr/bin/sys0"
ldid -S "$root/var/jb/usr/bin/sys0" 2>/dev/null || codesign -f -s - "$root/var/jb/usr/bin/sys0"
install -m 755 "$here/sys0-daemon" "$root/var/jb/usr/libexec/sys0-daemon"
install -m 644 "$here/io.github.ahackersguide.sys0.plist" "$root/var/jb/Library/LaunchDaemons/"
dpkg-deb --root-owner-group -Zxz -b "$root" "$out"
rm -rf "$root"
