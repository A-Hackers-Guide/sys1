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

## ✅ v0.2: Installers everywhere (mostly done)

- ✅ macOS `.pkg` (universal), Windows `.msi` (x64, ARM64), AppImage, snap, FreeBSD `.pkg`/`.txz`/`.tzst`
- ✅ Homebrew tap and Scoop bucket that update themselves; container image on GHCR
- ✅ winget manifests submitted to Microsoft
- ✅ Every installer is installed and run on its OS in CI before it ships

Still open. Most need an account, certificate or key only the maintainers can get:

- Signed + notarized macOS `.pkg` (Apple Developer ID, #1), signed `.msi` and Microsoft Store (code-signing certificate, #2)
- AUR and Chocolatey (#5), Docker Hub (#7): accounts and tokens
- Signed apt and dnf repositories (a GPG signing key, #6)
- Messages to community-maintained repositories (#8)
- Flatpak (#3): design work, since its sandbox is built for desktop apps rather than system services

## ✅ v0.3: Pooled storage (done)

- ✅ Encrypted (AES-256-GCM), content-addressed chunks replicated across machines, with configurable copies
- ✅ App volumes on pooled storage, **so apps move with their data**; consistent snapshots every minute and on stop
- ✅ Backups as a first-class feature: "keep 3 copies of this folder", with versions and restore to any of your machines
- ✅ Automatic repair of lost copies, cleanup of unused data, bit-rot detection
- ✅ Machines that hold copies can't read them; each app and backup has its own key

Next for storage: erasure coding (less space than full copies), block-level deduplication within large files, direct machine-to-machine transfers (with v0.5), and catalog recovery after losing the node (#27).

## ✅ v0.4: AI and batch (done)

- ✅ Batch jobs: split into N pieces, retried independently, results collected into one folder
- ✅ Groups: cooperating workers on different machines with automatic peer discovery and restarts
- ✅ One AI model split across machines (llama.cpp RPC), proven in CI on amd64 and arm64
- ✅ GPU inventory for NVIDIA, AMD, Intel and Apple with memory; scheduling by free GPU memory

Next for AI: GPU builds of the llama.cpp image (CUDA, Vulkan), and more group packages.

## ✅ v0.5: Internet-scale pools (done)

- ✅ Join from anywhere: join codes with every address (LAN, VPN, public), client failover, router port mapping (NAT-PMP/UPnP) with carrier-grade NAT detection
- ✅ Clusters: several nodes with Raft-replicated state, automatic failover, clients follow the leader
- ✅ Verified results: pieces checked on several machines, reputation for machines that disagree
- ✅ Internet limits for lent machines: sandbox network, gateway with per-package host lists, the lender's home network always off-limits

Still open: a relay for machines that can't reach the node directly even with a VPN (#14), and recovering pooled storage's catalog on a single node that lost its disk (#27).

## ✅ v0.6: Virtual machines, desktops and gaming (done)

- ✅ Full VMs as a package kind with QEMU (KVM, Apple Hypervisor, Windows Hypervisor Platform), checksum-verified images, disks that move with the VM, clean shutdown, consistent snapshots; a real VM boots in CI
- ✅ VM screens in the browser (noVNC, per-VM password)
- ✅ Ubuntu Server VMs (x86_64, ARM) and a Linux desktop in the browser
- ✅ Game streaming host (Sunshine + Moonlight), experimental

Next: GPU passthrough for VMs, Windows guests, and a CI test of game streaming on a GPU machine.

## 📱 v0.7: More devices

- Android and Android TV app (idle + charging; or rooted daemon); F-Droid
- iOS / iPadOS / tvOS sideload builds and a jailbreak daemon (never the App Store)

## 🧩 Integrations (ongoing)

- Deploy from a GitHub repository (build on the pool, deploy on push)
- Cloudflare DNS + tunnels managed from the dashboard
- Webhooks and a public node API with scoped tokens
- Prometheus metrics, OpenTelemetry

## Ideas parking lot

Live process checkpointing with CRIU on Linux · WebAssembly package kind (runs anywhere, no container runtime) · energy-aware scheduling (prefer machines on solar / cheap tariffs) · confidential computing (SEV-SNP/TDX) for sensitive work on community machines.
