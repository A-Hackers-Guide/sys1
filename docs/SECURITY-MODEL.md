# Security model

sys1 lets strangers lend you their machines and lets you run software on them. Both sides need protecting. "Super powerful" only works if it's also "super safe", so security is designed in, not bolted on.

To report a vulnerability, see [SECURITY.md](../SECURITY.md).

## Who we protect

| Party | Worry | Protection |
|---|---|---|
| **Machine owner** (runs sys0) | The pool runs something harmful on my computer, or slows it down. | Trust levels, sandboxed containers, client-side checks, lowest priority. |
| **Node owner** (runs sys1) | Someone takes over my pool, or impersonates my node. | Join tokens, per-machine secrets, pinned TLS, a key-locked localhost dashboard. |
| **Package users** | A marketplace package is malicious or tampered with. | Ed25519 signatures, explicit trusted keys, plain-language permission lists. |

## Trust levels

**The join code decides it.** The node has two: one for the owner's own machines and one to share. A machine that joins with the shared code is a community machine whatever it asks for, so nobody can get owner-only work (VMs, native programs, unauthenticated services, backups) by claiming a machine is the owner's. A machine's owner can also ask for less: a lender who picks **Shared** stays community even with the owner code. **The client enforces its own level too**: it never takes the node's word for it.

| | 🏠 **Owner** ("this is my machine") | 🤝 **Community** ("I'm sharing it") |
|---|---|---|
| Containers | ✅ | ✅ with extra hardening |
| Native programs | ✅ | ❌ always refused |
| Linux capabilities | runtime default | all dropped except `CHOWN DAC_OVERRIDE FOWNER SETUID SETGID NET_BIND_SERVICE KILL` |
| Default choice in installer | | ✅ |

## Container hardening (every machine)

Every container sys0 starts gets:

- `--security-opt no-new-privileges`: no setuid escalation
- `--pids-limit 1024`: no fork bombs
- `--cpu-shares 2`: minimum CPU weight, so the owner's apps always win
- `--cpus` / `--memory` limits from the package
- `--network none` unless the package declares network access
- `--init`, `--rm`: clean process reaping, no leftovers
- **Never** `--privileged`, host networking, host PID, or host paths. App data uses named volumes; the only bind mount is the task's own state directory.

## Input validation

All container options are built by sys0 from a validated spec, never from free text:

- Image names must match `^[a-zA-Z0-9][a-zA-Z0-9._/:@-]*$` (so nothing like `--privileged` can sneak in).
- Ports must be `N:N` or `N:N/udp|tcp`; volumes must be absolute container paths without `..` or `:`; env names must be identifiers.
- Command arguments go after the image name, where the runtime treats them as the container's arguments, never as runtime options.

The node validates on deploy; the client validates again on receipt (`internal/protocol/protocol.go`, tests in `protocol_test.go`).

## Network security

- **Transport:** TLS 1.3 only.
- **Node identity:** the node generates a self-signed ECDSA P-256 certificate. Its SHA-256 fingerprint is inside the join code, and clients reject any other certificate. There are no certificate authorities to compromise.
- **Joining:** a 192-bit random join token. Rotate it any time; existing machines are unaffected.
- **Per-machine auth:** at registration each machine receives its own 192-bit secret. The node stores only its SHA-256 hash and compares it in constant time. Removing a machine revokes it instantly.
- **Limits:** request bodies are size-capped; header read timeouts block slow-loris attacks.

## Dashboard security

The dashboard can control the whole pool, so:

- It listens on **127.0.0.1 only** by default.
- Access needs a **192-bit key** (in the link printed at start, or `sys1 open`). The page moves it from the address bar into its own storage and sends it in an `Authorization` header. It is never a cookie: browsers send cookies for `127.0.0.1` to every port on it, apps on the pool included. **Make a new dashboard key** locks out every other browser.
- **Host header check** blocks DNS-rebinding attacks from malicious websites.
- **Changes need a custom `X-Sys1` header**, which cross-site forms cannot send (CSRF protection).
- **Strict Content-Security-Policy:** `default-src 'self'`, no inline scripts, no third-party code, `frame-ancestors 'none'`.
- All text from machines (names, outputs) is inserted as text, never as HTML.

## Marketplace security

- Builtin packages are compiled into the binary.
- Every other package must be signed with Ed25519 by a key listed in `<data>/trusted-keys.txt` (or `--trust-key`). The signature covers the whole manifest, so changing even one character invalidates it.
- Unverified packages are shown but **cannot be deployed**.
- Every package shows its permissions in plain words before deploy ("Uses the network", "Opens port 8096"…).
- Builtin package names cannot be shadowed by third-party ones.

## Machines must stay in their folders

Task, backup and restore IDs from the node name folders on the client, so the client accepts only plain IDs (letters, digits, `-`, `_`) and refuses anything with a path in it before touching the disk. Snapshot IDs are checked as content hashes.

## Updates

sys0 and sys1 install a new version only if `checksums.txt` carries a Sigstore signature from this repository's release workflow on a version tag, logged in Sigstore's transparency log, and the archive matches its checksum. There's no long-lived signing key to steal; a compromised GitHub account could still publish a release through the real workflow, and that release would be visible in the public log. A version that keeps failing to start (several restarts within minutes) is rolled back and retried 3 days later. In the release workflow only the final job can write to the release; it checks that GoReleaser's builds still match GoReleaser's own checksums before signing, and third-party tools are pinned by hash. See [Automatic updates](UPDATES.md).

## Crash reports

Reports carry only an app, version, OS, CPU, a scrubbed message and Go stack frames. They are scrubbed on the machine, checked and scrubbed again by the node and by the project's intake (which also recompute each bug's signature, so a sender can't make up new ones, and remove anything that could break out of the code block or add a link), and the intake alone holds a GitHub token (issues only, one repository). Lenders and pool owners can turn them off. See [Crash reports](CRASHES.md).

## Threat model: honest limits

| Threat | Status |
|---|---|
| A malicious **node** sends a harmful container to a community machine | Contained by the sandbox above. Container escapes in the runtime itself are out of our control; keep Docker/Podman updated. Rootless Podman is recommended for community machines. |
| A malicious **node** abuses a lent machine's network (spam, attacks, snooping on the lender's home network) | **Prevented by default.** On a machine lent by someone else, an app with network access sits on an internal network with no route anywhere. A locked-down gateway is its only way out: it reaches only the HTTP(S) hosts its package lists, never raw IPs or other ports (so no SMTP), and never private, loopback, link-local or CGNAT addresses, even when a name resolves to one. Apps that need unrestricted internet run there only if the lender opted in (`--open-internet`). Tested against real Docker in CI. |
| A malicious **client** returns fake results | **Caught for verified jobs.** Batch jobs with `verify: N` run each piece on N different machines and accept output only when N agree, comparing a hash of the plain contents. Machines that disagree get a mark; at 3 they are kept off verified work. Use `verify: 2` or more for work on machines you don't control. Unverified jobs still trust their machines. |
| A **VM's screen** is reachable on its machine's network | Each VM's screen (VNC over WebSocket) has its own random password, which the dashboard fetches for you and never puts in a URL. VMs run only on owner machines, never on machines lent by others. |
| A package opens an **unauthenticated network service** (e.g. llama.cpp RPC) | Packages like that are marked `ownerOnly`: the node never places them, and sys0 never runs them, on machines lent by other people. The dashboard lists every port a package opens before you deploy it. Keep such groups on a trusted network. |
| A machine **holding pool copies** reads or alters them | **Prevented.** Chunks are AES-256-GCM encrypted on the machine they come from, and holders never get a key. Altered chunks fail their SHA-256 check and are replaced from another copy. A holder can see only how many chunks it has and their sizes. |
| A malicious **node** reads your backed-up folders | The node holds the pool key, so a node you don't control could read what you back up to it. Back up only to your own node. Backups only run on owner machines, so a community machine never lets a node read its folders. |
| A malicious **client** reads the data of apps it runs | **Inherent.** Whoever owns a machine can inspect what runs on it. Don't deploy secrets to machines you don't trust. Confidential computing (SEV/TDX) is a long-term roadmap item. |
| Someone steals the shared join code | They can join a machine to your pool as a community machine (they gain nothing from your pool). Rotate the codes. Machines they joined can be removed. |
| Someone steals the dashboard key | Full control of the pool. Treat it like a password. Delete `dashboard.key` and restart the node to rotate it. |
| Supply chain (our own releases) | Releases are built by GitHub Actions from tagged commits, with SHA-256 checksums. Reproducible builds and signed releases (Sigstore) are on the roadmap. |
