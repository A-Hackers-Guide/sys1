# Roadmap

Every item has a matching [issue](https://github.com/A-Hackers-Guide/sys1/issues), labelled with its milestone. Comment there to help or to push something up the list.

Guiding rule for every milestone: **super powerful, super safe, super well made.** Nothing ships without tests and docs.

## ✅ v0.1: Foundation (done)

- sys0 client and sys1 node, single static binaries for 21+ OS/CPU combinations
- Lowest-priority scheduling on Windows, macOS, Linux, BSD, UNIX
- Battery-aware: laptops on battery get no new work
- One-paste joining with pinned TLS 1.3
- Graphical setup wizard; invisible background service on every OS
- Live dashboard: machines, apps, marketplace, join codes, integrations
- Scheduler with scoring, exclusion, timeouts and attempts
- Watchdog: backoff, give up after 5 crashes, move with checkpoint
- Offline machines' work resent within ~15s
- Crash-safe journal for node state and client outboxes
- Marketplace with 11 builtin packages, Ed25519-signed third-party packages, OS-aware filtering
- Cloudflare Tunnel and GitHub Actions runner packages
- Hardened containers; trust levels for owner vs community machines
- GoReleaser, CI on Linux/macOS/Windows, GitHub Pages site

## 🚧 v0.2: Installers everywhere

- Signed + notarized macOS `.pkg` with the setup wizard as its last step
- Windows `.msi` (WiX) with the setup wizard; Microsoft Store (MSIX)
- AppImage, Flatpak, Snap
- FreeBSD port (`.pkg`, `.txz`, `.tzst`)
- Homebrew tap, Scoop bucket, winget, AUR, Chocolatey turned on
- Signed apt and dnf repositories on GitHub Pages
- Container images on GHCR and Docker Hub
- Messages to community-maintained repositories (Nix, Guix, Gentoo, Void, Alpine, Slackware, pkgsrc, MacPorts, Fink…)

## 🗄️ v0.3: Pooled storage

- Replicated block storage across machines (erasure coding, configurable copies)
- App volumes on pooled storage, **so apps move with all their data**
- Backups as a first-class feature: "keep 3 copies of this folder across the pool"
- Encryption at rest for data on community machines

## 🧠 v0.4: AI and batch

- Sharded AI inference across machines (pipeline-parallel, in the style of exo/petals)
- Batch jobs that split into chunks across the pool (rendering, encoding, compiling)
- GPU scheduling by VRAM; NVIDIA, AMD, Apple, Intel

## 🌍 v0.5: Internet-scale pools

- Built-in NAT traversal and relays (WireGuard-based mesh), so machines anywhere join without port forwarding
- Multiple nodes with replicated state (Raft), so there is no single point of failure
- Public community pools with reputation and redundant execution to catch cheaters
- Per-machine egress policy (block SMTP, rate limits, allow-lists)

## 🎮 v0.6: Cloud gaming and desktops

- Game streaming packages (Sunshine/Moonlight) on GPU machines
- Full VMs (QEMU/KVM, Hyper-V, Apple Virtualization) as a package kind
- Remote desktops from the pool

## 📱 v0.7: More devices

- Android and Android TV app (idle + charging; or rooted daemon); F-Droid
- iOS / iPadOS / tvOS sideload builds; jailbreak daemon
- Dashboard companion app for phones

## 🧩 Integrations (ongoing)

- Deploy from a GitHub repository (build on the pool, deploy on push)
- Cloudflare DNS + tunnels managed from the dashboard
- Webhooks and a public node API with scoped tokens
- Prometheus metrics, OpenTelemetry

## Ideas parking lot

Live process checkpointing with CRIU on Linux · WebAssembly package kind (runs anywhere, no container runtime) · energy-aware scheduling (prefer machines on solar / cheap tariffs) · confidential computing (SEV-SNP/TDX) for sensitive work on community machines.
