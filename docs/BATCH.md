# Batch jobs, groups and GPUs

Three ways to use more than one machine at once.

| | For | Example |
|---|---|---|
| **Batch job** | Work that splits into independent pieces | Render 2,000 frames, encode a video library, crunch a dataset |
| **Group** | Pieces that must talk to each other while they run | One AI model split across several machines |
| **GPU requirements** | Work that needs a particular amount of GPU memory | "Run where a GPU has 12 GB free" |

## Batch jobs

Add `"shards": N` to a package's `run`. Deploying it creates **one job of N pieces**. Each piece is an ordinary task: placed on the best machine, watched by the watchdog, moved when its machine disappears, and retried elsewhere after 5 crashes in a row. Pieces don't depend on each other, so one bad machine never sinks the job.

Each piece gets:

| Variable | Value |
|---|---|
| `SYS1_SHARD_INDEX` | `0` … `N-1` |
| `SYS1_SHARD_COUNT` | `N` |

and decides from those which part of the work is its own. For example, frames `index, index+N, index+2N, …`.

### Checking results on several machines

On machines lent by people you don't know, a machine could return wrong results, by accident or on purpose. Add `"verify": N` (2 to 5) and each piece runs on **N different machines**. It is accepted only when N of them produced the same output, compared by a hash of the plain contents (paths, permissions, file contents). Until they agree, extra copies run on other machines, up to N+2 in total; after that, the piece is marked as failed.

A machine whose output disagrees with the accepted answer gets a mark on its record, shown on the dashboard. At 3 marks it is kept off verified work. In the built-in Batch job package, choose how many machines check each piece when you deploy.

### Results

Give the package a pooled volume, such as `"volumes": ["/output"]`, and write results there. Each piece's output is saved to [pooled storage](STORAGE.md) when it finishes. On the dashboard, **Collect results** restores every piece's output into one folder on one of your machines:

```
results/
  shard-0000/vol0/…
  shard-0001/vol0/…
  …
```

### The built-in "Batch job" package

Runs any shell command in any container, split into pieces:

| Input | Default |
|---|---|
| Container image | `alpine:3` |
| Command | `echo "piece $SYS1_SHARD_INDEX of $SYS1_SHARD_COUNT ran on $(hostname)" > /output/result.txt` |
| Number of pieces | `8` |

Pieces run sandboxed with **no network**, 1 CPU and 512 MB each. For more, write a package of your own ([Packages](PACKAGES.md)).

## Groups

Some work needs its pieces to talk: a model split across machines has to pass activations between them on every step. A **group** is one *head* plus N *workers*:

```json
"run": {
  "kind": "container", "image": "…", "network": true,
  "command": ["head-program", "--peers", "${SYS1_PEERS}"],
  "group": {
    "workers": 2,
    "peerPort": 50052,
    "worker": { "kind": "container", "image": "…", "command": ["worker-program", "--port", "${SYS1_PEER_PORT}"] }
  }
}
```

- **Every member runs on a different machine.**
- Worker *i* listens on `peerPort + i`, given to it as `SYS1_PEER_PORT` (and `${SYS1_PEER_PORT}` in its command). sys0 publishes that port.
- The **head waits** until every worker is running, then starts with `SYS1_PEERS=host:port,host:port,…` (and `${SYS1_PEERS}` in its command).
- If a worker's machine goes away, the worker moves, and the **head restarts with the new set of peers**.
- When the head finishes or is stopped, the whole group stops.

Machines reach each other directly on their LAN addresses, so group members need to be on the same network (or a VPN) until [NAT traversal](ROADMAP.md) lands in v0.5.

### "AI model split across machines"

The built-in `llama-distributed` package uses a group to run one large language model across several machines with [llama.cpp's RPC backend](https://github.com/ggml-org/llama.cpp/tree/master/tools/rpc):

- **Workers** run `ggml-rpc-server`. Each one lends its memory and compute.
- **The head** runs `llama-server --rpc <workers>`, downloads the model from Hugging Face, spreads its layers across every worker in proportion to their memory, and serves an **OpenAI-compatible API on port 8080**.

The official llama.cpp images are built without RPC, so sys1 builds its own: `ghcr.io/a-hackers-guide/llama-rpc`, from a pinned llama.cpp release, for amd64 and arm64. On every change, CI starts two workers and a head, loads a model split across them, generates text, and checks both workers were used.

> **Owner machines on a trusted network only.** llama.cpp's RPC protocol has no password and its authors warn never to expose it on an open network. The package is marked `ownerOnly`, so it never runs on machines lent by other people, and the dashboard lists the ports it opens before you deploy.

Today's image is CPU-only. GPU builds (CUDA, Vulkan) are next.

## GPUs

sys0 reports every GPU it can find:

| Source | Finds |
|---|---|
| `nvidia-smi` (any OS) | NVIDIA GPUs with total and free memory |
| Linux `/sys/class/drm` | AMD GPUs with total and free memory; Intel GPUs |
| Windows video controllers | AMD and Intel GPUs (memory reported up to 4 GB) |
| Apple Silicon | the GPU and the share of unified memory it can use (about two thirds) |

Packages can ask for:

| Field | Meaning |
|---|---|
| `"gpu": true` | Prefer machines with a GPU, and use it |
| `"minVramMB": 12000` | Only run where a usable GPU has at least this much free memory |

The scheduler prefers the machine with the most free GPU memory. Containers get GPU access the right way for each vendor: NVIDIA through the NVIDIA container toolkit (`--gpus all`, or CDI on Podman), AMD and Intel through `/dev/kfd` and `/dev/dri` on Linux. Docker on macOS and Windows can't pass Apple, AMD or Intel GPUs into containers, so container packages are never sent there for a GPU. Native programs on owner machines can use any GPU.
