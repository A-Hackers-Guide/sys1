# Distribution

**Goal:** wherever you get software, you can get sys1, and it's the same two apps everywhere: `sys0` (client) and `sys1` (node).

Releases are built by [GoReleaser](https://goreleaser.com) in GitHub Actions when a version tag (`v*`) is pushed. One config ([`.goreleaser.yaml`](../.goreleaser.yaml)) produces every archive and package below, with SHA-256 checksums.

## Status

**Legend:** ✅ automated on every release · 🛠️ config in repo, needs a one-time account or repo setup · 🤝 community-maintained (we provide everything a maintainer needs) · 📋 planned

### Direct downloads

| Format | Platforms | Status |
|---|---|---|
| `.tar.gz` / `.zip` archives | every OS/CPU in [Platforms](PLATFORMS.md) | ✅ |
| `curl \| sh` / `wget` install script | Linux, macOS, BSD | ✅ [`install.sh`](../packaging/install.sh) |
| PowerShell install script | Windows | ✅ [`install.ps1`](../packaging/install.ps1) |
| `.deb` | Debian, Ubuntu, Mint, Pop!_OS, Raspberry Pi OS | ✅ |
| `.rpm` | Fedora, RHEL, Rocky, Alma, openSUSE | ✅ |
| `.apk` | Alpine | ✅ |
| `.pkg.tar.zst` | Arch, Manjaro, EndeavourOS | ✅ |
| `.ipk` | OpenWrt | ✅ |
| `.pkg` (macOS installer) | macOS | 📋 needs Apple Developer ID for signing + notarization |
| `.msi` | Windows | 📋 WiX config planned; needs code-signing certificate |
| AppImage | Linux | 📋 |
| Flatpak (`.flatpakref`) | Linux | 📋 |
| Snap | Linux | 📋 |
| FreeBSD `.pkg` / `.txz` / `.tzst` | FreeBSD | 📋 via a FreeBSD port (see below) |
| Container image | anything with Docker/Podman | 🛠️ `ghcr.io/a-hackers-guide/sys1` |

### Package managers

| Manager | Platforms | Status | How |
|---|---|---|---|
| Homebrew | macOS, Linux | ✅ | `brew install a-hackers-guide/tap/sys1`. The [tap](https://github.com/A-Hackers-Guide/homebrew-tap) rebuilds its formula from each release's checksums every hour, then installs and tests it on macOS and Linux |
| Scoop | Windows | 🛠️ | bucket repo, GoReleaser pushes manifests |
| winget | Windows | 🛠️ | manifests in [`packaging/winget/`](../packaging/winget/), validated and installed with the real winget client on every change; submission to `microsoft/winget-pkgs` pending |
| Chocolatey | Windows | 📋 | |
| Microsoft Store | Windows | 📋 | MSIX package |
| AUR (`sys1-bin`) | Arch | 🛠️ | GoReleaser pushes PKGBUILD |
| npm (`sys1`) | anywhere with Node | 📋 | thin wrapper that downloads the right binary |
| Docker Hub / GHCR | containers | 🛠️ | |
| Nix / nixpkgs | NixOS, any Linux, macOS | 🤝 | nixpkgs PR by a Nix maintainer; `flake.nix` planned in repo |
| Guix | Guix System | 🤝 | |
| apt repository | Debian family | 📋 | signed repo on GitHub Pages |
| dnf / yum / zypper repository | Fedora/RHEL/SUSE family | 📋 | signed repo on GitHub Pages |
| pacman (official) / pamac | Arch, Manjaro | 🤝 | AUR first |
| apk-tools (Alpine aports) | Alpine | 🤝 | |
| XBPS (void-packages) | Void | 🤝 | |
| Portage (ebuild) | Gentoo | 🤝 | overlay planned |
| Entropy | Sabayon (legacy) | 🤝 | |
| slackpkg / slapt-get / SlackBuilds.org | Slackware | 🤝 | |
| netpkg | Zenwalk | 🤝 | |
| FreeBSD ports / pkg | FreeBSD | 🤝 | `sysutils/sys1` port |
| pkgsrc / Joyent (SmartOS) | NetBSD, illumos, macOS, many more | 🤝 | |
| MacPorts | macOS | 🤝 | |
| Fink | macOS | 🤝 | |
| F-Droid | Android | 📋 | after the Android app exists |
| Flathub | Linux | 📋 | |
| Snap Store | Linux | 📋 | |
| Cygwin | Windows | 🤝 | |
| SMP/E | IBM z/OS | 📋 | Go supports `zos/s390x` from Go 1.21 on IBM's toolchain; long-term |
| Maven Central, NuGet, RubyGems | language ecosystems | 📋 | thin launcher packages, for people who live in those tools |
| Apache | – | n/a | Apache is a web server, not a package manager; the apt repo and downloads can be served from any web server, including Apache |

## Community-maintained repositories 🤝

Many distributions only accept packages from their own maintainers. For each, we keep a ready-to-send message in [`packaging/community/`](../packaging/community/) that explains sys1, points to the release artifacts and checksums, and offers help. When a release ships we open an issue labelled `distribution` for each repo we haven't reached yet.

If you maintain packages for any of these, thank you! Open an issue and we'll support you.

## Release checklist

1. All CI checks green on `main`.
2. Update `CHANGELOG.md`.
3. `git tag -a vX.Y.Z -m "sys1 vX.Y.Z" && git push origin vX.Y.Z`
4. GitHub Actions runs GoReleaser and publishes the release.
5. Homebrew updates itself within the hour (or run the tap's *Update formula* workflow).
6. `packaging/winget/generate.sh vX.Y.Z`, commit; the winget workflow validates and test-installs it; then submit the new version folder to `microsoft/winget-pkgs`.
7. Check the release page; announce in Discussions.
