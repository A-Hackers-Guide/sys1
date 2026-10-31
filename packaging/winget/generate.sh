#!/bin/sh
# Writes winget manifests for a sys1 release into
# packaging/winget/manifests/a/A-Hackers-Guide/sys1/<version>/, the same
# layout as microsoft/winget-pkgs, from the release's SHA-256 checksums.
#
#   packaging/winget/generate.sh v0.1.0
set -eu
REPO=A-Hackers-Guide/sys1
ID=A-Hackers-Guide.sys1
SCHEMA=1.6.0
tag=${1:?usage: generate.sh <tag>}
v=${tag#v}
sums=$(curl -fsSL "https://github.com/$REPO/releases/download/$tag/checksums.txt")
sha() { printf '%s\n' "$sums" | awk -v f="sys1_${v}_windows_$1.zip" '$2 == f { print toupper($1) }'; }
x64=$(sha amd64); arm=$(sha arm64)
[ -n "$x64" ] && [ -n "$arm" ] || { echo "missing Windows checksums for $tag" >&2; exit 1; }
date=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/tags/$tag" | sed -n 's/.*"published_at": *"\([0-9-]*\)T.*/\1/p' | head -1)
dir="$(dirname "$0")/manifests/a/A-Hackers-Guide/sys1/$v"
mkdir -p "$dir"
url="https://github.com/$REPO/releases/download/$tag/sys1_${v}_windows"

cat > "$dir/$ID.yaml" <<YAML
# yaml-language-server: \$schema=https://aka.ms/winget-manifest.version.$SCHEMA.schema.json
PackageIdentifier: $ID
PackageVersion: $v
DefaultLocale: en-US
ManifestType: version
ManifestVersion: $SCHEMA
YAML

cat > "$dir/$ID.installer.yaml" <<YAML
# yaml-language-server: \$schema=https://aka.ms/winget-manifest.installer.$SCHEMA.schema.json
PackageIdentifier: $ID
PackageVersion: $v
InstallerType: zip
NestedInstallerType: portable
NestedInstallerFiles:
  - RelativeFilePath: sys0.exe
    PortableCommandAlias: sys0
  - RelativeFilePath: sys1.exe
    PortableCommandAlias: sys1
Commands:
  - sys0
  - sys1
ReleaseDate: $date
Installers:
  - Architecture: x64
    InstallerUrl: ${url}_amd64.zip
    InstallerSha256: $x64
  - Architecture: arm64
    InstallerUrl: ${url}_arm64.zip
    InstallerSha256: $arm
ManifestType: installer
ManifestVersion: $SCHEMA
YAML

cat > "$dir/$ID.locale.en-US.yaml" <<YAML
# yaml-language-server: \$schema=https://aka.ms/winget-manifest.defaultLocale.$SCHEMA.schema.json
PackageIdentifier: $ID
PackageVersion: $v
PackageLocale: en-US
Publisher: A-Hackers-Guide
PublisherUrl: https://github.com/A-Hackers-Guide
PublisherSupportUrl: https://github.com/A-Hackers-Guide/sys1/issues
PackageName: sys1
PackageUrl: https://a-hackers-guide.github.io/sys1/
License: Unlicense
LicenseUrl: https://github.com/A-Hackers-Guide/sys1/blob/main/LICENSE
ShortDescription: Pool the spare power of every computer into one cloud.
Description: |-
  sys1 pools the spare CPU, memory, disk and GPU of many computers into one place to run websites, media servers, backups and AI models.
  It installs two commands: sys0, a lowest-priority background client that lends this PC's spare power to a pool, and sys1, the node that runs a pool and its dashboard.
Moniker: sys1
Tags:
  - cluster
  - distributed-computing
  - homelab
  - self-hosted
  - volunteer-computing
ReleaseNotesUrl: https://github.com/$REPO/releases/tag/$tag
Documentations:
  - DocumentLabel: Getting started
    DocumentUrl: https://github.com/A-Hackers-Guide/sys1/blob/main/docs/GETTING-STARTED.md
ManifestType: defaultLocale
ManifestVersion: $SCHEMA
YAML
echo "wrote $dir"
