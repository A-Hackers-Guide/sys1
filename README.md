<p align="center">
  <img src="site/logo.svg" width="96" alt="sys1 logo">
</p>

<h1 align="center">sys1</h1>

<p align="center">
  <b>Turn every computer you have, or that friends share with you, into one cloud.</b><br>
  Free · open source · public domain · every OS
</p>

<p align="center">
  <a href="https://a-hackers-guide.github.io/sys1/">Website</a> ·
  <a href="docs/GETTING-STARTED.md">Get started</a> ·
  <a href="docs/README.md">Documentation</a> ·
  <a href="docs/ROADMAP.md">Roadmap</a> ·
  <a href="https://github.com/A-Hackers-Guide/sys1/issues">Issues</a>
</p>

---

Most computers sit idle most of the day. **sys1 pools that spare power** (CPU, memory, disk and GPU) from all your machines into one place, and runs websites, media servers, backups, AI models, game servers and more on it. You control everything from one dashboard.

It comes as **two apps, everywhere**:

| App | What it is | What you see |
|---|---|---|
| **sys0**: the client | Installed on every machine that lends its spare power. Runs at the **lowest priority** the OS offers, so your apps and games always come first. | A friendly graphical installer, then **nothing**: it disappears into the background. |
| **sys1**: the node | The hub machines join. It schedules work, moves it when machines go away, and hosts the marketplace. | A **full dashboard**, all the time. It's the input and output for the whole pool. |

```
   ┌─────────┐   ┌─────────┐   ┌─────────┐
   │  sys0   │   │  sys0   │   │  sys0   │     every machine, every OS,
   │ Windows │   │  Linux  │   │  macOS  │     lowest priority, invisible
   └────┬────┘   └────┬────┘   └────┬────┘
        │   TLS 1.3, pinned certificate │
        └─────────────┬───────────────┘
                 ┌────┴────┐
                 │  sys1   │  ← dashboard, marketplace, scheduler,
                 │  node   │    crash-safe journal, GitHub + Cloudflare
                 └─────────┘
```

## What it does today (v0.1)

- ✅ **Joins machines in one paste.** The node shows a join code; paste it into the sys0 installer.
- ✅ **Lowest priority on every OS.** `nice 19` + idle I/O on Linux, background QoS on macOS, `IDLE_PRIORITY_CLASS` + background mode on Windows, `nice 19` on BSD/UNIX. Containers get the minimum CPU weight too.
- ✅ **Places work on the best machine** by spare CPU, memory and GPU, and skips busy machines and laptops on battery.
- ✅ **Watchdog.** Crashed apps restart with backoff. After **5 crashes in a row** it stops, uploads the app's fast-saved checkpoint to the node, and the node **moves the app to another machine**, which resumes from that checkpoint.
- ✅ **Machine goes offline?** Its work is resent to a replacement within ~15 seconds, with the cached checkpoint.
- ✅ **Fast save.** The node's state and each client's undelivered results live in an fsync'd, checksummed write-ahead journal. Pull the plug at any moment and nothing acknowledged is lost.
- ✅ **Marketplace** with Jellyfin, Plex, Audiobookshelf, Ollama (AI), Syncthing (backups), websites, Cloudflare Tunnel, GitHub Actions runners, any container… and it only offers what your connected machines can actually run.
- ✅ **Signed packages.** Third-party packages must be Ed25519-signed by a key you chose to trust.
- ✅ **Safe for strangers.** Machines shared by other people only run hardened, sandboxed containers. Native programs run only on machines whose owner said "this is mine".
- ✅ **Builds for 21+ OS/CPU combinations** from one codebase: Windows, macOS, Linux, FreeBSD, OpenBSD, NetBSD, DragonFly, Solaris, illumos, AIX, Android…

See the **[roadmap](docs/ROADMAP.md)** for what's next (pooled storage, AI model sharding, cloud gaming, native installers for every package manager…).

## Quick start

**1. Start a node** on the machine that will be the hub:

```bash
sys1
```

The dashboard opens in your browser. That machine is already part of the pool.

**2. Add more machines.** Install sys0 on each one and paste the join code from the dashboard's **Add a machine** tab. Or from a terminal:

```bash
sys0 join sys1-join:eyJh…
```

**3. Deploy something** from the **Marketplace** tab. Done.

Full walkthrough: **[docs/GETTING-STARTED.md](docs/GETTING-STARTED.md)**.

## Install

```bash
brew install a-hackers-guide/tap/sys1                              # macOS, Linux
curl -fsSL https://a-hackers-guide.github.io/sys1/install.sh | sh  # Linux, macOS, BSD
```

```powershell
irm https://a-hackers-guide.github.io/sys1/install.ps1 | iex      # Windows
```

Pre-built downloads for every platform are on the **[Releases](https://github.com/A-Hackers-Guide/sys1/releases)** page. Packages for package managers are being rolled out; see [docs/DISTRIBUTION.md](docs/DISTRIBUTION.md) for the status of each one.

Build from source (needs [Go](https://go.dev/dl/) 1.26+):

```bash
git clone https://github.com/A-Hackers-Guide/sys1
cd sys1
go build ./cmd/sys0 && go build ./cmd/sys1
```

## Documentation

| | |
|---|---|
| [Getting started](docs/GETTING-STARTED.md) | Install, join, deploy, in plain words |
| [Architecture](docs/ARCHITECTURE.md) | How the pieces fit together |
| [Reliability](docs/RELIABILITY.md) | Fast save, watchdog, failover: exactly what "no data loss" means |
| [Security model](docs/SECURITY-MODEL.md) | Trust levels, sandboxing, what a malicious node or client can and cannot do |
| [Marketplace packages](docs/PACKAGES.md) | Package format, signing, publishing |
| [Protocol](docs/PROTOCOL.md) | The client ⇄ node API |
| [Platforms](docs/PLATFORMS.md) | Support for every OS, including Android, iOS and Apple TV |
| [Distribution](docs/DISTRIBUTION.md) | Every package manager we target, and their status |
| [Roadmap](docs/ROADMAP.md) | Where this is going |
| [FAQ](docs/FAQ.md) | Questions people ask |

## Contributing

Everyone is welcome. Please read **[CONTRIBUTING.md](CONTRIBUTING.md)**. In short: open an issue for anything (bugs, ideas, packages, platforms) and we'll take it from there. Development happens on branches, not pull requests.

## License

**Public domain** ([The Unlicense](LICENSE)). Use it, change it, sell it, close it, anything, anywhere. No warranty, no responsibility taken. All we ask, and it's only a request: **share issues and improvements with us, and share sys1 with other people.** We respect you; please respect us.
