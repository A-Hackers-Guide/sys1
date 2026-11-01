#!/bin/sh
# Embeds Windows version information (product, publisher, version, a
# manifest) into sys0.exe and sys1.exe. Unsigned programs without it look
# like malware to Windows Security's machine-learning scan, which once
# quarantined sys1.exe as "Trojan:Win32/Bearfoos.A!ml".
#
#   packaging/windows/winres.sh 0.9.1
#
# Writes cmd/sys{0,1}/rsrc_windows_{amd64,arm64}.syso, which go build picks
# up on its own for Windows targets.
set -eu
v=${1:?version}
v=${v%%-*} # Windows versions are numbers only
for app in sys0 sys1; do
  case $app in
    sys0) desc="sys0 - lends this computer's spare capacity to a sys1 pool" ;;
    sys1) desc="sys1 - the node and dashboard for a pool of computers" ;;
  esac
  go run github.com/tc-hib/go-winres@v0.3.3 simply \
    --arch amd64,arm64 --out "cmd/$app/rsrc" --manifest cli \
    --product-name sys1 --product-version "$v.0" --file-version "$v.0" \
    --file-description "$desc" --original-filename "$app.exe" \
    --copyright "A-Hackers-Guide - public domain (Unlicense)"
done
