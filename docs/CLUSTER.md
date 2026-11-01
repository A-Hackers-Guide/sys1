# Clusters and reaching the node

Two things make a pool work beyond one house and one hub machine: machines being able to **reach** the node from anywhere, and the pool **surviving the loss of the node** itself.

## Reaching the node from anywhere

Machines only ever connect *out* to the node, never the other way round, so only the node needs to be reachable.

### Join codes carry every address

A join code lists every address the node can be reached on, best first:

1. addresses you give with `sys1 --public-addr host:port[,host:port…]` (a domain name, a dynamic DNS name)
2. the node's LAN address
3. VPN addresses: Tailscale (`100.64.0.0/10`), ZeroTier, WireGuard and other tunnel interfaces
4. the public address the router gives it, when **Reachable from the internet** is on (below)
5. in a cluster, every other node's addresses

Machines try them in order, stay with the first that answers, and move on when it stops answering. The node also sends its current list with every heartbeat, so machines keep up when the node's addresses change. Old join codes keep working as long as one of their addresses does.

### Reachable from the internet

On the dashboard, **Add a machine → Reachable from the internet** asks your router to forward the node's client port (7443) to it, using **NAT-PMP** or **UPnP**, which most home routers support. The mapping is renewed every 30 minutes and removed when you switch it off or the node stops.

- It's **off by default**, because it changes your router.
- Only sys1's client connection is opened: TLS 1.3, with the node's certificate pinned by every machine, and a 192-bit join token or per-machine secret on every request. The dashboard stays on the node's own machine.
- If your internet provider puts several customers behind one public address (**carrier-grade NAT**, common on mobile and some fibre and cable plans), no router can make the node reachable. The dashboard detects this and says so. Use a VPN instead: [Tailscale](https://tailscale.com) and [ZeroTier](https://www.zerotier.com) work through any NAT, and their addresses are picked up automatically.

## Clusters: no single point of failure

A single node is a single point of failure: if its machine dies, the pool stops scheduling, and its disk holds the list of everything in pooled storage. A **cluster** of nodes removes that.

```
        ┌────────── Raft (TLS, port 7444) ──────────┐
   ┌────┴────┐            ┌─────────┐            ┌────┴────┐
   │ node A  │◀──────────▶│ node B  │◀──────────▶│ node C  │
   │ leading │            │ standby │            │ standby │
   └────┬────┘            └─────────┘            └─────────┘
        │  machines talk to the leader; standbys send them on
   ┌────┴─────┬──────────┬──────────┐
  sys0      sys0       sys0       sys0
```

- One node **leads** and does all the work: scheduling, storage, the live dashboard.
- Every change it makes is recorded with **[Raft](https://raft.github.io)** (HashiCorp's implementation) and only counts once a **majority** of nodes have it on disk.
- If the leader disappears, the others **elect a new one within seconds**, and it carries on with exactly the same state. Machines notice the old leader is gone and move to the new one on their own.
- Standbys answer machines with "not the leader" (HTTP 421), so they try the next address.

### How many nodes

| Nodes | Survives losing | Notes |
|---|---|---|
| 1 | nothing | The default. Back up the data directory. |
| 2 | nothing | Raft needs a majority: with one of two gone, the other can't safely lead. Only useful as a hot copy. |
| 2 + a witness | any 1 | **The usual home setup**: your main computer, a standby, and a witness on any third machine. |
| **3** | **any 1** | **Recommended** when you have three computers that can each run the pool. |
| 5 | any 2 | For larger pools. |

### Setting one up

On the first node (it keeps everything it already has):

```bash
sys1 --cluster
```

On the dashboard, **Add a machine → Nodes** shows a `sys1 cluster join …` command. On each additional node:

```bash
sys1 cluster join sys1-cluster:eyJh…
sys1 --cluster
```

The first command fetches the cluster's identity over the pinned, encrypted connection: its TLS certificate (so every node has the same fingerprint, and existing join codes keep working) and the pool key (so any node can run pooled storage). The second starts the node; once it is up, it asks the leader to make it a voting member and catches up with the full state. A node is never counted as a voter before it is running, so adding one can't make the cluster lose its majority.

To run nodes as services: `sys1 install --cluster`.

### A standby and a witness

Most pools have one computer that is always on. To keep the pool running when it isn't, add a **standby** (a second full node) and a **witness**:

```bash
# on the standby
sys1 cluster join sys1-cluster:eyJh…
sudo sys1 install --cluster

# on the witness: any third machine, even a Raspberry Pi
sys1 cluster join --witness sys1-cluster:eyJh…
sudo sys1 install --cluster
```

- **The witness only votes.** It keeps a copy of the pool's state, as every member must, but if it is ever elected it hands leadership to a full node at once. Machines are never sent to it, it lends no capacity of its own, and it opens no router port.
- **Why a third machine at all.** Two nodes can't tell "the other one died" from "the link between us broke". If each took over in the second case, the pool would have two nodes in charge giving conflicting orders. With a witness, whichever node can reach the witness has the majority, and the other one stands down.
- **The preferred node.** The node the cluster started on is *preferred*. When it goes down, the standby takes over within seconds and machines follow. When it comes back, it catches up, and once it has answered steadily for a minute, the standby hands leadership back. A node that keeps crashing or drops off a bad link never gets leadership back until it is steady.
- **Changing it.** `sys1 cluster prefer <node name>` on the leader, or **Make preferred** under **Nodes** on its dashboard. A witness can't be preferred.

### Ports

| Port | What | Open to |
|---|---|---|
| 7443/tcp | client API | machines |
| 7444/tcp | Raft between nodes (`--raft-addr`) | the other nodes only |
| 7700/tcp | dashboard | this machine only |

Raft traffic is TLS 1.3 in both directions, and each side checks that the other presents the cluster's certificate, so only members can take part.

### What the dashboard shows

Each node has its own dashboard. On the leader it's live and accepts changes; on a standby a banner names the leader and the page is read-only. **Nodes** lists every member and which one leads.

### Limits today

- Pooled storage's staging area (where chunks pass between machines) is per node. After a leader change, uploads in flight are retried, and snapshots that referenced staged chunks are re-taken.
- Removing a node for good is done from the leader (`LeaveCluster`; a dashboard button is planned). A node that is simply off is just a standby that's down.
- The pool key and the cluster certificate are the same on every node. Treat each node's data directory as sensitive.
