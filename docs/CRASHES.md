# Crash reports

When sys0 or sys1 crashes anywhere, the project hears about it and can fix it, without anyone copying logs and without anything personal leaving your machines.

## What a report contains

Only this:

```json
{
  "app": "sys0",
  "version": "0.8.0",
  "os": "linux",
  "arch": "arm64",
  "signature": "3f9a1c07b2e4",
  "message": "panic: runtime error: invalid memory address or nil pointer dereference",
  "frames": [
    "github.com/A-Hackers-Guide/sys1/internal/agent.(*Agent).Run (github.com/A-Hackers-Guide/sys1/internal/agent/agent.go:126)"
  ],
  "count": 1
}
```

- **The message** is scrubbed: IP and MAC addresses, host names, URLs, email addresses, file paths, join codes, anything that looks like a key or token, and this machine's own host name, user name and home folder are replaced with placeholders like `<ip>` and `<path>`. `@` becomes `(at)`, so a report can't mention anyone on GitHub.
- **The frames** are function names and source lines of sys1's code (and Go's), from the goroutine that crashed. Nothing else is kept: no arguments, memory addresses, other goroutines, logs, app data or settings.
- **The signature** names the bug by the functions it crashed in, so the same bug matches across machines and versions.

## Where it goes

1. **On the machine.** Go writes a crashing program's last words to `crash/<app>-output.txt` in the data folder. On the next start, sys0 or sys1 turns that into a report as above and deletes the raw output.
2. **To your node.** sys0 hands its reports to the node it joined. The node checks each one against the shape above and scrubs it again, so even a modified client can't slip anything else in. Repeats of the same bug are counted, not stored again.
3. **On your dashboard.** **Machines → Crash reports** lists every crash, with exactly the text that is sent.
4. **To the project.** The node sends each new crash to the project's **crash intake**, a small service (code in [intake/](../intake)) that files one GitHub issue per bug on [A-Hackers-Guide/sys1](https://github.com/A-Hackers-Guide/sys1/issues?q=label%3Acrash), labeled `crash`, and adds a one-line comment when the same bug shows up again (at most once a day). The intake checks and scrubs every report a third time, and rate-limits each sender. It's the only place that holds a GitHub token: no app ever carries one.

The project's intake isn't online yet. Until it is, the dashboard offers **Report on GitHub** next to each crash: a link that opens a prefilled issue for you to look over and submit yourself.

## Turning it off

- **For your whole pool:** switch off "Send new crashes" under **Machines → Crash reports**. Reports still appear on your dashboard; they just don't leave it.
- **For one machine you lend:** `sys0 crash-reports off` (or join with `--no-crash-reports`). That machine keeps its crashes to itself, even from the node.
- **Your own intake:** set `SYS1_CRASH_INTAKE=https://…` for the node to send to an intake you run, or `SYS1_CRASH_INTAKE=` (empty) to never send.

## Running the intake

The intake is a Cloudflare Worker. Deploying it needs a Cloudflare account and a GitHub token, which only a maintainer holds:

1. Create a **fine-grained** personal access token on the GitHub account that should file the issues (the project files them as `A-Hackers-Guide`), with access to **only** `A-Hackers-Guide/sys1` and **only** the **Issues: Read and write** permission.
2. Deploy:
   ```bash
   cd intake
   npx wrangler deploy
   npx wrangler secret put GITHUB_TOKEN
   ```
3. Put the Worker's URL in `crash.DefaultIntake` ([internal/crash/store.go](../internal/crash/store.go)) and release.

`node --test intake/` runs the intake's tests.
