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

Every machine's owner picks one at setup. **The client enforces it, not the node.**

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
- Access needs a **192-bit key** (in the link printed at start, or `sys1 open`), stored as an `HttpOnly`, `SameSite=Strict` cookie.
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

## Threat model: honest limits

| Threat | Status |
|---|---|
| A malicious **node** sends a harmful container to a community machine | Contained by the sandbox above. Container escapes in the runtime itself are out of our control; keep Docker/Podman updated. Rootless Podman is recommended for community machines. |
| A malicious **node** abuses network access (e.g. sends spam from your IP) | **Partly open.** Packages with network permission can use your connection. Per-machine egress policies are on the [roadmap](ROADMAP.md) (#security issues). |
| A malicious **client** returns fake results | **Open.** Results from community machines are currently trusted. Redundant execution and result verification are on the roadmap. Run sensitive work only on owner machines. |
| A malicious **client** reads the data of apps it runs | **Inherent.** Whoever owns a machine can inspect what runs on it. Don't deploy secrets to machines you don't trust. Confidential computing (SEV/TDX) is a long-term roadmap item. |
| Someone steals the join code | They can join a machine to your pool (they gain nothing from your pool). Rotate the code. Machines they joined can be removed. |
| Someone steals the dashboard key | Full control of the pool. Treat it like a password. Delete `dashboard.key` and restart the node to rotate it. |
| Supply chain (our own releases) | Releases are built by GitHub Actions from tagged commits, with SHA-256 checksums. Reproducible builds and signed releases (Sigstore) are on the roadmap. |
