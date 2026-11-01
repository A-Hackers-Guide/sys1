# Platforms

sys1 is written in Go with no C dependencies (`CGO_ENABLED=0`, except the iOS builds, which talk to iOS's own frameworks), so one codebase compiles to a single static binary for each OS and CPU below. Every combination marked **builds** is compiled on every commit by CI.

## Support matrix

| OS | CPUs | sys0 client | sys1 node | Lowest priority | Battery aware | Service manager | Status |
|---|---|---|---|---|---|---|---|
| **Windows** 10/11, Server | amd64, arm64 | ✅ | ✅ | Idle class + background mode | ✅ | Windows Services | builds, CI tested |
| **macOS** 12+ | arm64, amd64 | ✅ | ✅ | Background QoS (`PRIO_DARWIN_BG`) + nice 19 | ✅ | launchd | builds, CI tested |
| **Linux** (any distro) | amd64, arm64, arm, 386, riscv64, ppc64le, s390x, mips64le | ✅ | ✅ | nice 19 + idle I/O class, every thread | ✅ | systemd, OpenRC, Upstart, SysV | builds, CI tested |
| **FreeBSD** | amd64, arm64 | ✅ | ✅ | nice 19 | ✅ | rc.d | builds |
| **OpenBSD / NetBSD / DragonFly** | amd64 | ✅ | ✅ | nice 19 | ✅ | rc.d | builds |
| **Solaris / illumos** | amd64 | ✅ | ✅ | nice 19 | – | manual (SMF planned) | builds |
| **AIX** | ppc64 | ✅ | ✅ | nice 19 | – | manual | builds |
| **Android** 8+ | arm64, x86_64, armv7 | ✅ app, or Magisk when rooted | – | nice 19 + idle I/O | ✅ | foreground service / Magisk | app CI tested on an emulator |
| **Android TV** | arm64, armv7 | ✅ same app | – | same | ✅ | same | builds |
| **iOS / iPadOS** 15+ | arm64 | ✅ sideload app, or jailbreak .deb | – | background QoS | ✅ | app / launchd (jailbreak) | app CI tested on a simulator |
| **tvOS (Apple TV)** | arm64 | ❌ not yet | – | – | – | – | Go can't build for tvOS yet |

"Builds" means the binary compiles and the portable parts (networking, journal, scheduler) are exercised by the shared test suite. Platform-specific behaviour is verified on the platforms CI runs on: Linux, macOS and Windows.

## macOS: run sys0 as a system service

macOS asks whether each program may use the **local network**, and asks again whenever the program changes, which an update does. Until someone clicks **Allow** on that Mac, sys0 can't reach a node on your network, and the log says "macOS is keeping sys0 off your local network". System services aren't asked. So on a Mac, after joining, run once:

```bash
sudo sys0 install
```

It moves your existing setup (same pool, same data) to a system service and removes the per-user one. sys0 still starts Docker Desktop in the signed-in person's session.

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

Phones and TV boxes are great idle compute, but Android is strict about background work. Get `sys1_<version>_android.apk` or `sys1_<version>_android_magisk.zip` from the [latest release](https://github.com/A-Hackers-Guide/sys1/releases/latest).

**The app (no root).** Install the APK (allow "install unknown apps" for your browser or file manager), open **sys0**, paste a join code and switch **Lend this device** on. Then:

- It works **only while the device is charging and the screen is off**. Unplug or pick up the phone and work stops within seconds; the pool moves it elsewhere.
- **Android TV** boxes are always plugged in and have no "screen off", so they lend whenever they're on power.
- Android requires a visible notification while it runs (a foreground service). It starts again after a reboot.
- A phone mostly lends pooled storage (and runs native work if you joined it as your own machine). It doesn't run containers or VMs.

**Rooted (Magisk).** Flash the Magisk module in the Magisk app and reboot, then join once from a root shell:

```sh
su -c "sys0 join <code> --data /data/adb/sys0 --no-service"
```

It runs as a daemon at the lowest priority, like on Linux, and still pauses on battery.

Each release's APK is signed with a new key until the project has a permanent one, so install a new version over the old one by uninstalling first (your join is kept only with the Magisk module). F-Droid is on the roadmap.

## iOS and iPadOS

sys1 is **never distributed through the App Store**, on any Apple device, in any form. There are two ways in:

**Sideload** `sys1_<version>_ios_sideload.ipa` with [AltStore](https://altstore.io), [SideStore](https://sidestore.io) or your own developer certificate (the IPA is unsigned; those tools sign it for your device). Open **sys0**, paste a join code, switch lending on. iOS doesn't let apps work in the background, so it lends **only while the app is open and the device is charging**: leave it on a charger with the app open overnight. The screen stays on while it lends; turn the brightness down.

**Jailbroken** (rootless jailbreaks such as Dopamine or palera1n): add the repository `https://a-hackers-guide.github.io/sys1/cydia/` to Sileo, Zebra or Cydia and install **sys0** (or install `sys1_<version>_ios_jailbreak_iphoneos-arm64.deb` from a release with `dpkg -i`), then join once:

```sh
sudo sys0 join <code> --no-service
```

A launchd daemon runs it in the background at the lowest priority, only while charging. Updates arrive through your package manager.

## Apple TV

Not yet. Go, which sys1 is written in, can't build programs for tvOS, and we won't ship a half-working port. If Go gains tvOS support, a sideload build follows. (Apple TV would be sideload or jailbreak only, never the App Store.)

To check on your pool from any phone or tablet, open the dashboard over SSH port forwarding or a VPN (see the FAQ).

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
