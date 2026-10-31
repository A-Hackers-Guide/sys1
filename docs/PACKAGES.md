# Marketplace packages

A package is one JSON file: a description for humans plus a spec for machines. Anyone can write one.

## Example

```json
{
  "name": "uptime-kuma",
  "version": "1.0.0",
  "title": "Uptime Kuma",
  "description": "Monitor your websites and get alerts when they go down. Opens on port 3001.",
  "category": "Web",
  "icon": "📈",
  "author": "you",
  "homepage": "https://github.com/louislam/uptime-kuma",
  "inputs": [],
  "run": {
    "kind": "container",
    "image": "louislam/uptime-kuma:1",
    "network": true,
    "ports": ["3001:3001"],
    "volumes": ["/app/data"],
    "memoryMB": 256,
    "service": true
  }
}
```

## Fields

| Field | Required | Notes |
|---|---|---|
| `name` | ✅ | Lowercase id: `a-z 0-9 -`, max 63 characters. Unique in the catalog. |
| `version` | ✅ | Any string; semver recommended. |
| `title` | ✅ | Shown on the card. |
| `description` | | One or two sentences. Say which port it opens. |
| `category` | | Groups packages in the marketplace filter. |
| `icon` | | One emoji. |
| `author`, `homepage` | | |
| `inputs` | | Values asked for at deploy time (below). |
| `run` | ✅ | A [TaskSpec](PROTOCOL.md#taskspec). |

### Inputs

```json
"inputs": [
  { "name": "TUNNEL_TOKEN", "label": "Tunnel token", "secret": true },
  { "name": "IMAGE", "label": "Image", "default": "alpine:3" }
]
```

`${NAME}` anywhere in `run.image`, `run.command` or `run.env` values is replaced with the value. `secret` inputs are masked in the dashboard. The spec is validated **after** substitution, so an input cannot smuggle options into the container runtime.

### Kinds

- **`container`**: runs anywhere with Docker or Podman. Linux containers run natively on Linux and in a lightweight VM on macOS and Windows (Docker Desktop / Podman machine). **Prefer this.**
- **`exec`**: a native program. Set `run.os` to the OSes it supports (`linux`, `darwin`, `windows`, `freebsd`, `any`…). Runs **only on owner machines**.

### How compatibility is decided

The marketplace only enables **Deploy** when at least one online machine can run the package:

- `container` → some machine has a working container runtime.
- `exec` → some *owner* machine runs one of the listed OSes.

Otherwise the card says what's missing.

## Signing

Packages that aren't built in must be signed, and the node owner must trust the key.

**Publisher:**

```bash
sys1 pkg keygen my.key          # once; prints your public key, keep my.key secret
sys1 pkg sign uptime-kuma.json --key my.key
sys1 pkg verify uptime-kuma.json
```

Publish your **public key** somewhere people can check it (your website, GitHub profile, README).

**Node owner:**

1. Add the publisher's public key, one per line, to `<sys1 data dir>/trusted-keys.txt` (or pass `--trust-key`).
2. Copy the package's `.json` into `<sys1 data dir>/packages/`.
3. Restart the node. The package shows **Verified signature**.

Any edit to a signed manifest, even whitespace inside a value, breaks the signature, and the package becomes undeployable until re-signed.

## Contributing a builtin package

Builtin packages live in `internal/marketplace/catalog/`. To propose one, open an issue with the **Package request** template. Good builtin packages are:

- popular, open source, actively maintained
- pinned to a trustworthy image publisher (the project's official image)
- minimal in permissions: only the ports, volumes and network they need
- clear about what they open and where

## Ideas for packages

Home Assistant, Nextcloud, Immich, Vaultwarden, Pi-hole, Minecraft/Valheim servers, Navidrome, Paperless-ngx, Gitea/Forgejo, Uptime Kuma, Stable Diffusion, ComfyUI, BOINC, Folding@home, Tor relays, IPFS… [Request one](https://github.com/A-Hackers-Guide/sys1/issues/new/choose).
