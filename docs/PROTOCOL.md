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
  "trust": "owner | community",
  "version": "0.1.0",
  "resources": { …Resources… }
}
```

→ `200 {"agentId": "…", "secret": "…"}` · `403` wrong or rotated join code.

### `POST /api/v1/heartbeat` (every 5s ± 0.5s)

```json
{
  "resources": { …Resources… },
  "running": [ { "id": "task id", "attempt": 2 } ],
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
  "runtimes": ["docker", "exec"]
}
```

`runtimes` lists only runtimes whose daemon actually answered. `exec` is present only on owner machines.

### TaskSpec

```json
{
  "kind": "container | exec",
  "image": "jellyfin/jellyfin:latest",
  "command": ["arg", "…"],
  "env": { "NAME": "value" },
  "ports": ["8096:8096", "53:53/udp"],
  "volumes": ["/config"],
  "network": true,
  "cpu": 1.5,
  "memoryMB": 2048,
  "gpu": true,
  "service": true,
  "os": ["linux", "darwin"]
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
