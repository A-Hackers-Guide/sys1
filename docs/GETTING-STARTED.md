# Getting started

This takes about five minutes. You need:

- One machine to be the **node** (the hub). Any OS. Ideally one that stays on.
- Any number of other machines to join as **clients**. Optional: the node is already a client.
- For most marketplace apps: **[Docker](https://docs.docker.com/get-docker/)** or **[Podman](https://podman.io/)** on the machines that should run them. sys1 runs apps in containers so they are sandboxed and work identically on every OS.

## 1. Start the node

Install it (this gives you both `sys1` and `sys0`):

```bash
brew install a-hackers-guide/tap/sys1          # macOS or Linux with Homebrew
```

or use the one-line installers on the [website](https://a-hackers-guide.github.io/sys1/#download), or download from the [Releases page](https://github.com/A-Hackers-Guide/sys1/releases). Then run:

```bash
sys1
```

You'll see:

```
  sys1 node is running.

  Dashboard:  http://127.0.0.1:7700/?key=…
  Join code:  sys1-join:eyJh…
```

and the dashboard opens in your browser. The link contains a private key: only someone with it can control your pool. Bookmark the dashboard after it opens; the key is saved in a cookie.

### Keep the node running all the time

To start the node automatically at boot, as a background service:

```bash
sys1 install
```

Then open the dashboard any time with:

```bash
sys1 open
```

On Linux and macOS this installs a per-user service if you are not root (no admin needed). Use `sudo sys1 install` for a system-wide one.

### Ports

| Port | What | Who connects |
|---|---|---|
| `7443/tcp` | Client API (TLS 1.3) | Your other machines. Allow it through the node's firewall. |
| `7700/tcp` | Dashboard | Only this machine (it listens on `127.0.0.1`). |

Machines outside your home network need to reach port 7443, for example through a VPN such as Tailscale or WireGuard, or a port forward. Use `--public-addr host:port` so the join code contains an address they can reach.

## 2. Add machines

On each machine you want to add:

1. Install **sys0**: the same install as the node (`brew install a-hackers-guide/tap/sys1`, the one-line installer, or [Releases](https://github.com/A-Hackers-Guide/sys1/releases)).
2. Run it: double-click, or run `sys0`. A setup wizard opens in your browser.
3. Paste the **join code** from the node's dashboard (**Add a machine** tab).
4. Choose **whose machine this is**:
   - **Mine**: you run the node too. Native programs may run here.
   - **Shared**: you're lending it to someone else's pool. Only sandboxed containers run here. **This is the safe choice.**
5. Click **Join the pool**. sys0 installs itself as a background service and disappears.

Terminal-only machines (servers, SSH):

```bash
sys0 join 'sys1-join:eyJh…' --name "basement server" --trust owner
```

Check on it any time:

```bash
sys0 status
```

Admin rights: on Windows and FreeBSD, installing the service needs Administrator/root. On Linux and macOS, sys0 installs a per-user service when run without root.

## 3. Deploy something

Open the **Marketplace** tab. Packages your machines can run have a **Deploy** button; the others say what they need (usually "a machine with Docker or Podman").

Try **Hello, pool!** first. Within a few seconds it appears under **Apps**, runs on the best machine, and shows **Finished**. Click **Output** to see which machine ran it.

Then try something real:

- **Jellyfin** or **Plex**: media server. Open `http://<machine>:8096` (the Apps tab shows which machine and port).
- **Audiobookshelf**: audiobooks and podcasts.
- **Ollama**: local AI. It prefers machines with a GPU.
- **Syncthing**: continuous backups between your devices.
- **Static website** + **Cloudflare Tunnel**: put a website on the internet with HTTPS and no open ports.
- **GitHub Actions runner**: use spare capacity for your CI.

## 4. What happens when things go wrong

You don't have to do anything. That's the point.

- **An app crashes**: sys0 restarts it, waiting a little longer each time.
- **It crashes 5 times in a row**: sys0 gives up on that machine, sends the app's last saved checkpoint to the node, and the node moves it to a different machine.
- **A machine turns off, sleeps or loses its network**: after about 15 seconds the node moves its apps elsewhere.
- **The node loses power**: when it comes back, everything is exactly as it was (see [Reliability](RELIABILITY.md)).

## Removing things

- Stop an app: **Apps → Stop**. Clear finished ones with **Clear**.
- Remove a machine from the pool: **Machines → Remove**. Its apps move elsewhere.
- Uninstall sys0 from a machine: `sys0 uninstall`, then delete the binary.
- Stop new machines joining with an old code: **Add a machine → Make a new join code**.

## Where things are stored

| | Linux / BSD | macOS | Windows |
|---|---|---|---|
| sys1 (as root) | `/var/lib/sys1` | `/Library/Application Support/sys1` | `C:\ProgramData\sys1` |
| sys1 (as you) | `~/.local/share/sys1` | `~/Library/Application Support/sys1` | `C:\ProgramData\sys1` |
| sys0 | same pattern with `sys0` | | |

Override with `--data <dir>` or the `SYS1_DATA_DIR` environment variable. Logs are `sys0.log` / `sys1.log` in the data directory.
