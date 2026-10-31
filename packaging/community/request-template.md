**Title:** Package request: sys1 (pool spare capacity of many computers into one cloud)

Hello, and thank you for maintaining **<REPOSITORY>**.

We'd love for **sys1** to be available to <DISTRIBUTION> users and wanted to ask whether someone would be interested in packaging it. We're happy to help in any way the project prefers.

**What it is:** sys1 pools the spare CPU, memory, disk and GPU of many computers into one place to run websites, media servers, backups and AI models. It ships as two binaries: `sys0` (a lowest-priority background client) and `sys1` (the node and its web dashboard).

- Homepage: https://a-hackers-guide.github.io/sys1/
- Source: https://github.com/A-Hackers-Guide/sys1
- Latest release: <VERSION>, https://github.com/A-Hackers-Guide/sys1/releases/tag/<VERSION>
- License: **Unlicense** (public domain)
- Language: Go (≥ 1.26), **no CGO**, three small dependencies (gopsutil, kardianos/service, x/sys)
- Build: `CGO_ENABLED=0 go build -trimpath ./cmd/sys0 ./cmd/sys1`
- Tests: `go test ./...`
- Checksums: `checksums.txt` (SHA-256) on every release
- Runtime dependencies: none. Docker or Podman is optional, to run container workloads.

It installs no files outside its data directory until the user runs `sys0 install` / `sys1 install`, which registers a <SERVICE MANAGER> service.

If a draft <PACKAGE FORMAT> recipe would help, we can write one following your guidelines. Please tell us anything that would make this easier.

Thank you!
— the sys1 maintainers
