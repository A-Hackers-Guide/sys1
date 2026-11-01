package node

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/hashicorp/raft"
)

// Witnesses and failback; see the top of cluster.go.

// Witness reports whether this node only votes in its cluster. A witness
// lends no capacity of its own and opens no router port: it isn't the
// pool's node, only its tie-breaker.
func (n *Node) Witness() bool { return n.cl != nil && n.cl.witness }

// handOff gives leadership away from a witness to a full node, trying
// again while no full node can take it.
func (n *Node) handOff(ctx context.Context) {
	c := n.cl
	for ctx.Err() == nil && c.r.State() == raft.Leader {
		if err := c.r.LeadershipTransfer().Error(); err == nil {
			n.log.Info("cluster: a witness was elected; handed leadership to a full node")
			return
		}
		select {
		case <-ctx.Done():
		case <-time.After(n.raftPause()):
		}
	}
}

// watchPeers remembers when each node last missed a heartbeat from this
// one, while it leads.
func (n *Node) watchPeers(ctx context.Context, obs chan raft.Observation) {
	c := n.cl
	for {
		select {
		case <-ctx.Done():
			return
		case o := <-obs:
			c.peerMu.Lock()
			switch d := o.Data.(type) {
			case raft.FailedHeartbeatObservation:
				c.peerFailed[string(d.PeerID)] = time.Now()
			case raft.ResumedHeartbeatObservation:
				// Counted as failing until steady for a while (see failback).
				c.peerFailed[string(d.PeerID)] = time.Now()
			}
			c.peerMu.Unlock()
		}
	}
}

// steadyFor is how long the preferred node must answer every heartbeat,
// with this node leading, before leadership goes back to it. A node that
// is flapping (crashing, or on a bad link) never gets leadership.
func (n *Node) steadyFor() time.Duration {
	if n.cfg.FastRaft {
		return 2 * time.Second
	}
	return time.Minute
}

func (n *Node) raftPause() time.Duration {
	if n.cfg.FastRaft {
		return 500 * time.Millisecond
	}
	return 10 * time.Second
}

// failback hands leadership to the preferred node once it is back.
func (n *Node) failback(ctx context.Context) {
	c := n.cl
	wait := n.raftPause()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = n.raftPause()
		id, addr, ok := n.failbackTarget()
		if !ok {
			continue
		}
		if err := c.r.LeadershipTransferToServer(raft.ServerID(id), raft.ServerAddress(addr)).Error(); err != nil {
			n.log.Warn("cluster: couldn't hand leadership back to the preferred node yet", "err", err)
			wait = 6 * n.raftPause()
			continue
		}
		n.log.Info("cluster: the preferred node is back; handed leadership to it")
	}
}

// failbackTarget is the preferred node, if this node leads, the preferred
// node is another full voter, and it has answered steadily for steadyFor.
func (n *Node) failbackTarget() (id, addr string, ok bool) {
	c := n.cl
	since := c.leaderSince.Load()
	if !c.active.Load() || since == 0 || time.Since(time.Unix(0, since)) < n.steadyFor() {
		return "", "", false
	}
	n.mu.Lock()
	id = c.prefer
	m, known := c.members[id]
	n.mu.Unlock()
	if id == "" || id == c.id || !known || m.Witness {
		return "", "", false
	}
	c.peerMu.Lock()
	failed, seen := c.peerFailed[id]
	c.peerMu.Unlock()
	if seen && time.Since(failed) < n.steadyFor() {
		return "", "", false
	}
	f := c.r.GetConfiguration()
	if f.Error() != nil {
		return "", "", false
	}
	for _, s := range f.Configuration().Servers {
		if string(s.ID) == id && s.Suffrage == raft.Voter {
			return id, string(s.Address), true
		}
	}
	return "", "", false
}

// Prefer makes the member named by idOrName (its ID or host name) the one
// that leads whenever it can. Asked of the leader.
func (n *Node) Prefer(idOrName string) error {
	if !n.clustered() || !n.leading() {
		return ErrStandby
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	var found *Member
	for _, m := range n.cl.members {
		if m.ID == idOrName || strings.EqualFold(m.Name, idOrName) {
			if found != nil && found.ID != m.ID {
				return errors.New("two nodes have that name; use the node's ID")
			}
			found = &m
		}
	}
	switch {
	case found == nil:
		return errors.New("no node in this cluster has that name or ID")
	case found.Witness:
		return errors.New("a witness only votes; it can't be the preferred node")
	}
	n.proposeLocked(entry{Prefer: found.ID})
	return nil
}
