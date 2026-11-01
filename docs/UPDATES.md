# Automatic updates

sys0 and sys1 keep themselves up to date. You don't have to do anything.

## How it works

1. **Checking.** About once a day (first check 10–60 minutes after starting, then every 20–28 hours, so a whole pool doesn't hit GitHub at the same moment), each app asks GitHub for the [latest release](https://github.com/A-Hackers-Guide/sys1/releases/latest). Drafts and pre-releases are ignored, and so is any version that isn't newer.
2. **Two checks before anything is installed.**
   - `checksums.txt` must carry a **Sigstore signature** made by this repository's release workflow, running for a version tag, and recorded in Sigstore's public transparency log. The signature is keyless: there is no signing key that could be stolen, and a signature made by anyone else (or by this repository from a branch) is refused. Sigstore's own trust root arrives through its TUF repository and is cached in the data folder.
   - The archive for this OS and CPU must match its SHA-256 in `checksums.txt`.
3. **Installing.** The new binary is written next to the old one and swapped in with a rename, which works on every OS, even while the app is running. The old binary is kept as `sys0.old` (or `sys1.old`).
4. **Restarting.** The app exits and the OS service manager (launchd, systemd, the Windows service manager…) starts the new version. Apps you started yourself in a terminal keep running and use the new version the next time they start.
5. **Rolling back.** If the new version restarts more than 3 times in quick succession (each within 5 minutes of the last) without staying up for 10 minutes, the old binary is put back and that version is skipped for 3 days. Reboots and restarts by hand don't count. Once the new version has run for 10 minutes, the old binary is deleted.

Updates are checked one machine at a time. Each one fetches straight from GitHub; nothing goes through the node.

## However it was installed

Every install updates itself:

- **Plain downloads and the installers** (`install.sh`, `install.ps1`, the macOS `.pkg`, the Windows `.msi`): the binary is replaced where it is, when the app may write there.
- **When it may not** (an installer put it in a system folder and it runs as you), and **for package managers** (Homebrew, Scoop, winget, Chocolatey, snap, Nix, MacPorts, `.deb`/`.rpm`/`.apk`): the package manager's copy is never touched. The update goes to `bin/` in the data folder, and the app switches to that copy when it starts. When the package manager later installs a newer version, that one runs and the copy in the data folder is removed.

Left to their own updaters: container images (pull the new image), the Android app and Magisk module, and the iPhone/iPad app and jailbreak package.

`sys0 doctor` shows whether a machine updates itself, and so does its card on the dashboard. **Update all machines now** on the Machines tab makes every machine check right away.

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
