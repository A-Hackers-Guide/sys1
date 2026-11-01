# sys1 documentation

Start with **[Getting started](GETTING-STARTED.md)** if you just want to use sys1.

## For everyone

- [Getting started](GETTING-STARTED.md): set up a node, add machines, deploy apps
- [FAQ](FAQ.md): common questions
- [Platforms](PLATFORMS.md): what runs where
- [Roadmap](ROADMAP.md): what's coming

## How it works

- [Architecture](ARCHITECTURE.md): components, data flow, code map
- [Reliability](RELIABILITY.md): fast save, watchdog, failover, and the exact guarantees
- [Storage](STORAGE.md): pooled, encrypted, replicated storage for app data and backups
- [Batch jobs, groups and GPUs](BATCH.md): splitting work and AI models across machines
- [Virtual machines, desktops and gaming](VMS.md): whole operating systems as apps, screens in the browser
- [Clusters and reaching the node](CLUSTER.md): joining from anywhere, and several nodes for no single point of failure
- [Security model](SECURITY-MODEL.md): trust levels, sandboxing, threat model
- [Protocol](PROTOCOL.md): every message between sys0 and sys1

## Building on it

- [Marketplace packages](PACKAGES.md): write, sign and publish packages
- [Distribution](DISTRIBUTION.md): every package manager and how releases reach it
- [Contributing](../CONTRIBUTING.md): how to help

## Glossary

| Term | Meaning |
|---|---|
| **Pool** | All the machines joined to one node, and their combined spare capacity. |
| **Node** | The machine running `sys1`. It schedules work and serves the dashboard. Also a client: its own spare capacity is in the pool. |
| **Client** | A machine running `sys0`. |
| **Join code** | A single string (`sys1-join:…`) holding the node's address, its certificate fingerprint and a join token. |
| **Owner machine** | A client whose owner said "this machine is mine". It may run native programs. |
| **Community machine** | A client shared by someone else. Only sandboxed containers run on it. |
| **Task / app** | One deployed package running on the pool. |
| **Checkpoint** | Up to 8 MB of state an app saves to `$SYS1_CHECKPOINT` so it can resume on another machine. |
| **Chunk** | Up to 4 MiB of data, encrypted on the machine it came from, named by the hash of its encrypted bytes. |
| **Snapshot** | A folder (an app's volumes, or a backed-up folder) turned into chunks at one moment. |
| **Journal** | The crash-safe write-ahead log holding the node's state and each client's outbox. |
