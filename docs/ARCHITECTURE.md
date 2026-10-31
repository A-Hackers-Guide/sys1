# Architecture

sys1 is deliberately simple: **one hub, many clients, one protocol, two binaries**, all written in Go so the same code compiles natively for every OS with no runtime to install.

## The big picture

```
                       ┌──────────────────────────── sys1 node ─────────────────────────────┐
  browser ──HTTP──▶    │  Dashboard (127.0.0.1:7700)     Marketplace catalog                │
  (owner only)         │        │                         (builtin + signed packages)        │
                       │        ▼                                                            │
                       │  ┌───────────┐   every 1s   ┌───────────┐     ┌──────────────────┐  │
                       │  │   state   │◀────────────▶│ scheduler │     │ journal (fsync'd │  │
                       │  │ machines, │              └───────────┘     │ write-ahead log) │  │
                       │  │   tasks   │───────── every change ───────▶ └──────────────────┘  │
                       │  └───────────┘                                                     │
                       │        ▲  Client API (:7443, TLS 1.3, self-signed + pinned)         │
                       └────────┼────────────────────────────────────────────────────────────┘
                                │ register · heartbeat (5s) · result
             ┌──────────────────┼──────────────────┐
     ┌───────┴──────┐   ┌───────┴──────┐   ┌───────┴──────┐
     │    sys0      │   │    sys0      │   │    sys0      │
     │  lowest prio │   │  lowest prio │   │  lowest prio │
     │  watchdog    │   │  watchdog    │   │  watchdog    │
     │  outbox      │   │  outbox      │   │  outbox      │
     │   ▼          │   │   ▼          │   │   ▼          │
     │ docker/podman│   │ docker/podman│   │ native exec  │
     └──────────────┘   └──────────────┘   └──────────────┘
```

## Why not "one giant VM across all machines"?

The original idea was a single virtual machine stretched over every computer. That can't work for general software: a CPU expects memory to answer in about **100 nanoseconds**, while even a fast home network takes **100,000+ nanoseconds**. A VM spread across machines would be thousands of times slower than any one of them.

So sys1 does what real clouds do:

1. **Placement.** Each app runs whole on the machine best suited to it right now.
2. **Movement.** When that machine goes away or the app keeps crashing, the app moves, carrying its checkpoint.
3. **Pooling where it really works.** Storage, batch jobs and some AI workloads genuinely split across machines (see the [roadmap](ROADMAP.md)).

## Components

### sys0 (client), `cmd/sys0`, `internal/agent`

| Piece | File | Job |
|---|---|---|
| Priority | `internal/priority` | Drops the whole process to the OS's lowest CPU and I/O priority before anything else runs. Children inherit it. |
| Setup wizard | `internal/setup` | Local web page (works on every OS without a GUI toolkit) that joins the node and installs the service. |
| Service | `internal/cli` | Registers with launchd, systemd, OpenRC, Upstart, SysV, rc.d or the Windows Service Manager via [kardianos/service](https://github.com/kardianos/service). The OS restarts sys0 if it ever dies. |
| Agent loop | `internal/agent/agent.go` | Heartbeat every 5s ± 0.5s of jitter; applies assignments and cancellations; backs off up to 30s when the node is unreachable. Running apps keep running. |
| Runner | `internal/agent/runner.go` | Builds hardened `docker run` / `podman run` commands or native processes; captures the output tail; enforces log caps. |
| Watchdog | `internal/watchdog` | Restarts crashed apps with exponential backoff; gives up after 5 consecutive crashes. |
| Outbox | `internal/journal` | Every result is written durably before delivery and kept until the node confirms it. |
| Resources | `internal/resources` | Spare CPU, memory, disk, GPUs and working container runtimes. |

### sys1 (node), `cmd/sys1`, `internal/node`

| Piece | File | Job |
|---|---|---|
| Client API | `node.go` | `register`, `heartbeat`, `result` over TLS 1.3. |
| Scheduler | `scheduler.go` | Every second: moves work off offline machines, times out unconfirmed assignments, and places queued tasks by score. |
| State + journal | `state.go` | Every change is an event appended to the journal *before* it takes effect; replaying events rebuilds state. Compacted into a snapshot every 2,000 events. |
| Dashboard | `gui.go`, `web/` | Localhost-only, key-protected UI with no third-party code. |
| Marketplace | `internal/marketplace` | Loads builtin and signed packages, checks compatibility against connected machines. |
| Built-in client | `cmd/sys1/main.go` | The node lends its own machine to the pool through an in-process sys0 with owner trust. |

## The life of a task

```
 Deploy ─▶ queued ─▶ assigned ─▶ running ─▶ done (jobs)
             ▲          │            │
             │          │ not confirmed in 20s
             │          ▼            │ machine offline 15s / restarted
             └──── requeue ◀─────────┘ crashed 5× (machine excluded, checkpoint kept)
                        │
                        └─ after 10 placements ─▶ failed
 Stop ─▶ stopping ─▶ stopped
```

1. **Deploy.** The dashboard posts a package and its inputs. The node substitutes inputs, validates the resulting spec, journals the task as `queued`, and schedules immediately.
2. **Place.** The scheduler scores every online machine that is allowed to run the task:
   `score = spare cores × 10 + free GB × 2 − running tasks × 15 (+100 if GPU wanted and present)`.
   Machines with under 10% idle CPU, not enough free memory, on battery, or already running 4 tasks are skipped. Machines a task crashed on are excluded.
3. **Deliver.** The task rides on the machine's next heartbeat response. It is re-sent on every heartbeat until the machine confirms it (idempotent by task ID + attempt).
4. **Run.** sys0 double-checks it is allowed (it never just trusts the node), then runs it under the watchdog.
5. **Report.** The outcome is written to the outbox, then delivered. A `409 stale` reply (the task already moved) is dropped.

## Scheduling details

- **Attempts.** Each placement increments `attempt`. Messages for an old attempt are ignored, so a machine that comes back from the dead cannot overwrite a newer run; it is told to cancel its copy.
- **Offline detection.** No heartbeat for 15s (3 intervals).
- **Grace after node restart.** All machines are treated as freshly seen when the node starts, so a quick node reboot does not reshuffle the pool.
- **Local preference.** The node's own machine gets a small penalty so heavy work prefers other machines and the dashboard stays snappy.

## Code map

```
cmd/
  sys0/          client entry point and CLI
  sys1/          node entry point, CLI, package signing tools
internal/
  agent/         client loop, task runner, config
  cli/           logging, signals, OS service management
  journal/       crash-safe write-ahead log
  marketplace/   package format, signatures, compatibility, builtin catalog/
  node/          node API, scheduler, state, dashboard (web/)
  paths/         per-OS data directories
  priority/      lowest-priority setup per OS
  protocol/      wire types, join codes, spec validation
  resources/     spare-capacity measurement
  setup/         graphical setup wizard (web/)
  tlsutil/       node certificate and pinning
  version/       build metadata
docs/            you are here
site/            the GitHub Pages website
packaging/       installer and package-manager files
```

## Design principles

1. **Never get in the owner's way.** Lowest priority, low memory, no UI after setup.
2. **Never trust the other side.** Clients validate everything the node sends; the node validates everything clients send.
3. **Write before acting.** State changes hit the journal before they take effect.
4. **One binary, no dependencies.** No runtime, no database, no web framework. Containers are optional per machine.
5. **Boring, readable code.** Standard library first.
