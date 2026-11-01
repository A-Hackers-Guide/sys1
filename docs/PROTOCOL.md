# Protocol

sys0 and sys1 talk JSON over HTTPS (TLS 1.3) to the node's client API, port `7443` by default. Types live in `internal/protocol/protocol.go`. API version: **1**.

## Join code

```
sys1-join:<base64url(JSON)>
```

```json
{ "a": "192.168.1.5:7443", "f": "<64 hex chars: SHA-256 of the node certificate>", "t": "<join token>" }
```

The client connects to `a`, rejects any certificate whose fingerprint isn't `f`, and presents `t` once at registration.

## Authentication

After registering, every request carries:

```
Authorization: Bearer <agentId>.<secret>
```

Unknown or removed machines get `401`.

## Endpoints

### `POST /api/v1/register`

```json
{
  "apiVersion": 1,
  "joinToken": "…",
  "name": "basement server",
  "trust": "owner | community",   // a request; the node decides by the join token
  "version": "0.1.0",
  "resources": { …Resources… }
}
```

→ `200 {"agentId": "…", "secret": "…"}` · `403` wrong or rotated join code.

### `POST /api/v1/heartbeat` (every 5s ± 0.5s)

```json
{
  "resources": { …Resources… },
  "running": [ { "id": "task id", "attempt": 2, "screen": { "port": 41234, "password": "8 chars" } } ],
  "version": "0.1.0"
}
```

→ `200`

```json
{
  "assign": [ { "id": "…", "attempt": 2, "spec": { …TaskSpec… }, "checkpoint": "<base64>" } ],
  "cancel": [ "task id", … ]
}
```

- `assign` repeats until the client reports the task as running. Clients ignore duplicates (same id + attempt).
- `cancel` lists tasks the client should stop: stopped by the user, moved elsewhere, or unknown.

### `POST /api/v1/result`

```json
{
  "taskId": "…",
  "attempt": 2,
  "outcome": "completed | stopped | failed | refused",
  "exitCode": 0,
  "message": "crashed 5 times in a row (last: exit status 3)",
  "output": "last 4 KB of output",
  "checkpoint": "<base64, ≤ 8 MB>"
}
```

→ `200` accepted · `409` stale (task moved or attempt superseded; the client drops it) · `401`.

| Outcome | Node does |
|---|---|
| `completed` | marks the job done |
| `stopped` | marks it stopped (only sent when the node asked) |
| `failed` | excludes this machine, keeps the checkpoint, requeues |
| `refused` | excludes this machine, requeues |

### Pooled storage

| Endpoint | Body | Purpose |
|---|---|---|
| `PUT /api/v1/chunks/{id}` | sealed chunk bytes | Upload a chunk to the node's staging. Rejected unless `SHA-256(body) == id`. `507` when staging is full: retry later. |
| `GET /api/v1/chunks/{id}` | | Download a chunk. `503` + `Retry-After` while the node fetches it from a holder; `404` if no holder is online. |
| `POST /api/v1/inventory` | `{"chunks": [ids]}` | Every chunk this machine holds. Sent when the node asks. |
| `POST /api/v1/snapshot` | `{"owner", "attempt", "root", "chunks", "size", "files"}` | A snapshot is in the pool. `409` with `missing` if some chunk isn't. |
| `POST /api/v1/restore-result` | `{"id", "ok", "message", "files"}` | Outcome of a restore job. |

Heartbeats carry a `storage` report (`quotaBytes`, `usedBytes`, `chunks`, plus `stored` and `missing` chunk IDs since the last one). Responses may carry `fetch`, `upload`, `drop` (chunk IDs), `inventory`, `backups`, `restores`, and `hurry` (heartbeat again in about a second). Task assignments for apps with volumes carry `dataKey` and `restore` (the snapshot to restore first). See [Storage](STORAGE.md).

### `POST /api/v1/crash`

A crash report from an earlier run of sys0 (see [Crash reports](CRASHES.md)): `{"app", "version", "os", "arch", "signature", "message", "frames", "count", "first", "last"}`. The node refuses reports that don't have exactly this shape (400) and scrubs the message again. Clients send queued reports about every 10 minutes and delete them once accepted or refused.

### Clusters

| Endpoint | Purpose |
|---|---|
| `POST /api/v1/cluster/join` | `{"token"}` → the cluster's certificate, key and pool key, for a node joining with `sys1 cluster join` |
| `POST /api/v1/cluster/add` | `{"token", "id", "name", "raftAddr", "apiAddrs"}`: a joined node, now running, asks to become a voting member |

A standby node answers every other client request with **421 Misdirected Request**; clients then try the next address. Heartbeat responses carry `nodeAddrs`, the addresses the node can currently be reached on, and clients adopt them.

An owner's machine reports `canHost` in its resources when sys1 is installed beside sys0 and sys0 may install services, and `hosting` once it is a cluster member. A heartbeat response may carry `host: {"role": "standby" | "witness", "code": "sys1-cluster:…"}`; the machine then runs `sys1 cluster join [--witness] <code>` and `sys1 install --cluster`, once per code. See [Clusters](CLUSTER.md#a-standby-and-a-witness).

### `GET /healthz`

→ `200 ok`

## Types

### Resources

```json
{
  "os": "linux", "arch": "amd64", "hostname": "box",
  "cpuCores": 8, "cpuIdlePercent": 87.5,
  "memTotal": 17179869184, "memFree": 9126805504,
  "diskTotal": 512110190592, "diskFree": 201863462912,
  "gpus": ["NVIDIA GeForce RTX 3060"],
  "onBattery": false,
  "runtimes": ["docker", "exec"],
  "addr": "192.168.1.20",
  "gpuInfo": [{ "vendor": "nvidia", "name": "NVIDIA GeForce RTX 3060", "vramMB": 12288, "freeMB": 11000 }]
}
```

`runtimes` lists only runtimes whose daemon actually answered. `exec` is present only on owner machines.

### TaskSpec

```json
{
  "kind": "container | exec | vm",
  "image": "jellyfin/jellyfin:latest",
  "command": ["arg", "…"],
  "env": { "NAME": "value" },
  "ports": ["8096:8096", "53:53/udp"],
  "volumes": ["/config"],
  "localVolumes": ["/media"],
  "snapshotSeconds": 60,
  "network": true,
  "cpu": 1.5,
  "memoryMB": 2048,
  "gpu": true,
  "service": true,
  "os": ["linux", "darwin"],
  "minVramMB": 8000,
  "shards": 0,
  "ownerOnly": false,
  "verify": 0,
  "egress": ["api.example.com"],
  "group": { "workers": 2, "peerPort": 50052, "worker": { …TaskSpec… } },
  "shmMB": 0,
  "vm": { "image": "https://…", "sha256": "…", "arch": "x86_64", "cpus": 2, "diskGB": 20, "screen": true, "cloudInit": "…" }
}
```

## Timing

| Constant | Value |
|---|---|
| Heartbeat interval | 5s (± 0.5s jitter) |
| Offline after | 15s without a heartbeat |
| Assignment confirmation timeout | 20s |
| Client backoff when the node is unreachable | 1s → 30s |
| Watchdog backoff | 0.5s → 30s, give up after 5 consecutive crashes |
| Max placements per task | 10 |

## Compatibility

New fields are added as optional; clients and nodes ignore fields they don't know. A breaking change bumps `APIVersion`.
