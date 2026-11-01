package node

import (
	"errors"
	"sort"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/protocol"
	"github.com/hashicorp/raft"
)

// The node picks its own standby and witness from the owner's machines
// (see docs/CLUSTER.md), and keeps the number of voting members odd: an
// even number survives no more failures than one fewer, and two voters
// survive none, so a lone standby stays a live copy without a vote until
// a witness joins too.

type hostOrder struct {
	protocol.HostOrder
	at time.Time
}

// hostOrderTimeout is how long a machine has to become a member before
// another one is asked instead.
const hostOrderTimeout = 15 * time.Minute

// steadyMachine is how long a machine must have been in the pool before
// it is trusted with a copy of the whole pool.
func (n *Node) steadyMachine() time.Duration {
	if n.cfg.FastRaft {
		return 0
	}
	return 10 * time.Minute
}

// EnableCluster turns cluster mode on for good and restarts the node,
// which then picks its own standby and witness.
func (n *Node) EnableCluster() error {
	if n.clustered() {
		return nil
	}
	if n.cfg.Restart == nil {
		return errors.New("start the node with --cluster (it isn't running as a service, so it can't restart itself)")
	}
	n.mu.Lock()
	n.settings.Cluster = true
	n.persistLocked(event{Settings: &n.settings})
	n.mu.Unlock()
	n.log.Info("cluster mode on; restarting")
	go func() {
		time.Sleep(time.Second) // let the dashboard get its answer
		n.cfg.Restart()
	}()
	return nil
}

// SetAutoStandby lets the node pick a standby and witness, or stops it.
func (n *Node) SetAutoStandby(on bool) {
	n.mu.Lock()
	n.settings.NoAutoStandby = !on
	n.persistLocked(event{Settings: &n.settings})
	n.mu.Unlock()
}

// hostOrderLocked is the order for a, if it was picked and hasn't joined.
func (n *Node) hostOrderLocked(a *Agent) *protocol.HostOrder {
	o, ok := n.hostOrders[a.ID]
	if !ok {
		return nil
	}
	if a.Resources.Hosting != "" || time.Since(o.at) > hostOrderTimeout {
		delete(n.hostOrders, a.ID)
		return nil
	}
	return &o.HostOrder
}

// chooseHosts asks the best of the owner's machines to be the standby and
// the witness, when the cluster lacks one.
func (n *Node) chooseHosts() {
	if !n.clustered() || !n.leading() || n.Witness() {
		return
	}
	code, err := n.ClusterCode()
	if err != nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.settings.NoAutoStandby {
		return
	}
	c := n.cl
	need := map[string]bool{protocol.RoleStandby: true, protocol.RoleWitness: true}
	for id, m := range c.members {
		if id == c.id {
			continue
		}
		if m.Witness {
			need[protocol.RoleWitness] = false
		} else {
			need[protocol.RoleStandby] = false
		}
	}
	for id, o := range n.hostOrders {
		if time.Since(o.at) > hostOrderTimeout {
			delete(n.hostOrders, id)
			continue
		}
		need[o.Role] = false
	}
	if !need[protocol.RoleStandby] && !need[protocol.RoleWitness] {
		return
	}
	var cands []*Agent
	for _, a := range n.agents {
		r := a.Resources
		if a.Local || a.Trust != protocol.TrustOwner || !r.CanHost || r.Hosting != "" || r.OnBattery ||
			time.Since(a.LastSeen) > 30*time.Second || time.Since(a.Joined) < n.steadyMachine() {
			continue
		}
		if _, asked := n.hostOrders[a.ID]; asked {
			continue
		}
		cands = append(cands, a)
	}
	// The standby may lead the pool, so it gets the machine with the most
	// room; any machine will do for a witness.
	sort.Slice(cands, func(i, j int) bool {
		ri, rj := cands[i].Resources, cands[j].Resources
		if ri.MemTotal != rj.MemTotal {
			return ri.MemTotal > rj.MemTotal
		}
		return cands[i].ID < cands[j].ID
	})
	if n.hostOrders == nil {
		n.hostOrders = map[string]hostOrder{}
	}
	for _, role := range []string{protocol.RoleStandby, protocol.RoleWitness} {
		if !need[role] || len(cands) == 0 {
			continue
		}
		a := cands[0]
		cands = cands[1:]
		n.hostOrders[a.ID] = hostOrder{HostOrder: protocol.HostOrder{Role: role, Code: code}, at: time.Now()}
		n.log.Info("cluster: asking a machine to be the pool's "+role, "machine", a.Name)
	}
}

// balanceVoters gives votes to members so that as many as possible vote
// while the number of voters stays odd. The leader keeps its vote; then
// members that already vote, then witnesses, then the rest.
func (n *Node) balanceVoters() {
	c := n.cl
	if !c.active.Load() {
		return
	}
	c.balanceMu.Lock() // one change of votes at a time
	defer c.balanceMu.Unlock()
	f := c.r.GetConfiguration()
	if f.Error() != nil {
		return
	}
	servers := append([]raft.Server(nil), f.Configuration().Servers...)
	n.mu.Lock()
	witness := map[string]bool{}
	for id, m := range c.members {
		witness[id] = m.Witness
	}
	n.mu.Unlock()
	rank := func(s raft.Server) int {
		switch {
		case string(s.ID) == c.id:
			return 0
		case s.Suffrage == raft.Voter:
			return 1
		case witness[string(s.ID)]:
			return 2
		}
		return 3
	}
	sort.SliceStable(servers, func(i, j int) bool { return rank(servers[i]) < rank(servers[j]) })
	want := len(servers)
	if want%2 == 0 {
		want--
	}
	for i, s := range servers {
		var err error
		switch {
		case i < want && s.Suffrage != raft.Voter:
			err = c.r.AddVoter(s.ID, s.Address, 0, 10*time.Second).Error()
		case i >= want && s.Suffrage == raft.Voter:
			err = c.r.DemoteVoter(s.ID, 0, 10*time.Second).Error()
		}
		if err != nil {
			n.log.Warn("cluster: couldn't change who votes", "err", err)
			return
		}
	}
}
