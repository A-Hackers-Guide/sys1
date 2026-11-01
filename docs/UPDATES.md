# Automatic updates

sys0 and sys1 keep themselves up to date. You don't have to do anything.

## How it works

1. **Checking.** About once a day (first check 10–60 minutes after starting, then every 20–28 hours, so a whole pool doesn't hit GitHub at the same moment), each app asks GitHub for the [latest release](https://github.com/A-Hackers-Guide/sys1/releases/latest). Drafts and pre-releases are ignored, and so is any version that isn't newer.
2. **Two checks before anything is installed.**
   - `checksums.txt` must carry a **Sigstore signature** made by this repository's release workflow, running for a version tag, and recorded in Sigstore's public transparency log. The signature is keyless: there is no signing key that could be stolen, and a signature made by anyone else (or by this repository from a branch) is refused. Sigstore's own trust root arrives through its TUF repository and is cached in the data folder.
   - The archive for this OS and CPU must match its SHA-256 in `checksums.txt`.
3. **Installing.** The new binary is written next to the old one and swapped in with a rename, which works on every OS, even while the app is running. The old binary is kept as `sys0.old` (or `sys1.old`).
4. **Restarting.** The app exits and the OS service manager (launchd, systemd, the Windows service manager…) starts the new version. Apps you started yourself in a terminal keep running and use the new version the next time they start.
5. **Rolling back.** If the new version starts 3 times without staying up for 10 minutes, the old binary is put back and that version is skipped from then on. Once the new version has run for 10 minutes, the old binary is deleted.

Updates are checked one machine at a time. Each one fetches straight from GitHub; nothing goes through the node.

## Installs that update elsewhere

The app leaves updating to whatever installed it:

| Installed with | Updated by |
|---|---|
| Homebrew, Scoop, winget, Chocolatey, snap, Nix, MacPorts | that package manager |
| `.deb` / `.rpm` / `.apk` packages (`/usr/bin`) | your system's package manager |
| AppImage | downloading the new AppImage |
| Container image | pulling the new image |
| Android app, Magisk module, iPhone/iPad app, jailbreak package | installing the new release |

The installers (`install.sh`, `install.ps1`, the macOS `.pkg` and the Windows `.msi`) and plain downloads update themselves. The folder holding the binary must be writable by the service; if it isn't, the app says so and doesn't update.

## Turning it off

| | |
|---|---|
| sys0 | `sys0 auto-update off` (or join with `--no-auto-update`), then `sys0 restart` |
| sys1 | start it with `--no-auto-update`; for the service: `sys1 install --no-auto-update` |

`sys0 status` shows whether updates are on. To update right now: `sys0 update` or `sys1 update`.

## Checking a release yourself

```bash
cosign verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity "https://github.com/A-Hackers-Guide/sys1/.github/workflows/packages.yml@refs/tags/v0.8.0" \
  checksums.txt
sha256sum --ignore-missing -c checksums.txt
```

Releases from v0.7.0 on are signed this way. The code is in [internal/update](../internal/update).
