# Reliability: fast save, watchdog, failover

"No data loss" is easy to promise and hard to mean. This page says exactly what sys1 guarantees today, how, and what is still on the roadmap.

## Guarantees (v0.1)

| Situation | What happens | Data guarantee |
|---|---|---|
| Node loses power or crashes | On restart it replays its journal and continues. | Every acknowledged change (machine joined, app deployed, state change) survives. |
| Client loses power or crashes | The OS service manager restarts sys0. Undelivered results are re-sent from the outbox. | Every finished result reaches the node exactly once in effect (duplicates are rejected as stale). |
| An app crashes | Restarted with backoff: 0.5s, 1s, 2s, 4s… (max 30s). A run longer than 60s resets the count. | The app's local disk and checkpoint persist between restarts. |
| An app crashes **5 times in a row** | The watchdog stops it, reads its checkpoint, and reports failure. The node excludes that machine and moves the app elsewhere **with the checkpoint**. | The checkpoint (≤ 8 MB) moves with the app. |
| A machine goes offline (sleep, shutdown, network loss) | After 15s the node moves its apps elsewhere, resending the last checkpoint it cached. | The cached checkpoint moves with the app. |
| A machine never confirms an assignment | After 20s the task goes to another machine. | Nothing was running yet. |
| An app with pooled volumes moves (any reason above) | The new machine restores the app's latest volume snapshot before starting it. | Changes since the last snapshot (at most about a minute) are lost; everything before is kept, in N copies. |
| A machine holding pool copies disappears for 10+ minutes | The node re-makes its copies on other machines. | Data survives as long as one copy does. |
| The node fails, in a 3-node [cluster](CLUSTER.md) | Another node is elected within seconds and carries on with the same state; machines move to it on their own. | Every change a majority of nodes acknowledged survives. |
| A machine comes back after its work moved | The node tells it to cancel its stale copy; any result it sends is rejected as stale. | No split-brain: only the newest attempt can report. |

### What is *not* guaranteed yet

- **Live memory.** An app's in-RAM state is lost when its machine dies. Apps that care should write checkpoints (below). Live migration of running processes is not feasible across different OSes; see [Architecture](ARCHITECTURE.md#why-not-one-giant-vm-across-all-machines).
- **Local volumes.** Volumes a package marks as local (big media libraries, caches, AI models) stay on the machine the app ran on. Pooled volumes move; see [Storage](STORAGE.md).
- **Node redundancy.** A single node is a single point of failure, and its disk holds the list of snapshots in pooled storage (#27). Run a [cluster](CLUSTER.md) of 3 nodes so any one can fail, or back up the node's data directory.

## Fast save: the journal

Both the node's state and each client's outbox use `internal/journal`, a write-ahead log:

```
┌──────────────┬──────────────┬─────────────────────┐
│ length (u32) │ CRC-32C (u32)│ payload (JSON event) │  … repeated
└──────────────┴──────────────┴─────────────────────┘
```

- **Append** writes the record and calls `fsync` before returning. Once it returns, the record is on stable storage.
- **Open** reads records until the first one that is incomplete or fails its checksum, which is what a power cut mid-write leaves behind, and truncates there. Every earlier record is intact.
- **Compact** writes a snapshot to a temporary file, fsyncs it, atomically renames it over the journal, and fsyncs the directory. A crash at any instant leaves either the old or the new journal, never a mix.

Tests in `internal/journal/journal_test.go` simulate torn writes and bit flips.

## Watchdog

`internal/watchdog` runs one app attempt at a time:

```
start ─▶ exits cleanly ─▶ job: done ✔   service: counts as a crash
   │
   └─▶ crashes ─▶ crashes++ ─▶ crashes == 5? ─▶ give up: report + checkpoint
                       │
                       └─▶ wait 0.5s × 2^(crashes−1), max 30s ─▶ start again
```

Services (web servers, media servers) are expected to run forever, so even a clean exit is treated as a crash and restarted.

sys0 itself is watched by the OS: systemd `Restart=always`, launchd `KeepAlive`, Windows service recovery "restart after 5s".

## Checkpoints: making your app resumable

Every task gets an environment variable:

```
SYS1_CHECKPOINT=/sys1/checkpoint      (inside containers)
SYS1_CHECKPOINT=<task dir>/checkpoint (native programs)
```

Write your progress there as often as is useful (≤ 8 MB, replace the whole file). On start, read it if it exists. That's the whole API.

```sh
# Example: a resumable counting job
n=$(cat "$SYS1_CHECKPOINT" 2>/dev/null || echo 0)
while [ "$n" -lt 1000000 ]; do
  n=$((n+1))
  [ $((n % 10000)) -eq 0 ] && echo "$n" > "$SYS1_CHECKPOINT.tmp" && mv "$SYS1_CHECKPOINT.tmp" "$SYS1_CHECKPOINT"
done
```

Write to a temporary file and rename it, so a crash mid-write never leaves a half-written checkpoint.

The end-to-end test `TestCrashLoopMovesTaskWithCheckpoint` in `internal/node/node_test.go` proves the full path: crash 5×, give up, upload checkpoint, move, resume on another machine.

## Container engines

Most apps need a container engine, so sys0 keeps it running the same way it keeps apps running. On your own machines (and on lent machines whose owner runs `sys0 keep-engines-running on`):

- **At start**, which is at boot, sys0 starts the engine that's installed: Docker Desktop, OrbStack, Rancher Desktop, Colima or a Podman machine on a Mac; Docker Desktop, Rancher Desktop or Podman on Windows; the Docker service on Linux.
- **If it stops or crashes**, sys0 notices within 30 seconds and starts it again.
- **If it keeps failing**, sys0 slows down after 5 tries in a row, tries every 30 minutes, and the machine's card on the dashboard says what went wrong.
- Desktop apps such as Docker Desktop only run in someone's session: sys0 starts them for whoever is signed in at the screen. With nobody signed in, the card says so.

`sys0 doctor` shows the engines sys0 found and whether it keeps them running.

## Health checks

A crash is easy to see; a hang isn't. Services can declare a health check (an HTTP path or a TCP port). sys0 asks every 30 seconds, and after 3 misses in a row it stops the app and treats it as a crash: the watchdog restarts it, and after 5 crashes in a row the pool moves it to another machine with its saved data. The built-in Jellyfin, Plex, Audiobookshelf, Syncthing, Ollama, website, desktop and AI packages have one.
