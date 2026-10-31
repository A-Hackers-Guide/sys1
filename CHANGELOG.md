# Changelog

All notable changes are listed here. Versions follow [semantic versioning](https://semver.org).

## [0.2.0] - 2026-10-31

Installers everywhere.

### Added

- **macOS `.pkg`**: one universal installer for Apple Silicon and Intel.
- **Windows `.msi`** for x64 and ARM64: graphical installer that adds sys0 and sys1 to PATH, adds Start menu entries ("Join a sys1 pool" opens the setup wizard), and removes the background service on uninstall.
- **AppImage** and **snap** for Linux x86-64 and ARM64.
- **FreeBSD** `.pkg`, `.txz` and `.tzst` for amd64 and ARM64, plus a ports-tree Makefile.
- **Container image** `ghcr.io/a-hackers-guide/sys1` for amd64 and arm64.
- **Scoop**: `scoop install a-hackers-guide/sys1`, via a bucket that updates itself from each release.
- Every installer is installed and run on a real machine of its OS before it is published; `checksums.txt` covers every download.
- `sys1 open` prints the dashboard link (useful over SSH and in containers); `sys1 serve --foreground`.
- Homebrew: `brew install a-hackers-guide/tap/sys1` (macOS and Linux), via a tap that updates itself from each release.
- winget manifests in `packaging/winget/`, validated and test-installed with the real winget client in CI; 0.1.0 submitted to microsoft/winget-pkgs.

## [0.1.0] - 2026-10-31

The first release: the foundation.

### Added

- **sys0 client**: lowest-priority background service for Windows, macOS, Linux, BSD, UNIX and Android; graphical setup wizard; `join`, `status`, `install`, `uninstall`, `start`/`stop`/`restart` commands.
- **sys1 node**: client API over TLS 1.3 with certificate pinning; live dashboard (overview, machines, apps, marketplace, add a machine, integrations); built-in client that lends the node's own machine to the pool; runs as a service with `sys1 install`.
- **Scheduler**: scores machines by spare CPU, memory and GPU; skips busy machines and machines on battery; per-machine limits; exclusion after crashes; assignment timeouts; up to 10 placements per task.
- **Reliability**: crash-safe write-ahead journal (fsync + CRC-32C + atomic compaction) for node state and client outboxes; watchdog with exponential backoff that gives up after 5 consecutive crashes and hands the app's checkpoint to the node; offline machines' work resent within ~15s.
- **Marketplace**: 11 builtin packages (Hello, benchmark, static website, Jellyfin, Plex, Audiobookshelf, Ollama, Syncthing, Cloudflare Tunnel, GitHub Actions runner, any container); Ed25519-signed third-party packages; OS- and runtime-aware compatibility; `sys1 pkg keygen|sign|verify`.
- **Security**: owner vs community trust levels enforced by the client; hardened containers; strict spec validation; key-locked, localhost-only dashboard with CSP, CSRF and DNS-rebinding protection.
- **Docs**: getting started, architecture, reliability, security model, protocol, packages, platforms, distribution, roadmap, FAQ.
- **Tooling**: GoReleaser config, CI on Linux/macOS/Windows with cross-compilation for 21 targets, GitHub Pages site.
