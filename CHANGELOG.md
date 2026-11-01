# Changelog

All notable changes are listed here. Versions follow [semantic versioning](https://semver.org).

## [Unreleased]

### Fixed

- On a Mac, `sys0 install` without `sudo` now says it installed for you only, and that `sudo sys0 install` makes it a system service macOS doesn't keep off your local network ([#36](https://github.com/A-Hackers-Guide/sys1/issues/36)).

## [0.9.1] - 2026-11-01

### Fixed

- When a node can't be reached, the log now says what went wrong at every address, not only the last one, and on a Mac it recognizes **Local Network privacy** blocking sys0 and says how to fix it ([#32](https://github.com/A-Hackers-Guide/sys1/issues/32)).
- On a Mac, `sudo sys0 install` moves an existing per-user sys0 to a system service, keeping its pool and data. System services aren't subject to Local Network privacy, which otherwise asks again after every update.
- On Windows, `sys0 install` and `sys1 install` copy the programs to `C:\Program Files\sys1` and run the service from there, not from the user's folder, which any program running as that user could change ([#33](https://github.com/A-Hackers-Guide/sys1/issues/33)).
- On Windows the log file stayed empty when running as a service: a service has no console, and the log stopped at the first place it couldn't write ([#34](https://github.com/A-Hackers-Guide/sys1/issues/34)).
- sys0.exe and sys1.exe now carry Windows version information; Windows Security's machine-learning scan had quarantined an unsigned sys1.exe as a trojan. The dashboard now also says when a machine's program file was removed while running, as antivirus quarantine does ([#35](https://github.com/A-Hackers-Guide/sys1/issues/35)).

## [0.9.0] - 2026-11-01

Real machines, datacenter basics: Docker that was installed but not seen, installs that never updated, container engines and apps kept running, metrics.

### Fixed

- **Docker, Podman and QEMU weren't found by the background service** when they were installed in `/usr/local/bin`, Homebrew, Docker Desktop's own folder, `~/.docker/bin`, OrbStack or Rancher Desktop, or after the service had started (Windows services keep the PATH from boot). sys0 and sys1 now look in those places, and use Docker Desktop's, OrbStack's or Colima's socket when the default one is missing.
- **Installs that couldn't replace their own binary never updated**, for example the macOS installer's `/usr/local/bin` with sys0 running as you. Updates now go to the data folder, and the app switches to that copy when it starts (with the same rollback).
- **Homebrew, Scoop and other package-manager installs never updated** unless someone ran the package manager. They now update the same way, beside the package manager's copy, which wins again once it's newer.
- **Docker counted as working when Docker Desktop wasn't running**: newer Docker CLIs exit successfully without an engine, so sys0 now needs the engine to answer.


### Added

- **Container engines are supervised like apps.** On your own machines, sys0 and sys1 start the installed engine when they start (so at boot), start it again if it stops or crashes, back off after 5 failed starts in a row (then try every 30 minutes) and say so on the dashboard. Docker Desktop, OrbStack, Rancher Desktop, Colima and Podman machines on macOS; Docker Desktop, Rancher Desktop and Podman on Windows (started in the signed-in person's session); the Docker system service, snap or rootless Docker on Linux. `sys0 keep-engines-running on|off`; lent machines are off unless their owner turns it on.
- **Prometheus metrics** at `/metrics` on the node (machines, apps, crashes, updates). docs/MONITORING.md.
- **Health checks**: services can declare an HTTP or TCP check; an app that stops answering is restarted like a crashed one, and moved after repeated failures. The built-in services have them.
- Each machine card says **why** an app kind can't run there ("Docker is installed but not running. Open Docker Desktop and turn on 'Start when you sign in'", "sys0 isn't allowed to use it", …) and whether the machine **updates itself**, or why not.
- `sys0 doctor`: what this machine can run, what's in the way, and whether it updates itself, as the background service sees it.
- **Update all machines now** on the Machines tab: every machine and the node check for a new version within seconds instead of waiting for their daily check.

## [0.8.0] - 2026-11-01

Self-care: automatic updates, crash reports, a security review.

### Added

- **Automatic updates** for sys0 and sys1: daily checks with jitter, installed only when `checksums.txt` has a Sigstore signature from this repository's release workflow and the archive matches its checksum; atomic swap, restart through the service manager, automatic rollback when a new version keeps failing to start. Package-manager, container and mobile installs are left to their updater. `sys0 auto-update on|off`, `--no-auto-update`, `sys0 update`, `sys1 update`. docs/UPDATES.md.
- **Crash reports**: the panic and crashing goroutine of every sys0 and sys1 crash, scrubbed of names, addresses, paths, codes and secrets; machines hand them to their node; **Machines → Crash reports** on the dashboard, with a prefilled "Report on GitHub" link; nodes forward them to the project's crash intake (`intake/`, a Cloudflare Worker that alone holds a GitHub token) which files one `crash` issue per bug. `sys0 crash-reports on|off`, `--no-crash-reports`, a dashboard switch. docs/CRASHES.md.
- Release checksums are signed with Sigstore (`checksums.txt.sigstore.json`), from v0.7.0 on.
- **A Sileo/Zebra/Cydia repository** at `https://a-hackers-guide.github.io/sys1/cydia/`, rebuilt from each release.
- **Deploy from GitHub**: a package that builds a public repository's Dockerfile on one of your machines and runs it; the node follows the branch (conditional requests, no token or webhook) and redeploys each new commit, keeping the app's data. Packages can use `run.build` too. docs/GITHUB.md.
- An upgrade test in CI from every earlier release: the node keeps its identity, join code, machines and apps.
- **Battery detection on FreeBSD, DragonFly, OpenBSD and NetBSD** (`hw.acpi.acline`, `apm -a`, `envstat`): laptops pause on battery like everywhere else.

### Security

These come from a security review of the whole project.

- **A malicious node could make a lent machine delete or write any folder** by sending a task ID with a path in it. Clients now refuse any task, backup, restore or snapshot ID that isn't a plain ID. Everyone lending machines to other people's pools should update.
- **A machine could make itself the owner's** by asking. The node now has two join codes, one for your own machines and one to share, and the code decides. Machines that already joined keep their trust. `sys1 join-code --mine` prints the owner code.
- **The dashboard key was a cookie**, which browsers send to every port on `127.0.0.1`, including apps on the pool. It now lives in the page and travels in a header; old cookies are deleted. **Make a new dashboard key** locks out other browsers.
- Crash reports can't add links or break out of their code block, signatures are recomputed by the node and the intake, each machine may send 20 a day, and the intake caps new issues per hour.
- The sandbox gateway also refuses NAT64, 6to4 and Teredo addresses of private networks, and the other reserved ranges.
- Release workflow: read-only tokens for every job but the final one, which checks GoReleaser's builds against GoReleaser's checksums before signing; third-party tools pinned by hash.
- An update is rolled back only for a quick crash loop, and a rolled-back version is retried after 3 days.

### Fixed

- A relative `--data` folder broke containers (Docker needs absolute volume paths); data folders are now made absolute.

## [0.7.0] - 2026-11-01

More devices.

### Added

- **Android app** (`sys1_<v>_android.apk`) for phones, tablets and Android TV: paste a join code, lend only while charging with the screen off (TVs: whenever on power), a foreground service that restarts after reboots. arm64, x86_64 and ARMv7. CI installs it on an emulator and sees it join a node.
- **Magisk module** (`sys1_<v>_android_magisk.zip`) for rooted Android: sys0 as a lowest-priority daemon.
- **iPhone and iPad app** for sideloading (`sys1_<v>_ios_sideload.ipa`, AltStore/SideStore/your own certificate): lends while open and charging. CI runs it on a simulator and sees it join a node. Never the App Store.
- **Jailbreak package** (`sys1_<v>_ios_jailbreak_iphoneos-arm64.deb`, rootless): sys0 as a launchd daemon, pausing on battery (IOKit).
- sys0 reports `android` as its OS on Android, and falls back to public DNS where `/etc/resolv.conf` doesn't exist.

### Not possible yet

- Apple TV: Go can't build for tvOS.

## [0.6.0] - 2026-11-01

Virtual machines, desktops and gaming.

### Added

- **Virtual machines** (`kind: vm`) with QEMU: checksum-verified base images cached per machine, copy-on-write disks in pooled volumes (VMs move with their disks), port forwards, CPUs, memory, disk size, x86_64 and aarch64, cloud-init first-boot config, hardware acceleration per OS with a software fallback, clean shutdown and paused snapshots through QMP. Owner machines only. CI boots a real VM.
- **VM screens in the browser**: noVNC (MPL-2.0) bundled in the dashboard, VNC over WebSocket with a random password per VM, **Open screen** on the Apps tab.
- Packages: **Ubuntu Server VM** (x86_64 and ARM), **Linux desktop in your browser** (Webtop), **Game streaming host** (Sunshine + Moonlight, experimental).
- `shmMB` for containers; sys0 refuses native programs that aren't installed so the pool tries elsewhere.
- docs/VMS.md.

## [0.5.0] - 2026-11-01

Internet-scale pools.

### Added

- **Join from anywhere**: join codes list every node address (`--public-addr`, LAN, Tailscale/ZeroTier/WireGuard, router-mapped public address); clients try them in order, fail over, and adopt the node's current addresses from heartbeats.
- **Reachable from the internet** switch: the node opens its port on the router with NAT-PMP or UPnP, renews and removes the mapping, and detects carrier-grade NAT.
- **Clusters**: `sys1 --cluster` and `sys1 cluster join`; Raft-replicated state over mutually pinned TLS, a portable crash-safe Raft log on sys1's journal, automatic failover, standbys redirect clients (421), Nodes panel on the dashboard. docs/CLUSTER.md.
- **Verified results**: `verify: N` runs each batch piece on N machines and accepts matching output (plaintext content hashes); disagreeing machines get marks and are kept off verified work at 3.
- **Internet limits for lent machines**: networked apps run on an internal network behind a gateway (`sys0 egress-proxy`) that forwards declared ports and reaches only declared hosts, never private addresses. Packages declare `egress`; lenders can opt in to open internet (`sys0 join --open-internet`, or the wizard).

### Changed

- Snapshots include a plaintext content hash.
- Built-in apps that need wide internet access declare `egress: ["*"]`.

## [0.4.0] - 2026-11-01

AI and batch.

### Added

- **Batch jobs**: `shards: N` splits a package into N independently placed and retried pieces with `SYS1_SHARD_INDEX`/`SYS1_SHARD_COUNT`; **Collect results** restores every piece's output into one folder. New built-in "Batch job" package (any command, any image).
- **Groups**: a head plus N workers, each on a different machine; the head starts when all workers run, gets `SYS1_PEERS`, and restarts if a worker moves.
- **AI model split across machines** (`llama-distributed`): llama.cpp RPC workers plus a head serving an OpenAI-compatible API. sys1 builds `ghcr.io/a-hackers-guide/llama-rpc` (amd64, arm64) because the official images lack RPC; CI proves a model really runs split across two workers.
- **GPU inventory** with memory for NVIDIA, AMD, Intel and Apple; `minVramMB` scheduling; per-vendor GPU access for containers.
- `ownerOnly` packages, enforced by the node and sys0.
- docs/BATCH.md.

### Changed

- sys1 is never distributed through the App Store, on any Apple device.

## [0.3.0] - 2026-11-01

Pooled storage.

### Added

- **Pooled storage.** Every machine lends disk (10 GB by default, always leaving 5 GB free). Data is cut into chunks, encrypted with AES-256-GCM on the machine it comes from, named by its SHA-256, and kept in N copies on different machines. Holders can't read what they store. Lost copies are re-made automatically, extra copies trimmed, unused data deleted, and corrupt copies detected and replaced.
- **App data moves with apps.** Package `volumes` are snapshotted every minute and on stop (containers are paused for a consistent copy) and restored before the app starts on another machine. New `localVolumes` stay on the machine for big media, caches and models.
- **Backups.** Keep a folder on any of your machines backed up with N copies and M versions; back up now; restore any version into a new folder on any of your machines.
- **Dashboard Storage tab**: protection level, space lent per machine, backups, restores, and the recovery key. Apps show when their data was last saved.
- `sys0 join --storage-gb`, a disk-share slider in the setup wizard, and `sys1 --copies` / `--staging-gb`.
- docs/STORAGE.md.

### Changed

- Builtin packages split their volumes into pooled (settings) and local (media, caches, models).

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
