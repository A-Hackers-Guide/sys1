# Pooled storage

sys1 turns spare disk space on your machines into one encrypted, replicated store. Two things use it:

- **App data.** An app's volumes are saved to the pool, so when the app moves to another machine its data comes with it.
- **Backups.** "Keep 3 copies of this folder" on any of your own machines, with versions you can restore anywhere.

## The short version

- Every machine running sys0 lends some disk: **10 GB by default**, chosen in the setup wizard or with `sys0 join --storage-gb N`. sys0 always leaves **5 GB of the disk free**, whatever the setting.
- Data is **encrypted on the machine it comes from** before it goes anywhere. Machines that store copies can't read them.
- The node keeps **3 copies** of app data (`sys1 --copies N`) and the number you choose for each backup, each copy on a different machine.
- When a machine disappears, the copies it held are **re-made on other machines** automatically.
- **Back up the node's data directory** (it holds the pool key and the list of snapshots), and keep the **recovery key** from the Storage tab somewhere safe, like a password manager. Without the key, nobody can read the pool, including you.

## How it works

### Chunks

Data is cut into pieces of at most 4 MiB. Each piece is sealed with **AES-256-GCM** using a random nonce:

```
chunk = nonce (12 bytes) ‖ ciphertext ‖ tag (16 bytes)
chunk ID = SHA-256(chunk)
```

Because the ID is the hash of the sealed bytes, any machine can check a chunk is intact without being able to read it. Damaged copies are detected on every read and replaced from another copy.

### Keys

The node creates a random 256-bit **pool key** (`pool.key` in its data directory, readable only by the node). Every owner of data gets its own key derived from it with HKDF-SHA256:

| Owner | Key |
|---|---|
| an app | `HKDF(pool key, "sys1 data key: task:<id>")` |
| a backup | `HKDF(pool key, "sys1 data key: backup:<id>")` |

The node gives a derived key **only to the machine that produces or restores that data**: the machine running the app, or the owner machine a backup comes from or is restored to. Machines that merely hold chunks never get any key.

### Snapshots

A snapshot turns a folder into chunks:

1. Each file's contents become chunks. Files whose size, modification time and permissions haven't changed since the last snapshot **reuse their chunks** and aren't read again.
2. A manifest lists every path, its type, permissions, owner, modification time and chunks. It is gzipped, split and sealed too.
3. One small sealed **root chunk** points at the manifest. The root's ID names the snapshot.

The node learns only chunk IDs, counts and total size, never file names or contents.

Restoring rebuilds the folder from the root. Restores refuse absolute paths, `..`, and paths through symlinks, so a malicious snapshot can't write outside its folder. Symlinks are created last for the same reason.

### Moving chunks around

Machines can't reach each other directly yet ([NAT traversal is v0.5](ROADMAP.md)), so chunks travel through the node:

```
 source machine ──PUT──▶ node staging ──GET──▶ holder machines
                                ▲
 holder machine ──PUT (asked by the node)──┘   when a copy must be re-made
```

The node's staging area is capped (`--staging-gb`, 4 GB by default). When it is full, uploads wait and retry. Staged chunks are removed once they have enough copies.

### Keeping copies

A maintenance pass on the node runs every second:

- **Missing copies** are re-made: a staged chunk is sent to the machines with the most free space; otherwise a machine holding it is asked to upload it first.
- A machine that has been offline for **10 minutes** stops counting as a copy. Shorter absences don't cause churn.
- **Extra copies** (a machine came back after its copies were re-made) are trimmed.
- **Chunks nothing uses** (old versions, removed backups, cleared apps) are deleted everywhere.
- For one minute after the node starts, nothing is judged missing, so machines can report what they hold first.

Machines report every chunk they hold when they connect, and the result of storage work on every heartbeat.

## App data

Marketplace packages list two kinds of volume:

| | `volumes` | `localVolumes` |
|---|---|---|
| Kept | in pooled storage | on the machine the app runs on |
| Moves with the app | ✅ | ❌ |
| Good for | settings, databases, small app data | big media libraries, caches, AI models |

For pooled volumes, sys0:

1. **Before starting** the app, restores its latest snapshot, unless this machine already has that exact data (the app ran here last).
2. **While it runs**, snapshots the volumes every minute (`snapshotSeconds` in the package), uploading only what changed.
3. **When it stops** for any reason (moved, crashed out, stopped, machine shutting down), takes one last snapshot.

Containers are **paused for the moment files are read**, so a snapshot is consistent, like a sudden power cut rather than a half-copied file. Most databases recover from that cleanly. Native programs on owner machines are not paused.

So if a machine dies suddenly, an app loses at most about one snapshot interval of changes. Apps can also use [checkpoints](RELIABILITY.md#checkpoints-making-your-app-resumable) for finer-grained progress.

## Backups

From the dashboard's **Storage** tab: pick one of your machines, a folder, how many copies, how often, and how many versions to keep. Backups only run on **owner machines**; a community machine never lets the node read its folders.

**Back up now** runs one immediately. **Restore** puts any version into a **new folder** on any of your online machines; restores never overwrite existing files.

Removing a backup deletes its versions from the pool.

## Limits today

- Chunks pass through the node, so the node's bandwidth limits how fast copies are made.
- A snapshot re-reads a whole file when it changes; there is no block-level deduplication within a file yet.
- Ownership of restored files is kept only when sys0 runs as root (as a system service).
- The node is a single point of failure for the pool's catalog. If its disk is lost, the chunks are still on the machines, but the list of which snapshots exist is gone, and rebuilding it from the machines isn't automated yet (#27). Back up the node's data directory. Multiple nodes with replicated state are planned for v0.5.
- Without the pool key, the pool can't be read. That's the price of nobody else being able to read it either.
