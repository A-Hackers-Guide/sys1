# FAQ

### Will sys0 slow down my computer or my games?

It's designed not to. sys0 runs at the lowest priority your OS has, for CPU *and* disk, and containers get the smallest possible CPU weight. The OS gives them time only when nothing else wants it. sys0 also stops taking new work when the machine is on battery or its CPU is under 10% idle.

You may still notice heavy work in a few cases: memory use (RAM can't be "lowest priority" the way CPU can), GPU use, and network bandwidth. Package memory limits and per-machine task limits keep this in check; finer controls are on the [roadmap](ROADMAP.md).

### Is it safe to let other people's pools use my machine?

Choose **"I'm sharing it"** in the installer. Then only sandboxed containers with no extra privileges can run, never native programs. See the [security model](SECURITY-MODEL.md) for the honest details, including what's still being hardened (network egress controls).

### Is it safe to run my stuff on other people's machines?

Whoever owns a machine can, in principle, look at what runs on it. Don't put secrets or private data on machines you don't trust. Keep sensitive apps on your own (owner) machines.

### Can I combine all my computers into one big computer?

Not as a single virtual machine. Networks are about 1,000× too slow for that (see [Architecture](ARCHITECTURE.md#why-not-one-giant-vm-across-all-machines)). Instead, sys1 runs each app on the best machine and moves it when needed, and pools things that really can be split: storage, batch jobs, some AI work.

### Do I need Docker?

For most marketplace apps, yes, on the machines that should run them. Machines without it still join and can run native packages if they're owner machines.

### What happens if the node goes down?

Clients keep their apps running and keep retrying. When the node is back, everything continues where it was. The node's state is in a crash-safe journal. A multi-node setup with no single point of failure is on the roadmap.

### Does it work across the internet, not just my home network?

Yes, if clients can reach the node's port 7443: a VPN like Tailscale or WireGuard is easiest, or a port forward. Use `sys1 --public-addr your.host:7443`. Built-in NAT traversal is on the roadmap.

### Why is it public domain?

Because we want it used everywhere by everyone, with no legal friction. You can do anything with it. We only ask, nicely, that you share issues and improvements back, and share sys1 with others.

### Why "sys0" and "sys1"?

sys1 is the system you see; sys0 is the layer underneath that you don't.

### Can I use the dashboard from my phone?

The dashboard is bound to the node machine for safety. To reach it from elsewhere, use SSH port forwarding (`ssh -L 7700:127.0.0.1:7700 node-machine`) or run the node with `--gui 0.0.0.0:7700` on a trusted network only (it warns you). A proper remote access mode is on the roadmap.

### How do I uninstall?

`sys0 uninstall` (or `sys1 uninstall`) removes the service. Then delete the binary and, if you like, the data directory listed by `sys0 status`.
