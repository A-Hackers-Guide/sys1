# Platforms

sys1 is written in Go with no C dependencies (`CGO_ENABLED=0`), so one codebase compiles to a single static binary for each OS and CPU below. Every combination marked **builds** is compiled on every commit by CI.

## Support matrix

| OS | CPUs | sys0 client | sys1 node | Lowest priority | Battery aware | Service manager | Status |
|---|---|---|---|---|---|---|---|
| **Windows** 10/11, Server | amd64, arm64 | ✅ | ✅ | Idle class + background mode | ✅ | Windows Services | builds, CI tested |
| **macOS** 12+ | arm64, amd64 | ✅ | ✅ | Background QoS (`PRIO_DARWIN_BG`) + nice 19 | ✅ | launchd | builds, CI tested |
| **Linux** (any distro) | amd64, arm64, arm, 386, riscv64, ppc64le, s390x, mips64le | ✅ | ✅ | nice 19 + idle I/O class, every thread | ✅ | systemd, OpenRC, Upstart, SysV | builds, CI tested |
| **FreeBSD** | amd64, arm64 | ✅ | ✅ | nice 19 | – | rc.d | builds |
| **OpenBSD / NetBSD / DragonFly** | amd64 | ✅ | ✅ | nice 19 | – | rc.d | builds |
| **Solaris / illumos** | amd64 | ✅ | ✅ | nice 19 | – | manual (SMF planned) | builds |
| **AIX** | ppc64 | ✅ | ✅ | nice 19 | – | manual | builds |
| **Android** 8+ | arm64 | ✅ | – | nice 19 + idle I/O | ✅ | app (planned) | builds; app on the roadmap |
| **Android TV** | arm64 | ✅ | – | same | ✅ | app (planned) | roadmap |
| **iOS / iPadOS** | arm64 | sideload / jailbreak only | – | – | – | – | roadmap |
| **tvOS (Apple TV)** | arm64 | sideload / jailbreak only | – | – | – | – | roadmap |

"Builds" means the binary compiles and the portable parts (networking, journal, scheduler) are exercised by the shared test suite. Platform-specific behaviour is verified on the platforms CI runs on: Linux, macOS and Windows.

## Running apps on each OS

Most marketplace packages are **containers**:

| OS | Container runtime |
|---|---|
| Linux | Docker Engine or Podman: native, fastest |
| macOS / Windows | Docker Desktop, Podman Desktop, Rancher Desktop, OrbStack (runs a small Linux VM) |
| FreeBSD | Podman with Linux emulation (experimental), or native `exec` packages |
| Others | native `exec` packages on owner machines |

Without a container runtime a machine still joins the pool and can run native `exec` packages if it is an owner machine.

## Android

Phones and TV boxes are great idle compute, but Android is strict about background work. sys0 follows these rules on Android:

- **Unrooted:** contribute **only while charging and idle** (screen off). Work stops the moment you unplug. Packaged as an app using a foreground service with a visible notification, as Android requires.
- **Rooted:** may run as a system daemon with the same lowest-priority rules as Linux, still pausing on battery.
- **Android TV:** usually always plugged in, so it contributes whenever the TV is idle.

The command-line `sys0` binary already builds for Android and detects charging state through `/sys/class/power_supply`. The installable app (APK, F-Droid) is [on the roadmap](ROADMAP.md).

## iOS, iPadOS and Apple TV

sys1 is **never distributed through the App Store**, on any Apple device, in any form. Support is planned for:

- **Sideloaded** builds (AltStore, SideStore, a developer certificate), contributing while the app is open and the device is charging.
- **Jailbroken** devices, as a launchd daemon with full background support.

To check on your pool from an iPhone or iPad, open the dashboard in Safari over SSH port forwarding or a VPN (see the FAQ).

## Unusual and legacy systems

We don't decompile or reverse-engineer other operating systems. That's legally risky and unnecessary. New platforms arrive by:

1. **Cross-compiling**, if Go supports the OS/CPU (most do).
2. **Running sys0 in a VM or container** on that system.
3. **Community ports** using the documented [protocol](PROTOCOL.md): a client in any language just needs HTTPS and JSON.

## Container

The node also ships as a container image for Linux amd64 and arm64:

```bash
docker run -d --name sys1 -p 7443:7443 -p 127.0.0.1:7700:7700 \
  -v sys1-data:/data ghcr.io/a-hackers-guide/sys1 \
  serve --no-open --foreground --gui 0.0.0.0:7700 --public-addr <this-host-ip>:7443
docker exec sys1 sys1 open        # prints the dashboard link
docker exec sys1 sys1 join-code   # prints the join code
```

Keep the dashboard port bound to `127.0.0.1` as shown. Pass `--public-addr` so the join code holds an address other machines can reach, not the container's internal one.

Want a platform? [Open a platform request](https://github.com/A-Hackers-Guide/sys1/issues/new/choose).
